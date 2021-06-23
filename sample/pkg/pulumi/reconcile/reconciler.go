package reconcile

import (
	"context"
	ot "github.com/opentracing/opentracing-go"
	otlog "github.com/opentracing/opentracing-go/log"
	otbridge "go.opentelemetry.io/otel/bridge/opentracing"

	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/pkg/v3/resource/deploy"
	"github.com/pulumi/pulumi/pkg/v3/util/cancel"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag/colors"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
	"github.com/pulumi/pulumi/sdk/v3/go/common/util/result"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	ctrldeploy "github.com/streamnative/pulumi-controller/sample/pkg/pulumi/deploy"
	apitrace "go.opentelemetry.io/otel/trace"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)
import "sigs.k8s.io/controller-runtime/pkg/client"

const (
	RuntimeEmbedded = "embedded"
)

type PulumiContext struct {
	Object client.Object
}

type PulumiReconciler struct {
	apitrace.Tracer
	PulumiTracer *otbridge.BridgeTracer
	client.Client
	FinalizerName string
	ControllerName string

	// Pulumi options
	RuntimeOptions map[string]interface{}  // Pulumi runtime options (see Pulumi.yaml)
	Decrypter      config.Decrypter  // secrets decryptor
	Snapshotter    SnapshotManager
	BackendClient  deploy.BackendClient // for working with stack references
	//Options        engine.UpdateOptions
}

type ConfigMap config.Map

type ReconcileOptions struct {
	StackConfig    config.Map
}

type Stack interface {
	GetObject() client.Object
	Generate(ctx *pulumi.Context) error
	UpdateStatus(ctx context.Context, changes StackChanges) error
}

func (r *PulumiReconciler) ReconcileStack(ctx context.Context, stack Stack, opts ReconcileOptions) (reconcileResult reconcile.Result, err error) {
	log := log.FromContext(ctx)
	ctx = r.PulumiTracer.ContextWithBridgeSpan(ctx, apitrace.SpanFromContext(ctx))
	//otSpan, ctx := ot.StartSpanFromContextWithTracer(ctx, r.PulumiTracer, "ReconcileStack")
	//defer otSpan.Finish()

	project := r.getProject()
	obj := stack.GetObject()

	if obj.GetDeletionTimestamp().IsZero() {
		if !controllerutil.ContainsFinalizer(obj, r.FinalizerName) {
			controllerutil.AddFinalizer(obj, r.FinalizerName)
			if err := r.Update(ctx, obj); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Load snapshot of object state
	snapshotHandle, err := func() (handle SnapshotHandle, err error) {
		span, ctx := ot.StartSpanFromContextWithTracer(ctx, r.PulumiTracer, "GetSnapshot")
		defer span.Finish()
		handle, err = r.Snapshotter.GetSnapshot(ctx, obj)
		if err != nil {
			span.LogFields(otlog.Error(err))
		}
		return
	}()
	if err != nil {
		return reconcile.Result{}, errors.Wrapf(err, "reading snapshot data for %q", obj)
	}

	// Perform snapshot integrity check
	if snapshotHandle != nil {
		// avoid going backwards in terms of goal state, i.e.
		// maintain the invariant that the goal generation is >= state generation
		if snapshotHandle.GetObjectGeneration() > obj.GetGeneration() {
			// the object is stale; requeue.
			reconcileResult.Requeue = true
			return
		}
	}

	// Obtain a plugin host
	hostConfig := ctrldeploy.NewHostConfig(opts.StackConfig, r.Decrypter)
	pluginContext, err := r.createPluginContext(ctx, stack, &hostConfig)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "creating Pulumi plugin context")
	}
	defer pluginContext.Close()

	// Define a function to run the appropriate operation
	run := func(dryRun bool) (snap *deploy.Snapshot, changes engine.ResourceChanges, result result.Result) {
		var runSpan ot.Span
		var runCtx context.Context
		if dryRun {
			runSpan, runCtx = ot.StartSpanFromContextWithTracer(ctx, r.PulumiTracer, "Preview")
		} else {
			runSpan, runCtx = ot.StartSpanFromContextWithTracer(ctx, r.PulumiTracer, "Update")
		}
		defer func(){
			fields := logResourceChanges(changes)
			if result != nil && result.IsBail() {
				fields = append(fields, otlog.Bool("pulumi.is_bail", result.IsBail()))
			}
			if result != nil && !result.IsBail() {
				fields = append(fields, otlog.Error(result.Error()))
			}
			runSpan.LogFields(fields...)
			runSpan.Finish()
		}()

		t := r.getTarget(stack, opts.StackConfig, snapshotHandle)

		if obj.GetDeletionTimestamp().IsZero() {
			// update the Pulumi stack
			return r.runOp(runCtx, project, stack, t, engine.Update, engine.UpdateOptions{Host: pluginContext.Host}, dryRun)
		} else {
			// destroy the Pulumi stack
			return r.runOp(runCtx, project, stack, t, engine.Destroy, engine.UpdateOptions{Host: pluginContext.Host}, dryRun)
		}
	}

	// Preview for status update
	_, previewSummary, previewRes := run(true)
	if previewRes != nil && previewRes.Error() != nil {
		// the preview failed due to an internal error, not due to a problem with applying the stack.
		return reconcile.Result{}, previewRes.Error()
	}
	if previewRes != nil && previewRes.IsBail() {
		// the preview failed due to a problem with applying the stack.
		log.Info("Pulumi preview bailed; will retry")
		return reconcile.Result{Requeue: true}, nil
	}

	if !previewSummary.HasChanges() {
		log.Info("Pulumi up/destroy skipped (no changes)")
	} else {
		// Run the deployment operation
		updateSnap, updateSummary, updateRes := run(false)
		if updateRes != nil && updateRes.Error() != nil {
			// the update failed due to an internal error, not due to a problem with applying the stack.
			return reconcile.Result{}, updateRes.Error()
		}

		// Save the snapshot
		err = func() (err error) {
			span, ctx := ot.StartSpanFromContextWithTracer(ctx, r.PulumiTracer, "SetSnapshot")
			defer span.Finish()
			err = r.Snapshotter.SetSnapshot(ctx, obj, updateSnap, snapshotHandle)
			if err != nil {
				span.LogFields(otlog.Error(err))
			}
			return
		}()
		if err != nil {
			// unable to persist the snapshot; fixme.
			return reconcile.Result{}, errors.Wrapf(err, "writing snapshot data for %q", obj)
		}

		if updateRes != nil && updateRes.IsBail() {
			// the update failed due to a problem with applying the stack.  Some progress might have occurred.
			log.Info("Pulumi up/destroy bailed; will retry")
			return reconcile.Result{Requeue: true}, nil
		}

		log.Info("Pulumi up/destroy succeeded", "summary", updateSummary)

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

func (r *PulumiReconciler) getProject() workspace.Project {
	return workspace.Project{
		Name:    tokens.PackageName(r.ControllerName),
		Runtime: workspace.NewProjectRuntimeInfo(RuntimeEmbedded, r.RuntimeOptions),
	}
}

// GetTarget Make a deployment target for the given reconciliation target.
func (r *PulumiReconciler) getTarget(stack Stack, cfg config.Map, snapshotHandle SnapshotHandle) deploy.Target {
	if cfg == nil {
		cfg = config.Map{}
	}
	var snap *deploy.Snapshot
	if snapshotHandle != nil {
		snap = snapshotHandle.GetSnapshot()
	}
	return deploy.Target{
		Name:      StackName(stack.GetObject()),
		Config:    cfg,
		Decrypter: r.Decrypter,
		Snapshot:  snap,
	}
}

func (r *PulumiReconciler) createPluginContext(ctx context.Context, stack Stack, cfg plugin.ConfigSource) (*plugin.Context, error) {

	loaders := []*ctrldeploy.ProviderLoader{
		// install inproc providers here
	}

	// Create an embedded language runtime which calls the supplied program function
	program := ctrldeploy.NewLanguageRuntime(func(info plugin.RunInfo, monitor *ctrldeploy.ResourceMonitor) error {
		// Convert the program configuration to the format used by the Pulumi Go SDK
		cfg := make(map[string]string, len(info.Config))
		for k, v := range info.Config {
			cfg[k.String()] = v
		}
		cfgSecretKeys := make([]string, len(info.ConfigSecretKeys))
		for i, k := range info.ConfigSecretKeys {
			cfgSecretKeys[i] = k.String()
		}


		// Run the supplied program using the Pulumi Go SDK

		err := func() (err error) {
			span, ctx := ot.StartSpanFromContextWithTracer(ctx, r.PulumiTracer, "Generate")
			defer span.Finish()

			engineCtx, err := pulumi.NewContext(ctx, pulumi.RunInfo{
				Project:          info.Project,
				Stack:            info.Stack,
				Config:           cfg,
				ConfigSecretKeys: cfgSecretKeys,
				Parallel:         info.Parallel,
				DryRun:           info.DryRun,
				MonitorAddr:      info.MonitorAddress,
			})
			if err != nil {
				return errors.Wrapf(err, "creating Pulumi SDK context")
			}
			defer engineCtx.Close()
			err = pulumi.RunWithContext(engineCtx, stack.Generate)
			if err != nil {
				span.LogFields(otlog.Error(err))
			}
			return
		}()

		return err
	})

	// Create a plugin host
	sink := diag.DefaultSink(os.Stdout, os.Stderr, diag.FormatOptions{
		Color: colors.Never,
	})
	host := ctrldeploy.NewPluginHost(sink, sink, program, loaders...)

	// Create a plugin context for the plugins loaded by the host
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	tracingSpan, _ := ot.StartSpanFromContextWithTracer(ctx, r.PulumiTracer, "PluginContext")
	pluginCtx, err := plugin.NewContext(sink, sink, host, cfg, cwd, nil, false, tracingSpan)
	if err != nil {
		_ = host.Close()
		return nil, errors.Wrapf(err, "creating Pulumi plugin context")
	}
	host.SetPluginContext(pluginCtx)

	return pluginCtx, nil
}

type engineOp func(engine.UpdateInfo, *engine.Context, engine.UpdateOptions, bool) (engine.ResourceChanges, result.Result)

func (r *PulumiReconciler) runOp(
	callerCtx context.Context, project workspace.Project, stack Stack,
	target deploy.Target, op engineOp, opts engine.UpdateOptions, dryRun bool) (*deploy.Snapshot, engine.ResourceChanges, result.Result) {

	tracingSpan := ot.SpanFromContext(callerCtx)
	log := log.FromContext(callerCtx)

	// Create an appropriate update info and context.
	info := &updateInfo{project: project, target: target}

	// Create a cancellable context
	cancelCtx, cancelSrc := cancel.NewContext(context.Background())
	done := make(chan bool)
	defer close(done)
	go func() {
		select {
		case <-callerCtx.Done():
			cancelSrc.Cancel()
		case <-done:
		}
	}()

	// Initialize the Pulumi engine
	events := make(chan engine.Event)  // detailed progress events
	journal := engine.NewJournal()
	engineCtx := &engine.Context{
		Cancel:          cancelCtx,
		Events:          events,
		SnapshotManager: journal,
		BackendClient:   r.BackendClient,
		ParentSpan:      tracingSpan.Context(),
	}

	// Begin draining events.
	stb := builder{}
	stb.ApplyPlanning(dryRun)
	go func() {
		for e := range events {
			err := stb.ApplyEngineEvent(callerCtx, e)
			if err != nil {
				log.Error(err, "ApplyEngineEvent")
				// TODO
			}
		}
	}()

	// Run the operation.
	changes, res := op(info, engineCtx, opts, dryRun /*dry-run */)

	// Store the snapshot of the resultant state
	contract.IgnoreClose(journal)
	snap := journal.Snap(target.Snapshot)
	if res == nil && snap != nil {
		res = result.WrapIfNonNil(snap.VerifyIntegrity())
	}

	err := func() (err error) {
		span, ctx := ot.StartSpanFromContextWithTracer(callerCtx, r.PulumiTracer, "UpdateStatus")
		defer span.Finish()
		err = stack.UpdateStatus(ctx, stb.Build())
		if err != nil {
			span.LogFields(otlog.Error(err))
		}
		return
	}()
	if err != nil {
		log.Error(err, "UpdateStatus")
		// fixme: if UpdateStatus fails, we need to recover somehow,
		// because a subsequent reconcile might not call UpdateStatus again (skipped due to no-change)
	}

	return snap, changes, res
}



type updateInfo struct {
	project workspace.Project
	target  deploy.Target
}

func (u *updateInfo) GetRoot() string {
	return ""
}

func (u *updateInfo) GetProject() *workspace.Project {
	return &u.project
}

func (u *updateInfo) GetTarget() *deploy.Target {
	return &u.target
}

func logResourceChanges(changes engine.ResourceChanges) []otlog.Field {
	return []otlog.Field{
		otlog.Bool("pulumi.has_changes", changes.HasChanges()),
	}
}
