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
	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	sdkconfig "github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
	"github.com/streamnative/pulumi-controller/sample/pkg/pulumi/reconcile"
	"hash/crc32"
	"k8s.io/apimachinery/pkg/runtime"
	"os"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	otelcontroller "github.com/streamnative/pulumi-controller/sample/pkg/controller-runtime/controller"

	pulumigoogleiamv1 "github.com/pulumi/pulumi-google-native/sdk/go/google/iam/v1"
	pulumicorev1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/core/v1"
	pulumimetav1 "github.com/pulumi/pulumi-kubernetes/sdk/v3/go/kubernetes/meta/v1"
	pulumicontrollerv1 "github.com/streamnative/pulumi-controller/sample/api/v1"
)

const (
	ControllerName                = "iamaccount-controller"
	FinalizerName                 = "iamaccount-controller.pulumi-controller.example.com"
	WorkloadIdentityKsaAnnotation = "iam.gke.io/gcp-service-account"
)

// IamAccountReconciler reconciles a IamAccount object
type IamAccountReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	reconcile.PulumiReconciler
	otelcontroller.Tracer
}

//+kubebuilder:rbac:groups=pulumi-controller.example.com,resources=iamaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pulumi-controller.example.com,resources=iamaccounts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pulumi-controller.example.com,resources=iamaccounts/finalizers,verbs=update

func (r *IamAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	ctx, _ = r.NewReconcileContext(ctx, req)
	defer r.EndReconcile(ctx, &result, &err)

	_ = log.FromContext(ctx)

	var iamAccount pulumicontrollerv1.IamAccount
	if err := r.Get(ctx, req.NamespacedName, &iamAccount); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	c := &IamAccountContext{
		Client: r.Client,
		Object: iamAccount,
	}
	return r.ReconcileStack(ctx, c, reconcile.ReconcileOptions{
		StackConfig: config.Map{
			config.MustMakeKey("google-native", "project"): config.NewValue(os.Getenv("GCP_PROJECT")),
		},
	})
}

type IamAccountContext struct {
	client.Client
	Object pulumicontrollerv1.IamAccount
}

func (r *IamAccountContext) GetObject() client.Object {
	return &r.Object
}

func (r *IamAccountContext) Generate(ctx *pulumi.Context) error {
	conf := sdkconfig.New(ctx, "google-native")
	project := conf.Require("project")

	// create a GSA for the IamAccount
	gsa, err := pulumigoogleiamv1.NewServiceAccount(ctx, "iamaccount", &pulumigoogleiamv1.ServiceAccountArgs{
		Project:     pulumi.String(project),
		AccountId:   pulumi.StringPtr(makeServiceAccountId(&r.Object)),
		DisplayName: pulumi.StringPtr(fmt.Sprintf("IamAccount/%s/%s", r.Object.Namespace, r.Object.Name)),
		Description: pulumi.StringPtr("Enables resource access for SN Cloud"),
	})
	if err != nil {
		return err
	}
	ctx.Export("gsa", gsa.Email)

	// create a KSA for the IamAccount
	ksa, err := pulumicorev1.NewServiceAccount(ctx, "iamaccount", &pulumicorev1.ServiceAccountArgs{
		Metadata: &pulumimetav1.ObjectMetaArgs{
			Name: pulumi.StringPtr(makeServiceAccountId(&r.Object)),
			Annotations: pulumi.StringMap(map[string]pulumi.StringInput{
				WorkloadIdentityKsaAnnotation: gsa.Email,
			}),
		},
	})
	if err != nil {
		return err
	}
	ctx.Export("ksa", ksa.Metadata.Name())

	return nil
}

func makeServiceAccountId(sa *pulumicontrollerv1.IamAccount) string {
	// "Service account ID must be between 6 and 30 characters."
	// "Service account ID must start with a lower case letter,
	//  followed by one or more lower case alphanumerical characters that can be separated by hyphens."
	return fmt.Sprintf("iamaccount-%s", hash(string(sa.UID)))
}

func hash(s string) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(s)))
}

func (r *IamAccountContext) UpdateStatus(ctx context.Context, event engine.Event) error {
	// TODO update status based on Pulumi events
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *IamAccountReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&pulumicontrollerv1.IamAccount{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}