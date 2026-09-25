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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const (
	CephStorageClassTypeRBD    = "RBD"
	CephStorageClassTypeCephFS = "CephFS"
)

var (
	DefaultMountOptionsRBD = []string{"discard"}
)

// CephStorageClass is a Kubernetes Custom Resource that defines a configuration for a Kubernetes Storage class.
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
// +crd-enricher:crd:minimal=true
// +crd-enricher:crd:preserveUnknownFields=false
type CephStorageClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Defines the StorageClass configuration.
	Spec CephStorageClassSpec `json:"spec"`

	Status *CephStorageClassStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
type CephStorageClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []CephStorageClass `json:"items"`
}

// +k8s:deepcopy-gen=true
// +kubebuilder:validation:XValidation:rule=`self.type != "CephFS" || (has(self.cephFS) && has(self.cephFS.fsName) && self.cephFS.fsName != "")`,message="Field 'cephFS.fsName' is required for 'CephFS' type."
// +kubebuilder:validation:XValidation:rule=`self.type != "CephFS" || !has(self.rbd) || !has(self.rbd.pool) || self.rbd.pool == ""`,message="Field 'rbd' is forbidden for 'CephFS' type."
// +kubebuilder:validation:XValidation:rule=`self.type != "RBD" || (has(self.rbd) && has(self.rbd.pool) && self.rbd.pool != "")`,message="Field 'rbd.pool' is required for 'RBD' type."
// +kubebuilder:validation:XValidation:rule=`self.type != "RBD" || !has(self.cephFS) || !has(self.cephFS.fsName) || self.cephFS.fsName == ""`,message="Field 'cephFS' is forbidden for 'RBD' type."
type CephStorageClassSpec struct {
	// Name of the CephClusterConnection custom resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	ClusterConnectionName string `json:"clusterConnectionName"`

	// Name of the CephClusterAuthentication custom resource.
	// +kubebuilder:validation:MinLength=1
	ClusterAuthenticationName string `json:"clusterAuthenticationName,omitempty"`

	// The storage class's reclaim policy. Might be:
	// - Delete (If the Persistent Volume Claim is deleted, deletes the Persistent Volume and its associated storage as well)
	// - Retain (If the Persistent Volume Claim is deleted, remains the Persistent Volume and its associated storage)
	// +kubebuilder:validation:Enum=Delete;Retain
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	ReclaimPolicy string `json:"reclaimPolicy"`

	// The type of the storage class. Might be:
	// - CephFS
	// - RBD (Rados Block Device)
	// +kubebuilder:validation:Enum=CephFS;RBD
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable."
	Type string `json:"type"`

	// Rados Block Device specific parameters.
	RBD *CephStorageClassRBD `json:"rbd,omitempty"`

	// CephFS specific parameters.
	CephFS *CephStorageClassCephFS `json:"cephFS,omitempty"`
}

// +k8s:deepcopy-gen=true
type CephStorageClassRBD struct {
	// Default file system type for the Rados Block Device.
	// +optional
	// +kubebuilder:default=ext4
	// +kubebuilder:validation:XValidation:rule=`self == oldSelf || oldSelf == ""`,message="Value is immutable."
	DefaultFSType string `json:"defaultFSType"`

	// Name of the RBD pool.
	// +optional
	// +kubebuilder:validation:XValidation:rule=`self == oldSelf || oldSelf == ""`,message="Value is immutable."
	Pool string `json:"pool"`
}

// +k8s:deepcopy-gen=true
type CephStorageClassCephFS struct {
	// Name of the CephFS file system.
	// +optional
	// +kubebuilder:validation:XValidation:rule=`self == oldSelf || oldSelf == ""`,message="Value is immutable."
	FSName string `json:"fsName"`
}

// Displays current information about the Storage Class.
// +k8s:deepcopy-gen=true
type CephStorageClassStatus struct {
	// The Storage class current state. Might be:
	// - Failed (if the controller received incorrect resource configuration or some errors occurred during the operation)
	// - Created (if everything went fine)
	// +kubebuilder:validation:Enum=Failed;Created
	Phase string `json:"phase,omitempty"`

	// Additional information about the current state of the Storage Class.
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
	// +crd-enricher:raw:items.properties.type.description="Condition type. `Ready` is `True` once the StorageClass and the\nVolumeSnapshotClass are in place and `False` when a reconcile pass failed.\nThe condition is absent until the controller has processed the resource for\nthe first time."
	// +crd-enricher:raw:items.properties.status.description=Current status of the condition.
	// +crd-enricher:raw:items.properties.observedGeneration.description=The value of `metadata.generation` this condition was set against.
	// +crd-enricher:raw:items.properties.lastTransitionTime.description="Timestamp of the last status transition for this condition. It is not bumped\nwhen only the reason or the message changes."
	// +crd-enricher:raw:items.properties.reason.description="Machine-readable reason for the current status, suitable for log filtering\nand alerting."
	// +crd-enricher:raw:items.properties.message.description=Human-readable explanation of the current status.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
