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
	DefaultMountOptions = []string{"discard"}
)

type CephStorageClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CephStorageClassSpec    `json:"spec"`
	Status            *CephStorageClassStatus `json:"status,omitempty"`
}

// CephStorageClassList contains a list of empty block device
type CephStorageClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []CephStorageClass `json:"items"`
}

type CephStorageClassSpec struct {
	ClusterConnectionName     string                  `json:"clusterConnectionName"`
	ClusterAuthenticationName string                  `json:"clusterAuthenticationName"`
	ReclaimPolicy             string                  `json:"reclaimPolicy"`
	Type                      string                  `json:"type"`
	RBD                       *CephStorageClassRBD    `json:"RBD,omitempty"`
	CephFS                    *CephStorageClassCephFS `json:"cephFS,omitempty"`
}

type CephStorageClassRBD struct {
	DefaultFSType string `json:"defaultFSType"`
	Pool          string `json:"pool"`
}

type CephStorageClassCephFS struct {
	FSName string `json:"fsName,omitempty"`
}

type CephStorageClassStatus struct {
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
}
