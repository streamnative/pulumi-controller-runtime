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

package controllers

import (
	"context"
	"fmt"
	"hash/crc32"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	pulumiconfig "github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	pulumireconcile "github.com/streamnative/pulumi-controller-runtime/sample/pkg/reconciler-runtime/reconcile"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	pulumigoogleiamv1 "github.com/pulumi/pulumi-google-native/sdk/go/google/iam/v1"
	pulumicorev1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/core/v1"
	pulumimetav1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/meta/v1"

	samplev1 "github.com/streamnative/pulumi-controller-runtime/sample/api/v1"
)

const (
	FinalizerName                 = "iamaccount-controller.pulumi-controller.example.com"
	WorkloadIdentityKsaAnnotation = "iam.gke.io/gcp-service-account"
)

// IamAccountReconciler reconciles a IamAccount object
type IamAccountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	pulumireconcile.PulumiReconciler
}

//+kubebuilder:rbac:groups=pulumi-controller.example.com,resources=iamaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pulumi-controller.example.com,resources=iamaccounts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pulumi-controller.example.com,resources=iamaccounts/finalizers,verbs=update

func (r *IamAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	var iamAccount samplev1.IamAccount
	if err := r.Get(ctx, req.NamespacedName, &iamAccount); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	return r.ReconcileObject(ctx, &iamAccount, pulumireconcile.ReconcileOptions{
		// prepare a stack configuration for the default resource providers
		StackConfig: config.Map{
			config.MustMakeKey("google-native", "project"): config.NewValue(os.Getenv("GCP_PROJECT")),
		},
		// Update the status block based on the current state of the Pulumi stack
		UpdateStatus: r.UpdateStatus,
	})
}

func (r *IamAccountReconciler) MakeResources(ctx *pulumi.Context) error {
	// obtain the object being  reconciled as a configuration parameter.
	conf := pulumiconfig.New(ctx, "")
	obj := samplev1.IamAccount{}
	conf.RequireObject("obj", &obj)

	var annotations pulumi.StringMap
	switch obj.Spec.Type {
	case samplev1.IamAccountTypeGoogle:
		// create a GSA for the IamAccount
		gsa, err := pulumigoogleiamv1.NewServiceAccount(ctx, "iamaccount", &pulumigoogleiamv1.ServiceAccountArgs{
			Project:     pulumi.String(obj.Spec.Google.Project),
			AccountId:   pulumi.StringPtr(makeServiceAccountId(&obj)),
			DisplayName: pulumi.StringPtr(fmt.Sprintf("IamAccount/%s/%s", obj.Namespace, obj.Name)),
			Description: pulumi.StringPtr("Enables resource access for SN Cloud"),
		})
		if err != nil {
			return err
		}
		ctx.Export("gsa", gsa.Email)

		annotations = pulumi.StringMap(map[string]pulumi.StringInput{
			WorkloadIdentityKsaAnnotation: gsa.Email,
		})
	}

	// create a KSA for the IamAccount
	ksa, err := pulumicorev1.NewServiceAccount(ctx, "iamaccount", &pulumicorev1.ServiceAccountArgs{
		Metadata: &pulumimetav1.ObjectMetaArgs{
			Name:        pulumi.StringPtr(makeServiceAccountId(&obj)),
			Namespace:   pulumi.StringPtr(obj.Namespace),
			Annotations: annotations,
		},
	})
	if err != nil {
		return err
	}
	ctx.Export("ksa", ksa.Metadata.Name())

	return nil
}

func makeServiceAccountId(sa *samplev1.IamAccount) string {
	// "Service account ID must be between 6 and 30 characters."
	// "Service account ID must start with a lower case letter,
	//  followed by one or more lower case alphanumerical characters that can be separated by hyphens."
	return fmt.Sprintf("iamaccount-%s", hash(string(sa.UID)))
}

func hash(s string) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(s)))
}

func (r *IamAccountReconciler) UpdateStatus(ctx context.Context, o client.Object, chg pulumireconcile.StackChanges) error {
	log := log.FromContext(ctx)
	obj := o.(*samplev1.IamAccount)

	if chg.Planning {
		if chg.HasResourceChanges {
			meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
				Type:   samplev1.ConditionReady,
				Reason: "PendingChanges",
				Status: metav1.ConditionFalse,
			})
		} else {
			meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
				Type:   samplev1.ConditionReady,
				Reason: "OK",
				Status: metav1.ConditionTrue,
			})
		}
	} else {
		obj.Status.KSA = ""
		obj.Status.GSA = ""
		switch obj.Spec.Type {
		case samplev1.IamAccountTypeGoogle:
			if chg.Outputs["gsa"].IsString() {
				obj.Status.GSA = chg.Outputs["gsa"].StringValue()
			}
		}
		if chg.Outputs["ksa"].IsString() {
			obj.Status.KSA = chg.Outputs["ksa"].StringValue()
		}
	}

	obj.Status.ObservedGeneration = obj.Generation
	err := r.Status().Update(ctx, obj)
	if err != nil {
		log.Error(err, "failed to update status", "name", obj.Name)
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *IamAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// Build a Pulumi-based reconciler for IamAccounts, with resources defined by the MakeResources function
	pr, err := pulumireconcile.NewReconcilerManagedBy(mgr).
		For(&samplev1.IamAccount{}).
		WithProgram(r.MakeResources).
		WithOptions(pulumireconcile.FinalizerName(FinalizerName)).
		Build()
	if err != nil {
		return err
	}
	r.PulumiReconciler = pr

	return ctrl.NewControllerManagedBy(mgr).
		For(&samplev1.IamAccount{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
