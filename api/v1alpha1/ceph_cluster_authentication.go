/*
Copyright 2025 Flant JSC

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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Ceph cluster authentication parameters.
// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels={"heritage=deckhouse","module=csi-ceph","backup.deckhouse.io/cluster-config=true"}
// +kubebuilder:printcolumn:name="Deprecated",type="string",JSONPath=".status.conditions[?(@.type==\"Deprecated\")].status"
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=".status.reason",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of this resource"
// +crd-enricher:crd:minimal=true
// +crd-enricher:crd:preserveUnknownFields=false
type CephClusterAuthentication struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CephClusterAuthenticationSpec    `json:"spec"`
	Status            *CephClusterAuthenticationStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type CephClusterAuthenticationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []CephClusterAuthentication `json:"items"`
}

// +k8s:deepcopy-gen=true
type CephClusterAuthenticationSpec struct {
	// Username without `client.`.
	UserID string `json:"userID"`

	// Ceph auth key corresponding to the `userID`.
	UserKey string `json:"userKey"`
}

// Displays current information about the resources managed by the CephClusterAuthentication custom resource.
// +k8s:deepcopy-gen=true
type CephClusterAuthenticationStatus struct {
	// Not published for this resource kind. This resource is deprecated and the controller
	// does not reconcile it towards a desired state, so there is no phase to report — see
	// the `Deprecated` condition instead. The field is retained for compatibility.
	// +kubebuilder:validation:Enum=Failed;Created
	Phase string `json:"phase,omitempty"`

	// Additional information about the resources managed by the CephClusterAuthentication custom resource.
	Reason string `json:"reason,omitempty"`

	// The value of `metadata.generation` the controller has last acted on.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// The latest observations of the resource state. Condition type: `Deprecated`.
	//
	// No `Ready` condition is published: the controller does not reconcile this resource
	// towards a desired state, it only marks it as superseded by CephClusterConnection.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +crd-enricher:raw:items.description=Condition contains details for one aspect of the current state of this API Resource.
	// +crd-enricher:raw:items.properties.type.description="Condition type. `Deprecated` is `True` for every CephClusterAuthentication:\nthe kind is superseded by CephClusterConnection, which carries the\nauthentication fields itself."
	// +crd-enricher:raw:items.properties.status.description=Current status of the condition.
	// +crd-enricher:raw:items.properties.observedGeneration.description=The value of `metadata.generation` this condition was set against.
	// +crd-enricher:raw:items.properties.lastTransitionTime.description=Timestamp of the last status transition for this condition.
	// +crd-enricher:raw:items.properties.reason.description="Machine-readable reason for the current status, suitable for log filtering\nand alerting."
	// +crd-enricher:raw:items.properties.message.description=Human-readable explanation of the current status.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
