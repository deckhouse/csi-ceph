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

// Ceph cluster connection parameters.
// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:metadata:labels={"heritage=deckhouse","module=csi-ceph","backup.deckhouse.io/cluster-config=true"}
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status"
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=".status.reason",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The age of this resource"
// +kubebuilder:validation:XValidation:rule="oldSelf == null || (has(self.spec.cephFS) == has(oldSelf.spec.cephFS) && (!has(self.spec.cephFS) || self.spec.cephFS == oldSelf.spec.cephFS))",message="The spec.cephFS field is immutable (cannot be added, removed, or changed)."
// +crd-enricher:crd:minimal=true
// +crd-enricher:crd:preserveUnknownFields=false
type CephClusterConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CephClusterConnectionSpec    `json:"spec"`
	Status            *CephClusterConnectionStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type CephClusterConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []CephClusterConnection `json:"items"`
}

// +k8s:deepcopy-gen=true
type CephClusterConnectionSpec struct {
	// Ceph cluster FSID/UUID.
	//
	// Use `ceph fsid` to get Ceph cluster FSID/UUID.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="The clusterID field is immutable."
	ClusterID string `json:"clusterID"`

	// List of ceph-mon IP addresses in the format `10.0.0.10:6789`.
	Monitors []string `json:"monitors"`

	// Ceph user name.
	UserID string `json:"userID"`

	// Ceph user key.
	// +kubebuilder:validation:MinLength=1
	UserKey string `json:"userKey"`

	// CephFS specific parameters (Cluster wide).
	CephFS *CephClusterConnectionSpecCephFS `json:"cephFS,omitempty"`
}

// +k8s:deepcopy-gen=true
type CephClusterConnectionSpecCephFS struct {
	// CephFS subvolume group name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	SubvolumeGroup string `json:"subvolumeGroup,omitempty"`
}

// Displays current information about the resources managed by the CephClusterConnection custom resource.
// +k8s:deepcopy-gen=true
type CephClusterConnectionStatus struct {
	// The current state of resources managed by the CephClusterConnection custom resource. Might be:
	// - Failed (if the controller received incorrect resource configuration or some errors occurred during the operation)
	// - Created (if everything went fine)
	// +kubebuilder:validation:Enum=Failed;Created
	Phase string `json:"phase,omitempty"`

	// Additional information about the resources managed by the CephClusterConnection custom resource.
	Reason string `json:"reason,omitempty"`

	// The value of `metadata.generation` the controller has last acted on. When it trails
	// `metadata.generation`, the controller has not yet processed the latest spec, and the
	// conditions below still describe the previous one.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// The latest observations of the resource state. Condition type: `Ready`.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +crd-enricher:unset:items.description
	// +crd-enricher:raw:items.properties.type.description="Condition type. `Ready` is `True` once the secret and the CSI ConfigMap entry\nare in place and `False` when a reconcile pass failed. The condition is\nabsent until the controller has processed the resource for the first time."
	// +crd-enricher:raw:items.properties.status.description=Current status of the condition.
	// +crd-enricher:raw:items.properties.observedGeneration.description=The value of `metadata.generation` this condition was set against.
	// +crd-enricher:raw:items.properties.lastTransitionTime.description="Timestamp of the last status transition for this condition. It is not bumped\nwhen only the reason or the message changes."
	// +crd-enricher:raw:items.properties.reason.description="Machine-readable reason for the current status, suitable for log filtering\nand alerting."
	// +crd-enricher:raw:items.properties.message.description=Human-readable explanation of the current status.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +k8s:deepcopy-gen=true
type ClusterConfig struct {
	CephFS    CephFSConfig `json:"cephFS"`
	RBD       RBDConfig    `json:"rbd"`
	ClusterID string       `json:"clusterID"`
	Monitors  []string     `json:"monitors"`
}

// +k8s:deepcopy-gen=true
type CephFSConfig struct {
	SubvolumeGroup             string          `json:"subvolumeGroup,omitempty"`
	ControllerPublishSecretRef SecretReference `json:"controllerPublishSecretRef"`
}

// +k8s:deepcopy-gen=true
type RBDConfig struct {
	ControllerPublishSecretRef SecretReference `json:"controllerPublishSecretRef"`
}

// +k8s:deepcopy-gen=true
type SecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}
