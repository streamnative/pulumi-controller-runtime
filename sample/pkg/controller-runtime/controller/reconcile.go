package controller

import (
	"context"
	"go.opentelemetry.io/otel"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apitrace "go.opentelemetry.io/otel/trace"

	. "github.com/streamnative/kube-instrumentation/semconv"
)

const (
	// requestKey is the context key for the reconciliation request data
	requestKey         = iota
	instumentationName = "github.com/streamnative/pulumi-controller/pkg/controller"
)

type Tracer struct {
	apitrace.Tracer
	Name string
	Kind string
}

type TracerOption func(*Tracer)

// NewTracer makes a traced reconciler.
func NewTracer(controllerName string, options ...TracerOption) Tracer {
	t := Tracer{
		Tracer: otel.Tracer(instumentationName),
		Name:   controllerName,
	}
	for _, option := range options {
		option(&t)
	}
	return t
}

func WithTraceProvider(provider apitrace.TracerProvider) TracerOption {
	return func(t *Tracer) {
		t.Tracer = provider.Tracer(instumentationName)
	}
}

func WithKind(kind string) TracerOption {
	return func(t *Tracer) {
		t.Kind = kind
	}
}

func (t *Tracer) NewReconcileContext(ctx context.Context, request reconcile.Request) (context.Context, apitrace.Span) {
	ctx = WithRequest(ctx, request)
	c, s := t.Start(ctx, t.Kind+".Reconcile", apitrace.WithSpanKind(apitrace.SpanKindConsumer))
	s.SetAttributes(
		KubeControllerNameKey.String(t.Name),
		KubeObjectNamespaceKey.String(request.Namespace),
		KubeObjectNameKey.String(request.Name))
	return c, s
}

// WithRequest returns a copy of parent in which the request value is set
func WithRequest(parent context.Context, request reconcile.Request) context.Context {
	return context.WithValue(parent, requestKey, request)
}

// RequestFrom returns the value of the request key on the ctx
func RequestFrom(ctx context.Context) (reconcile.Request, bool) {
	r, ok := ctx.Value(requestKey).(reconcile.Request)
	return r, ok
}

func (t *Tracer) EndReconcile(ctx context.Context, result *reconcile.Result, err *error) {
	span := apitrace.SpanFromContext(ctx)
	if *err != nil {
		span.RecordError(*err)
	} else {
		span.AddEvent("result", apitrace.WithAttributes(ReconcileAttributesFromResult(*result)...))
	}
	span.End()
}
