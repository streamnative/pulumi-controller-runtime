// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package backend

import (
	"context"
	ot "github.com/opentracing/opentracing-go"
	"github.com/pkg/errors"
	pulumibackend "github.com/pulumi/pulumi/pkg/v3/backend"
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/pkg/v3/secrets"
	"github.com/pulumi/pulumi/pkg/v3/util/cancel"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/result"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	ErrSnapshotGenerationMismatch = errors.New("ErrSnapshotGenerationMismatch")
)

type StackReference pulumibackend.StackReference

// Backend defines an interface for executing stacks against a particular state backend.
type Backend interface {
	// ParseStackReference takes a string representation and parses it to a reference which may be used for other
	// methods in this backend.
	ParseStackReference(s string) (StackReference, error)

	// LoadStack obtains a stack interface for execution purposes.
	LoadStack(ctx context.Context, pluginCtx plugin.Context, project *workspace.Project, obj client.Object) (Stack, error)
}

type defaultBackend struct {
	// snapshotter implements snapshot persistence
	snapshotter SnapshotStorage

	// secretsManager encrypts and decrypts secrets in the stack configuration and state
	secretsManager secrets.Manager

	// project is the Pulumi project manifest associated with the backend
	project *workspace.Project
}

// NewDefaultBackend creates a backend that stores state into Kubernetes secrets.
func NewDefaultBackend(snapshotter SnapshotStorage, secretsManager secrets.Manager, project *workspace.Project) *defaultBackend {
	b := &defaultBackend{
		snapshotter:    snapshotter,
		secretsManager: secretsManager,
		project:        project,
	}
	return b
}

var _ Backend = &defaultBackend{}

func (b *defaultBackend) ParseStackReference(s string) (StackReference, error) {
	ref, err := parseStackReference(s)
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func (b *defaultBackend) LoadStack(ctx context.Context,
	pluginContext plugin.Context,
	project *workspace.Project, obj client.Object) (Stack, error) {

	ref := newStackReferenceForObject(obj)

	// Load the snapshot for the stack.
	// note that snap may be nil if the stack is new.
	snap, handle, err := b.snapshotter.GetSnapshot(ctx, obj)
	if err != nil {
		return nil, err
	}

	return newStack(b, pluginContext, project, ref, obj, snap, handle), nil
}

type defaultBackendContext struct {
	// provides information about the target of the engine operation
	UpdateInfo engine.UpdateInfo
	// persists snapshots of stack state
	SnapshotPersister pulumibackend.SnapshotPersister
	// a channel for engine events
	Events chan<- engine.Event
}

type applyOptions struct {
	// DryRun indicates if the update should not change any resource state and instead just preview changes.
	DryRun bool
}

type engineOp func(engine.UpdateInfo, *engine.Context, engine.UpdateOptions, bool) (engine.ResourceChanges, result.Result)

// runEngineAction performs an update using the Pulumi engine.
func (b *defaultBackend) runEngineAction(
	ctx context.Context, backendCtx *defaultBackendContext, op engineOp, engineOpts engine.UpdateOptions, opts applyOptions) (engine.ResourceChanges, result.Result) {

	// Create a cancellable context
	cancelCtx, cancelSrc := cancel.NewContext(context.Background())
	done := make(chan bool)
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			cancelSrc.Cancel()
		case <-done:
		}
	}()

	// Initialize the Pulumi engine
	snapshotManager := pulumibackend.NewSnapshotManager(backendCtx.SnapshotPersister, backendCtx.UpdateInfo.GetTarget().Snapshot)
	engineCtx := &engine.Context{
		Cancel:          cancelCtx,
		Events:          backendCtx.Events,
		SnapshotManager: snapshotManager,
		BackendClient:   b.snapshotter,
	}
	if parentSpan := ot.SpanFromContext(ctx); parentSpan != nil {
		engineCtx.ParentSpan = parentSpan.Context()
	}

	// Run the operation.
	changes, res := op(backendCtx.UpdateInfo, engineCtx, engineOpts, opts.DryRun)

	// Cleanup
	contract.IgnoreClose(snapshotManager)

	return changes, res
}
