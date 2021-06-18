package reconcile

import (
	"context"
	"github.com/opentracing/opentracing-go"
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
	UpdateStatus(ctx context.Context, event engine.Event) error
}

func (r *PulumiReconciler) ReconcileStack(ctx context.Context, stack Stack, opts ReconcileOptions) (reconcileResult reconcile.Result, err error) {
	log := log.FromContext(ctx)

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
	var snapshotHandle SnapshotHandle
	snapshotHandle, err = r.Snapshotter.GetSnapshot(ctx, obj)
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

	// Prepare a deployment target
	target := r.getTarget(stack, opts.StackConfig, snapshotHandle)

	// Obtain a plugin host
	host, err := r.createHost(ctx, stack, &target)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "creating Pulumi language host")
	}
	defer host.Close()

	// Run the deployment operation
	var res result.Result
	var snap *deploy.Snapshot
	if obj.GetDeletionTimestamp().IsZero() {
		// update the Pulumi stack
		snap, res = r.runOp(ctx, project, target, engine.Update, engine.UpdateOptions{Host: host})
	} else {
		// destroy the Pulumi stack
		snap, res = r.runOp(ctx, project, target, engine.Destroy, engine.UpdateOptions{Host: host})
	}
	if res != nil && res.Error() != nil {
		// the update failed due to an internal error, not due to a problem with applying the stack.
		return reconcile.Result{}, res.Error()
	}

	// Save the snapshot
	err = r.Snapshotter.SetSnapshot(ctx, obj, snap, snapshotHandle)
	if err != nil {
		// unable to persist the snapshot; fixme.
		return reconcile.Result{}, errors.Wrapf(err, "writing snapshot data for %q", obj)
	}

	if res != nil && res.IsBail() {
		// the update failed due to a problem with applying the stack.  Some progress might have occurred.
		log.Info("Pulumi up/destroy bailed; will retry")
		return reconcile.Result{Requeue: true}, nil
	}

	log.Info("Pulumi up/destroy succeeded")

	if !obj.GetDeletionTimestamp().IsZero() {
		if controllerutil.ContainsFinalizer(obj, r.FinalizerName) {
			controllerutil.RemoveFinalizer(obj, r.FinalizerName)
			if err := r.Update(ctx, obj); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	return reconcile.Result{}, nil
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

func (r *PulumiReconciler) createHost(ctx context.Context, stack Stack, target *deploy.Target) (plugin.Host, error) {

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
		ctx, err := pulumi.NewContext(context.Background(), pulumi.RunInfo{
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
		return pulumi.RunWithContext(ctx, stack.Generate)
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
	tracingSpan := opentracing.SpanFromContext(ctx)
	pluginCtx, err := plugin.NewContext(nil, nil, host, target, cwd, nil, false, tracingSpan)
	if err != nil {
		_ = host.Close()
		return nil, errors.Wrapf(err, "creating Pulumi plugin context")
	}
	host.SetPluginContext(pluginCtx)

	return host, nil
}

type engineOp func(engine.UpdateInfo, *engine.Context, engine.UpdateOptions, bool) (engine.ResourceChanges, result.Result)

func (r *PulumiReconciler) runOp(
	callerCtx context.Context, project workspace.Project,
	target deploy.Target, op engineOp, opts engine.UpdateOptions) (*deploy.Snapshot, result.Result) {

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
	ctx := &engine.Context{
		Cancel:          cancelCtx,
		Events:          events,
		SnapshotManager: journal,
		BackendClient:   r.BackendClient,
	}

	// Begin draining events.
	var firedEvents []engine.Event
	go func() {
		for e := range events {
			firedEvents = append(firedEvents, e)
			log.Info("engine event", "event", e)
		}
	}()

	// Run the operation.
	_, res := op(info, ctx, opts, false)

	// Store the snapshot of the resultant state
	contract.IgnoreClose(journal)
	snap := journal.Snap(target.Snapshot)
	if res == nil && snap != nil {
		res = result.WrapIfNonNil(snap.VerifyIntegrity())
	}

	return snap, res
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
