/*
Copyright 2024 Flant JSC

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
	FSName string `json:"fsName,omitempty"`
	Pool   string `json:"pool,omitempty"`
}

// +k8s:deepcopy-gen=true
type CephStorageClassStatus struct {
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
}
