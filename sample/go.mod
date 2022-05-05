module github.com/streamnative/pulumi-controller-runtime/sample

go 1.16

require (
	github.com/onsi/ginkgo v1.14.1
	github.com/onsi/gomega v1.10.2
	github.com/pulumi/pulumi-google-native/sdk v0.3.0
	github.com/pulumi/pulumi-kubernetes/sdk/v3 v3.4.0
	github.com/pulumi/pulumi/sdk/v3 v3.4.0
	github.com/streamnative/pulumi-controller-runtime v0.0.0-00010101000000-000000000000
	k8s.io/apimachinery v0.20.2
	k8s.io/client-go v0.20.2
	sigs.k8s.io/controller-runtime v0.8.3
)

replace github.com/streamnative/pulumi-controller-runtime => ../
