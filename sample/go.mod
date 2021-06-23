module github.com/streamnative/pulumi-controller/sample

go 1.16

require (
	cloud.google.com/go v0.84.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace v0.20.1
	github.com/blang/semver v3.5.1+incompatible
	github.com/gofrs/uuid v3.3.0+incompatible
	github.com/golang/protobuf v1.5.2
	github.com/mitchellh/copystructure v1.0.0
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/opentracing/opentracing-go v1.2.0
	github.com/pkg/errors v0.9.1
	github.com/pulumi/pulumi-google-native/sdk v0.3.0
	github.com/pulumi/pulumi-kubernetes/sdk/v3 v3.4.0
	github.com/pulumi/pulumi/pkg/v3 v3.4.0
	github.com/pulumi/pulumi/sdk/v3 v3.4.0
	github.com/streamnative/kube-instrumentation v0.1.2
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.20.0
	go.opentelemetry.io/otel v1.0.0-RC1
	go.opentelemetry.io/otel/bridge/opentracing v0.21.0
	go.opentelemetry.io/otel/internal/metric v0.21.0 // indirect
	go.opentelemetry.io/otel/sdk v0.20.0
	go.opentelemetry.io/otel/trace v1.0.0-RC1
	golang.org/x/net v0.0.0-20210614182718-04defd469f4e // indirect
	golang.org/x/oauth2 v0.0.0-20210622190553-bce0382f6c22 // indirect
	golang.org/x/sys v0.0.0-20210616094352-59db8d763f22 // indirect
	google.golang.org/genproto v0.0.0-20210617175327-b9e0b3197ced // indirect
	google.golang.org/grpc v1.38.0
	k8s.io/api v0.20.2
	k8s.io/apimachinery v0.20.2
	k8s.io/client-go v0.20.2
	sigs.k8s.io/controller-runtime v0.8.3
)

replace (
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace => github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/trace v0.20.1
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp => go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.20.0
	go.opentelemetry.io/otel => go.opentelemetry.io/otel v0.20.0
	go.opentelemetry.io/otel/bridge/opentracing => go.opentelemetry.io/otel/bridge/opentracing v0.20.0
	go.opentelemetry.io/otel/metric => go.opentelemetry.io/otel/metric v0.20.0
	go.opentelemetry.io/otel/sdk => go.opentelemetry.io/otel/sdk v0.20.0
	go.opentelemetry.io/otel/trace => go.opentelemetry.io/otel/trace v0.20.0

)
