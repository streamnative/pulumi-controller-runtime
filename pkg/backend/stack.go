// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package backend

import (
	"context"
	"io"
	"sync"

	ot "github.com/opentracing/opentracing-go"
	otlog "github.com/opentracing/opentracing-go/log"
	"github.com/pkg/errors"
	pulumibackend "github.com/pulumi/pulumi/pkg/v3/backend"
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/pkg/v3/resource/deploy"
	"github.com/pulumi/pulumi/pkg/v3/secrets"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/result"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Stack is used to manage stacks of resources against a backend.
type Stack interface {
	io.Closer
	Refresh(ctx context.Context, opts RefreshOpts) (engine.ResourceChanges, result.Result)
	Update(ctx context.Context, opts UpdateOpts) (engine.ResourceChanges, result.Result)
	Destroy(ctx context.Context, opts DestroyOpts) (engine.ResourceChanges, result.Result)
}

type RefreshOpts struct {
	DryRun       bool
	Config       config.Map
	EventHandler UpdateEventHandler
}

type UpdateOpts struct {
	DryRun       bool
	Config       config.Map
	EventHandler UpdateEventHandler
}

type DestroyOpts struct {
	DryRun       bool
	Config       config.Map
	EventHandler UpdateEventHandler
}

// kubeStack contains the information needed to run a series of engine operations for a given stack.
type kubeStack struct {
	b          *defaultBackend
	ctx        plugin.Context
	project    *workspace.Project
	ref        StackReference
	obj        client.Object
	snap       *deploy.Snapshot
	snapHandle SnapshotHandle
	m          sync.Mutex
}

type UpdateEventHandler interface {
	EngineEvent(ctx context.Context, event engine.Event)
}

func newStack(b *defaultBackend, pluginContext plugin.Context,
	project *workspace.Project,
	ref StackReference, obj client.Object,
	snap *deploy.Snapshot, snapHandle SnapshotHandle) *kubeStack {
	return &kubeStack{
		b:          b,
		ctx:       pluginContext,
		project:    project,
		ref:        ref,
		obj:        obj,
		snap:       snap,
		snapHandle: snapHandle,
	}
}

// region Stack

func (s *kubeStack) Close() error {
	s.m.Lock()
	defer s.m.Unlock()
	return s.ctx.Close()
}

func (s *kubeStack) Refresh(ctx context.Context, opts RefreshOpts) (changes engine.ResourceChanges, res result.Result) {
	span, ctx := ot.StartSpanFromContext(ctx, "Refresh")
	defer func() {
		span.LogFields(logUpdateResult(changes, res)...)
		span.Finish()
	}()
	span.LogFields(otlog.Bool("pulumi.dry_run", opts.DryRun))
	upd, err := s.newUpdate(ctx, opts.Config, opts.EventHandler)
	if err != nil {
		return nil, result.FromError(err)
	}
	changes, res = s.run(ctx, upd, engine.Refresh, engine.UpdateOptions{Host: wrapHost(s.ctx.Host)}, opts.DryRun)
	return
}

func (s *kubeStack) Update(ctx context.Context, opts UpdateOpts) (changes engine.ResourceChanges, res result.Result) {
	span, ctx := ot.StartSpanFromContext(ctx, "Update")
	defer func() {
		span.LogFields(logUpdateResult(changes, res)...)
		span.Finish()
	}()
	span.LogFields(otlog.Bool("pulumi.dry_run", opts.DryRun))
	upd, err := s.newUpdate(ctx, opts.Config, opts.EventHandler)
	if err != nil {
		return nil, result.FromError(err)
	}
	changes, res = s.run(ctx, upd, engine.Update, engine.UpdateOptions{Host: wrapHost(s.ctx.Host)}, opts.DryRun)
	return
}

func (s *kubeStack) Destroy(ctx context.Context, opts DestroyOpts) (changes engine.ResourceChanges, res result.Result) {
	span, ctx := ot.StartSpanFromContext(ctx, "Update")
	defer func() {
		span.LogFields(logUpdateResult(changes, res)...)
		span.Finish()
	}()
	span.LogFields(otlog.Bool("pulumi.dry_run", opts.DryRun))
	upd, err := s.newUpdate(ctx, opts.Config, opts.EventHandler)
	if err != nil {
		return nil, result.FromError(err)
	}
	changes, res = s.run(ctx, upd, engine.Destroy, engine.UpdateOptions{Host: wrapHost(s.ctx.Host)}, opts.DryRun)
	return
}

func (s *kubeStack) newUpdate(ctx context.Context, cfg config.Map, h UpdateEventHandler) (*update, error) {
	log := log.FromContext(ctx)

	// Prepare an update structure
	events := make(chan engine.Event)
	update := &update{
		ctx:    ctx,
		stack:  s,
		Proj:   s.project,
		Events: events,
	}

	t, err := s.getTarget(cfg)
	if err != nil {
		return nil, err
	}
	update.Target = t
	go func() {
		// Drain the engine events
		for e := range events {
			log.Info("engine event", "e", e)
			if h != nil {
				h.EngineEvent(ctx, e)
			}
		}
	}()
	return update, nil
}

func (s *kubeStack) run(ctx context.Context, upd *update, op engineOp, engineOpts engine.UpdateOptions, dryRun bool) (changes engine.ResourceChanges, res result.Result) {
	// Prepare a backend context to invoke an engine operation
	bctx := &defaultBackendContext{
		UpdateInfo:        upd,
		SnapshotPersister: upd,
		Events:            upd.Events,
	}

	// Run the engine operation
	changes, res = s.b.runEngineAction(ctx, bctx, op, engineOpts, applyOptions{DryRun: dryRun})
	return
}

// GetTarget makes a deployment target for the stack.
// The Pulumi engine uses the supplied language runtime as a 'source' of resource declarations,
// that are applied to a 'target' consisting of state and configuration.
func (s *kubeStack) getTarget(cfg config.Map) (*deploy.Target, error) {
	dec, err := s.b.secretsManager.Decrypter()
	if err != nil {
		return nil, errors.Wrap(err, "unable to obtain a decryptor")
	}
	if cfg == nil {
		cfg = config.Map{}
	}
	s.m.Lock()
	defer s.m.Unlock()
	snap := s.snap

	return &deploy.Target{
		Name:      s.ref.Name(),
		Config:    cfg,
		Decrypter: dec,
		Snapshot:  snap,
	}, nil
}

func logUpdateResult(changes engine.ResourceChanges, result result.Result) []otlog.Field {
	f := []otlog.Field{
		otlog.Bool("pulumi.has_changes", changes.HasChanges()),
	}
	if result != nil {
		if result.IsBail() {
			f = append(f, otlog.Bool("pulumi.is_bail", result.IsBail()))
		} else {
			f = append(f, otlog.Error(result.Error()))
		}
	}
	return f
}

// endregion

// region update

// update is an implementation of engine.UpdateInfo that conveys information about
// the deployment operation.  It implements SnapshotPersister to receive state updates.
type update struct {
	// the context of the update
	ctx context.Context
	// the stack being operated on
	stack *kubeStack
	// the project being operated on
	Proj *workspace.Project
	// the target of the operation
	Target *deploy.Target
	// a channel for engine events
	Events chan<- engine.Event
}

// region UpdateInfo

var _ engine.UpdateInfo = &update{}

func (u *update) GetRoot() string {
	return ""
}

func (u *update) GetProject() *workspace.Project {
	return u.Proj
}

func (u *update) GetTarget() *deploy.Target {
	return u.Target
}

// endregion

// region SnapshotPersister

var _ pulumibackend.SnapshotPersister = &update{}

func (u *update) Save(snap *deploy.Snapshot) error {
	handle, err := u.stack.b.snapshotter.SetSnapshot(u.ctx, u.stack.obj, snap, u.stack.snapHandle)
	if err != nil {
		return err
	}

	// retain the snapshot as the basis for any subsequent operation
	u.stack.m.Lock()
	defer u.stack.m.Unlock()
	u.stack.snap = snap
	u.stack.snapHandle = handle
	return nil
}

func (u *update) SecretsManager() secrets.Manager {
	return u.stack.b.secretsManager
}

// endregion

// endregion

// region StackReferences

func newStackReferenceForObject(obj client.Object) objectReference {
	return objectReference{Namespace: obj.GetNamespace(), UID: obj.GetUID()}
}

// ParseStackReference takes a string representation and parses it to a stack reference,
// representing a Kubernetes object that is implemented using a Pulumi stack.
//
// The expected format is `namespace/uid`.
func parseStackReference(s string) (objectReference, error) {
	if !tokens.IsQName(s) {
		return objectReference{}, errors.Errorf("invalid stack reference: %s", s)
	}
	n := tokens.QName(s)
	return objectReference{Namespace: n.Namespace().String(), UID: types.UID(n.Name().String())}, nil
}

// objectReference is a stack reference to a Kubernetes object.
type objectReference struct {
	UID       types.UID
	Namespace string
}

var _ StackReference = &objectReference{}

func (r objectReference) String() string {
	return r.Name().String()
}

func (r objectReference) Name() tokens.QName {
	return tokens.QName(r.Namespace + tokens.QNameDelimiter + string(r.UID))
}

// endregion

func wrapHost(host plugin.Host) plugin.Host {
	return &wrappedHost{
		Host: host,
	}
}

type wrappedHost struct {
	plugin.Host
}

func (h *wrappedHost) Close() error {
	return nil
}
