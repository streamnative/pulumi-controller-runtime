module github.com/streamnative/pulumi-controller/sample

go 1.16

require (
	github.com/blang/semver v3.5.1+incompatible
	github.com/gofrs/uuid v3.3.0+incompatible
	github.com/golang/protobuf v1.5.2
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/opentracing/opentracing-go v1.1.0
	github.com/pkg/errors v0.9.1
	github.com/pulumi/pulumi-google-native/sdk v0.3.0
	github.com/pulumi/pulumi-kubernetes/sdk/v3 v3.4.0
	github.com/pulumi/pulumi/pkg/v3 v3.4.0
	github.com/pulumi/pulumi/sdk/v3 v3.4.0
	github.com/streamnative/kube-instrumentation v0.1.2
	go.opentelemetry.io/otel v0.19.0
	go.opentelemetry.io/otel/trace v0.19.0
	google.golang.org/grpc v1.37.0
	k8s.io/api v0.20.2
	k8s.io/apimachinery v0.20.2
	k8s.io/client-go v0.20.2
	sigs.k8s.io/controller-runtime v0.8.3
)
