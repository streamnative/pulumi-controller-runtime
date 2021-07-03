// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package reconcile

import (
	"context"
	"github.com/pkg/errors"

	ot "github.com/opentracing/opentracing-go"
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/result"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	snbackend "github.com/streamnative/pulumi-controller/sample/pkg/reconciler-runtime/backend"
	otbridge "go.opentelemetry.io/otel/bridge/opentracing"
	apitrace "go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	// RuntimeEmbedded identifies thw 'embedded' language runtime
	RuntimeEmbedded = "embedded"
)

// Program generates a resource graph and processes resources changes for a Kubernetes object.
type Program interface {
	GetObject() client.Object
	Generate(ctx *pulumi.Context) error
	UpdateStatus(ctx context.Context, changes StackChanges) error
}

// PulumiReconciler is a reconciler for a Kubernetes object, backed by a Pulumi stack of resources.
type PulumiReconciler struct {
	apitrace.Tracer
	client.Client
	FinalizerName string
	createPluginContext CreatePluginContextFunc  // obtains a plugin host for engine operations

	// Pulumi options
	backend     snbackend.Backend // the backend for stack persistence
	hostContext plugin.Context   // the plugin host context for engine operations
	proj        *workspace.Project
}

type ConfigMap config.Map

type ReconcileOptions struct {
	StackConfig  config.Map
	UpdateStatus UpdateStatusFunc
}

type UpdateStatusFunc func(ctx context.Context, obj_ client.Object, chg StackChanges) error

func (r *PulumiReconciler) ReconcileObject(ctx context.Context, obj client.Object, opts ReconcileOptions) (reconcileResult reconcile.Result, err error) {
	log := log.FromContext(ctx)
	ctx = WithOpenTracingContext(ctx)

	if obj.GetDeletionTimestamp().IsZero() {
		if !controllerutil.ContainsFinalizer(obj, r.FinalizerName) {
			controllerutil.AddFinalizer(obj, r.FinalizerName)
			if err := r.Update(ctx, obj); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	stack, err := r.getOrCreateStack(ctx, obj)
	if err != nil {
		if err == snbackend.ErrSnapshotGenerationMismatch {
			// the object is stale; requeue.
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	defer stack.Close()

	// Prepare a stack configuration
	v, err := json.Marshal(obj)
	if err != nil {
		return ctrl.Result{}, err
	}
	cfg := config.Map{
		config.MustMakeKey(r.proj.Name.String(), "obj"): config.NewObjectValue(string(v)),
	}

	// Use the appropriate stack operation
	updateFunc := func(h snbackend.UpdateEventHandler, dryRun bool) (engine.ResourceChanges, result.Result) {
		if obj.GetDeletionTimestamp().IsZero() {
			return stack.Update(ctx, snbackend.UpdateOpts{DryRun: dryRun, Config: cfg, EventHandler: h})
		} else {
			return stack.Destroy(ctx, snbackend.DestroyOpts{DryRun: dryRun, Config: cfg, EventHandler: h})
		}
	}
	updateStatusFunc := func(ctx context.Context, chg StackChanges) error {
		span, ctx := ot.StartSpanFromContext(ctx, "UpdateStatus")
		defer func() {
			span.Finish()
		}()
		if err = opts.UpdateStatus(ctx, obj, chg); err != nil {
			log.Error(err, "Unable to update status", "obj", obj)
			return err
		}
		return nil
	}

	// Preview for status update
	previewChanges := NewStackChanges(true)
	previewSummary, previewRes := updateFunc(&previewChanges, true)
	if previewRes != nil && previewRes.Error() != nil {
		// the preview failed due to an internal error, not due to a problem with applying the stack.
		return reconcile.Result{}, previewRes.Error()
	}
	if previewRes != nil && previewRes.IsBail() {
		// the preview failed due to a problem with applying the stack.
		log.Info("Pulumi preview bailed; will retry")
		return reconcile.Result{Requeue: true}, nil
	}
	if err = updateStatusFunc(ctx, previewChanges); err != nil {
		log.Error(err, "Unable to update status", "obj", obj)
		return reconcile.Result{Requeue: true}, nil
	}

	if !previewSummary.HasChanges() {
		log.Info("Pulumi up/destroy skipped (no changes)")
	} else {
		// Run the deployment operation
		updateChanges := NewStackChanges(false)
		updateSummary, updateRes := updateFunc(&updateChanges, false)
		if updateRes != nil && updateRes.Error() != nil {
			// the update failed due to an internal error, not due to a problem with applying the stack.
			return reconcile.Result{}, updateRes.Error()
		}

		if updateRes != nil && updateRes.IsBail() {
			// the update failed due to a problem with applying the stack.  Some progress might have occurred.
			log.Info("Pulumi up/destroy bailed; will retry")
			return reconcile.Result{Requeue: true}, nil
		}

		log.Info("Pulumi up/destroy succeeded", "summary", updateSummary)

		if err = updateStatusFunc(ctx, updateChanges); err != nil {
			return reconcile.Result{Requeue: true}, nil
		}

		// We need to update status again; be sure to requeue.
		reconcileResult.Requeue = true
	}

	if !obj.GetDeletionTimestamp().IsZero() {
		if controllerutil.ContainsFinalizer(obj, r.FinalizerName) {
			controllerutil.RemoveFinalizer(obj, r.FinalizerName)
			if err := r.Update(ctx, obj); err != nil {
				return ctrl.Result{}, err
			}
			reconcileResult.Requeue = false
		}
	}

	return reconcileResult, nil
}

func (r *PulumiReconciler) getOrCreateStack(ctx context.Context, obj client.Object) (stack snbackend.Stack, err error) {
	hostCtx, err := r.createPluginContext(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "unable to crate a Pulumi plugin context")
	}
	r.hostContext = *hostCtx

	stack, err = r.backend.LoadStack(ctx, r.hostContext, r.proj, obj)
	return
}

// WithOpenTracingContext converts an OpenTelemetry (OTEL) context into an OpenTracing (OT) context.
// This allows an OTEL span to serve as the parent of an OT span.
func WithOpenTracingContext(ctx context.Context) context.Context {
	t, ok := ot.GlobalTracer().(*otbridge.BridgeTracer)
	if !ok {
		return ctx
	}
	return t.ContextWithBridgeSpan(ctx, apitrace.SpanFromContext(ctx))
}
