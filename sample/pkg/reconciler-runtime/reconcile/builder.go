// Copyright (c) 2020 StreamNative, Inc.. All Rights Reserved.

package reconcile

import (
	"context"
	"os"
	"strings"

	ot "github.com/opentracing/opentracing-go"
	"github.com/pkg/errors"
	"github.com/pulumi/pulumi/pkg/v3/secrets/b64"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag"
	"github.com/pulumi/pulumi/sdk/v3/go/common/diag/colors"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	snbackend "github.com/streamnative/pulumi-controller-runtime/sample/pkg/reconciler-runtime/backend"
	snplugin "github.com/streamnative/pulumi-controller-runtime/sample/pkg/reconciler-runtime/plugin"
	apitrace "go.opentelemetry.io/otel/trace"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type reconcilerBuilder struct {
	mgr          manager.Manager
	forInput     ForInput
	programInput ProgramInput
	optsInput    OptsInput
}

// ForInput represents the information set by For method.
type ForInput struct {
	object client.Object
	err    error
}

type ProgramInput struct {
	program        pulumi.RunFunc
	runtimeOptions map[string]interface{}
}

// ProgramOption is some configuration that modifies options for a program.
type ProgramOption func(*ProgramInput)

type OptsInput struct {
	finalizerName string
	tracer        apitrace.Tracer
}

type Option func(input *OptsInput)

// NewReconcilerManagedBy builds a Pulumi reconciler based on the given manager.
func NewReconcilerManagedBy(mgr manager.Manager) *reconcilerBuilder {
	return &reconcilerBuilder{mgr: mgr}
}

// For sets the object type.
func (b *reconcilerBuilder) For(object client.Object) *reconcilerBuilder {
	b.forInput = ForInput{
		object: object,
	}
	return b
}

// WithProgram sets the Pulumi program information.
func (b *reconcilerBuilder) WithProgram(program pulumi.RunFunc, opts ...ProgramOption) *reconcilerBuilder {
	b.programInput = ProgramInput{program: program}
	for _, opt := range opts {
		opt(&b.programInput)
	}
	return b
}

// RuntimeOptions the runtime options for the program.
func RuntimeOptions(runtimeOptions map[string]interface{}) ProgramOption {
	return func(i *ProgramInput) {
		i.runtimeOptions = runtimeOptions
	}
}

// WithOptions sets general reconciler options.
func (b *reconcilerBuilder) WithOptions(opts ...Option) *reconcilerBuilder {
	for _, opt := range opts {
		opt(&b.optsInput)
	}
	return b
}

// FinalizerName the finalizer name to use.
func FinalizerName(name string) Option {
	return func(i *OptsInput) {
		i.finalizerName = name
	}
}

func Tracer(tracer apitrace.Tracer) Option {
	return func(i *OptsInput) {
		i.tracer = tracer
	}
}

func (b *reconcilerBuilder) Build() (PulumiReconciler, error) {
	r := PulumiReconciler{
		Tracer:        b.optsInput.tracer,
		Client:        b.mgr.GetClient(),
		FinalizerName: b.optsInput.finalizerName,
	}

	proj, err := b.createProject()
	if err != nil {
		return PulumiReconciler{}, err
	}
	r.proj = proj

	backend, err := b.createBackend(proj)
	if err != nil {
		return PulumiReconciler{}, err
	}
	r.backend = backend

	r.createPluginContext = func(ctx context.Context) (*plugin.Context, error) {
		return b.createPluginHost(ctx, proj, b.programInput.program)
	}

	return r, nil
}

func makeProjectName(gvk schema.GroupVersionKind) tokens.PackageName {
	return tokens.PackageName(strings.ToLower(gvk.Kind) + "." + strings.ToLower(gvk.Group))
}

func (b *reconcilerBuilder) createProject() (*workspace.Project, error) {
	// Retrieve the GVK from the object we're reconciling
	// to generate an appropriate project name.
	gvk, err := apiutil.GVKForObject(b.forInput.object, b.mgr.GetScheme())
	if err != nil {
		return nil, err
	}
	return &workspace.Project{
		Name:    tokens.PackageName(makeProjectName(gvk)),
		Runtime: workspace.NewProjectRuntimeInfo(RuntimeEmbedded, b.programInput.runtimeOptions),
	}, nil
}

func (b *reconcilerBuilder) createBackend(proj *workspace.Project) (snbackend.Backend, error) {
	secretsManager := b64.NewBase64SecretsManager()
	backend := snbackend.NewDefaultBackend(
		snbackend.NewSecretSnapshotter(b.mgr.GetClient(), secretsManager),
		secretsManager,
		proj)
	return backend, nil
}

type CreatePluginContextFunc func(ctx context.Context) (*plugin.Context, error)

// createPluginHost creates a plugin host for engine operations.
// ctx is a top-level context for the plugin interactions.
func (b *reconcilerBuilder) createPluginHost(ctx context.Context, proj *workspace.Project, program pulumi.RunFunc) (*plugin.Context, error) {

	// Start a tracing span for the lifespan of the plugin context.
	// This span is the parent for calls to plugins and for callbacks from plugin to host.
	// The span will finish when the plugin context is closed.
	tracingSpan, _ := ot.StartSpanFromContext(ctx, "pulumi-plugin")

	// Create an embedded Pulumi language runtime to execute the given Pulumi Go SDK program function.
	// The supplied context becomes the parent for program executions.
	runtime := snplugin.NewEmbeddedLanguageRuntime(ctx, program)

	// Create a plugin host as required by the Pulumi engine to obtain resource providers, language runtimes, analyzers, etc.
	sink := diag.DefaultSink(os.Stdout, os.Stderr, diag.FormatOptions{
		Color: colors.Never,
	})
	host, err := snplugin.NewHost(sink, sink, tracingSpan,
		snplugin.SimpleLanguageRuntimeLoader{RuntimeEmbedded: runtime},
		snplugin.NewPulumiProviderLoader(proj.Runtime.Options()))
	if err != nil {
		return nil, errors.Wrapf(err, "creating Pulumi plugin host")
	}

	// Create a plugin context for the plugins loaded by the host.
	// Note that an separate plugin context ('pulumi-plan') will be created by the Pulumi engine.
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	pluginCtx, err := plugin.NewContext(sink, sink, host, nil, cwd, nil, false, tracingSpan)
	if err != nil {
		_ = host.Close()
		return nil, errors.Wrapf(err, "creating Pulumi plugin context")
	}
	host.SetPluginContext(pluginCtx)

	// Return the context to be able to conveniently close the host and associated trace span
	return pluginCtx, nil
}
