
## Usage

### Install the Custom Resource Definitions
The sample controller implements custom resources using a Custom Resource Definition (CRD).  Install the sample CRD 
into your Kubernetes cluster before starting the controller.
```shell
kubectl apply -f config/crd/bases/
```

### Start the Manager
_Be sure to set your kubectl context to the desired cluster before running the manager._

Use the provided Run Configuration ("go run sample") to run the manager in `sample/main.go`.

### Apply a Sample Object
See the sample object in `sample/config/samples/pulumi-controller.example.com_v1_iamaccount.yaml`.

Observe that a KSA is created and that the object's condition turns to `Ready`.
