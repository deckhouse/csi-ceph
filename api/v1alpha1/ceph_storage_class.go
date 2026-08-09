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

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CephStorageClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CephStorageClassSpec    `json:"spec"`
	Status            *CephStorageClassStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen=true
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type CephStorageClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []CephStorageClass `json:"items"`
}

// +k8s:deepcopy-gen=true
type CephStorageClassSpec struct {
	ClusterConnectionName     string                  `json:"clusterConnectionName"`
	ClusterAuthenticationName string                  `json:"clusterAuthenticationName,omitempty"`
	ReclaimPolicy             string                  `json:"reclaimPolicy"`
	Type                      string                  `json:"type"`
	RBD                       *CephStorageClassRBD    `json:"rbd,omitempty"`
	CephFS                    *CephStorageClassCephFS `json:"cephFS,omitempty"`
}

// +k8s:deepcopy-gen=true
type CephStorageClassRBD struct {
	DefaultFSType string `json:"defaultFSType"`
	Pool          string `json:"pool"`
}

// +k8s:deepcopy-gen=true
type CephStorageClassCephFS struct {
	FSName string `json:"fsName"`
}

// +k8s:deepcopy-gen=true
type CephStorageClassStatus struct {
	// Phase is a coarse summary derived from Conditions, kept for
	// compatibility with the printer column and existing tooling.
	// Conditions are the source of truth.
	Phase string `json:"phase,omitempty"`

	// Reason carries the summary the last reconcile pass produced. It is not
	// always the same text as the Ready condition message: on failure the
	// condition carries the wrapped error, which is the more specific of the
	// two.
	Reason string `json:"reason,omitempty"`

	// ObservedGeneration is the most recent metadata.generation the
	// controller has acted on. When it trails metadata.generation the
	// controller has not yet processed the latest spec.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds the latest observations of the resource state.
	// Condition type: Ready.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}
