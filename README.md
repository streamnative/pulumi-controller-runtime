_This project is in an alpha stage, not suitable for production usage.  Expect breaking changes._

# Purpose

Contollers/Operators frequently follow this pattern:

1. They have an API extension (ex. CRD) that declaratively describes what one would like deployed
2. The controller/operator watches for creations and updates (ex. of the CRD)
3. A kubernetes object (ex CR) is created or updated
4. The operator responds to the change and works forward to get the cluster to the neccessary final state (ex creating a statefulset,
updating a service account, ectera)

The last step is where the substance of many operators reside; however, the code is often boiletplate and boring.
Gather the state of the cluster, figure out the difference between what is needed and what exists on the cluster,
and do the neccessary creates, updates, and deletes. Often sequentially and frequently checking for errors.

**Our CRDs give us a declarative way to specify what we want to deploy but we find ourselves writing controllers imperatively.**
Our utility in this component is to show how to use the Pulumi Go SDK to write declarative code, eliminating the often common imperative parts
of our controllers/operators.

# Getting Started
This library is designed to work with any Kubernetes controller that is based on
the [kubernetes-sigs/controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) library.
Use the [Kubebuilder](https://book.kubebuilder.io/) tool to scaffold an ordinary Kubernetes controller.

See the sample controller in `sample/`.

## Implement a Custom Resource
You'll use this library to implement a reconciler for your custom resource.   A reconciler typically provisions
resources based on a resource specification, and that specification will be defined using the Pulumi Go SDK, as a 
resource graph built within the reconciliation loop.  The other task of a reconciler is to maintain a status block 
on the resource object.  A status block primarily consists of conditions that reflect the object's current state
with respect to its specification.  Your reconciliation loop uses Pulumi stack state to update the status information.

### Reconciler Struct
A typical reconciler is implemented as a `struct` that implements the `Reconciler` interface.  To use Pulumi
within your reocnciler, embed the `pulumireconcile.PulumiReconciler` type.

### Controller Setup
During the setup of your controller, configure the `PulumiReconciler`.  For example:
```go
func (r *IamAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Build a Pulumi-based reconciler for IamAccount, with resources defined by the MakeResources function
	r.PulumiReconciler, _ := pulumireconcile.NewReconcilerManagedBy(mgr).
		For(&samplev1.IamAccount{}).
		WithProgram(r.MakeResources).
		WithOptions(pulumireconcile.FinalizerName(FinalizerName)).
		Build()

	return ctrl.NewControllerManagedBy(mgr).
		For(&samplev1.IamAccount{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
```

### Reconcile
In the `Reconcile` function, get the custom object as normal.  Use the object's metadata and specification to
set configuration values for the object's Pulumi stack configuration.  It is also possible to generate the resource graph
based on the object; the library provides the whole object as a configuration value named `obj`.

Invoke the `ReconcileObject` function on your reconciler.  The library performs the following tasks:
1. Calls the program function (provided during setup and described below) to generate a resource graph.
2. Fetches the current stack state, and makes a callback to your reconciler to update the status block of the custom object.
3. Applies changes to the resource.
4. If any changes were made, updates the status again.

The library implements object finalization automatically.  When the custom object is marked for deletion, the library
destroys the current resources.

_This functionality may change based on feedback.  One thought is to not handle finalization automatically, in favor of
having the reconciler call `Up` or `Destroy` instead of `ReconcileObject`.  This would be to improve
flexibility._

### Resource Graph
Your reconciler uses the Pulumi Go SDK to define a resource graph for the custom object.  The library
provides your reconciler with a context object for this purpose.

For example, here's an implementation of `ReconcileObject` that simply creates a Kubernetes Service Account (KSA)
using the [Pulumi Kubernetes Provider](https://www.pulumi.com/registry/packages/kubernetes/#pulumi-kubernetes-provider).

```go
func (r *IamAccountReconciler) MakeResources(ctx *pulumi.Context) error {
    // obtain the object being reconciled as a configuration parameter.
    conf := pulumiconfig.New(ctx, "")
    obj := samplev1.IamAccount{}
    conf.RequireObject("obj", &obj)
    
    // make a KSA for the IamAccount object
    ksa, err := pulumicorev1.NewServiceAccount(ctx, "iamaccount", &pulumicorev1.ServiceAccountArgs{
        Metadata: &pulumimetav1.ObjectMetaArgs{
            Name:        pulumi.StringPtr(makeServiceAccountId(&obj)),
            Namespace:   pulumi.StringPtr(obj.Namespace),
        },
    })
    if err != nil {
        return err
    }
    ctx.Export("ksa", ksa.Metadata.Name())
    return nil
}
```

Use configuration values, such as the custom object itself or selected values from it, to parameterize the resource graph.

Use stack outputs to expose output values to your reconciler.

_Future: add support for stack references to other objects._

### Status Update
Before making changes, Pulumi generates a plan that reflects the planned additions, deletions, and other modifications to
the object's resources.  The plan may be used to update the status of the custom object itself.  For example, imagine that
the custom object's resource graph consists of a Kubernetes `Deployment` object.  The `Ready` condition of the object
should reflect the readiness of the deployment.  The plan contains enough information for your reconciler to report on
current conditions.

An important aspect to keep in mind is the mutability of your custom object's specification.  As a specification changes
over time, Kubernetes automatically increments the `metadata.generation` field.  Your object's status block should
contain an `observedGeneration` field that is managed by your reconciler.  When you update the status with the Pulumi plan in hand,
set the `observedGeneration` to the `generation` that the plan is based on.  Keep in mind that a status condition reflects
current conditions _with respect to that generation_.

Use admission control webhooks to impose constraints on mutability as desired.

### Stack State
Each custom resource object that undergoes reconciliation has an independent resource graph and associated stack that is named
after the object.  The stack configuration is not stored as a file (e.g. `Pulumi.foo.yaml`, but is set by the reconciler
during the reconciliation loop.

The library uses Kubernetes secrets as a state backend, one secret per custom resource object.  

_In the future, tools will be developed to import and export the stack state for operational purposes._

