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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	IamAccountTypeGoogle     = "google"
	IamAccountTypeGeneric = "generic"
)

type IamAccountType string

// IamAccountSpec defines the desired state of IamAccount
type IamAccountSpec struct {
	Type IamAccountType `json:"type"`

	// +optional
	Google *IamAccountSpecGoogle `json:"google,omitempty"`
}

type IamAccountSpecGoogle struct {
	Project string `json:"project"`
}

// IamAccountStatus defines the observed state of IamAccount
type IamAccountStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is an array of conditions.
	// Known .status.conditions.type are: "Ready"
	//+patchMergeKey=type
	//+patchStrategy=merge
	//+listType=map
	//+listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// GSA the associated Google Service Account email address
	// +optional
	GSA string `json:"gsa,omitempty"`

	// KSA the associated Kubernetes Service Account name
	// +optional
	KSA string `json:"ksa,omitempty"`
}

const (
	ConditionReady string = "Ready"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// IamAccount is the Schema for the iamaccounts API
type IamAccount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   IamAccountSpec   `json:"spec,omitempty"`
	Status IamAccountStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// IamAccountList contains a list of IamAccount
type IamAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []IamAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IamAccount{}, &IamAccountList{})
}
