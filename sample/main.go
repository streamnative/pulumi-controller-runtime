/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"github.com/pulumi/pulumi/pkg/v3/secrets/b64"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/streamnative/pulumi-controller/sample/pkg/pulumi/deploy"
	"github.com/streamnative/pulumi-controller/sample/pkg/pulumi/reconcile"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	otbridge "go.opentelemetry.io/otel/bridge/opentracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"k8s.io/client-go/transport"
	"net/http"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	texporter "github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace"
	otelcontroller "github.com/streamnative/pulumi-controller/sample/pkg/controller-runtime/controller"

	pulumicontrollerexamplecomv1 "github.com/streamnative/pulumi-controller/sample/api/v1"
	"github.com/streamnative/pulumi-controller/sample/controllers"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(pulumicontrollerexamplecomv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	ctx := context.Background()

	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	projectID := os.Getenv("GCP_PROJECT")
	exporter, err := texporter.NewExporter(
		texporter.WithProjectID(projectID),
		texporter.WithOnError(func(err error) {
			setupLog.Error(err, "unable to export to Google Trace")
		}))
	if err != nil {
		setupLog.Error(err, "unable to start trace exporter")
		os.Exit(1)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBlocking(), sdktrace.WithMaxExportBatchSize(1)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		)
	defer tp.ForceFlush(ctx) // flushes any pending spans
	otel.SetTracerProvider(tp)

	cfg := ctrl.GetConfigOrDie()
	cfg.Wrap(NewTransport())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "6932d832.example.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	tracer := otelcontroller.NewTracer("iamaccount-controller", otelcontroller.WithKind("IamAccount"))
	bridgeTracer, _ := otbridge.NewTracerPair(tracer)
	bridgeTracer.SetWarningHandler(func(msg string) {
		setupLog.Info("warning from OpenTracing bridge: " + msg)
	})
	if err = (&controllers.IamAccountReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		PulumiReconciler: reconcile.PulumiReconciler{
			Client:         mgr.GetClient(),
			FinalizerName:  "iamaccount-controller.pulumi-controller.example.com",
			ControllerName: "iamaccount",
			Decrypter:      config.NopDecrypter,
			Snapshotter:    reconcile.NewSecretSnapshotter(mgr.GetClient(), b64.NewBase64SecretsManager()),
			BackendClient:  &deploy.BackendClient{},
			Tracer:         tracer,
			PulumiTracer:   bridgeTracer,
		},
		Tracer: tracer,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "IamAccount")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func NewTransport() transport.WrapperFunc {
	return func(rt http.RoundTripper) http.RoundTripper {
		return otelhttp.NewTransport(rt, otelhttp.WithTracerProvider(otel.GetTracerProvider()))
	}
}
