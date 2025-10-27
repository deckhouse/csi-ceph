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

type CephClusterConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CephClusterConnectionSpec    `json:"spec"`
	Status            *CephClusterConnectionStatus `json:"status,omitempty"`
}

type CephClusterConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []CephClusterConnection `json:"items"`
}

type CephClusterConnectionSpec struct {
	ClusterID string                          `json:"clusterID"`
	Monitors  []string                        `json:"monitors"`
	UserID    string                          `json:"userID"`
	UserKey   string                          `json:"userKey"`
	CephFS    CephClusterConnectionSpecCephFS `json:"cephFS"`
}

type CephClusterConnectionSpecCephFS struct {
	SubvolumeGroup string `json:"subvolumeGroup,omitempty"`
}

type CephClusterConnectionStatus struct {
	Phase  string `json:"phase,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type ClusterConfig struct {
	CephFS    CephFSConfig `json:"cephFS"`
	ClusterID string       `json:"clusterID"`
	Monitors  []string     `json:"monitors"`
}

type CephFSConfig struct {
	SubvolumeGroup             *string         `json:"subvolumeGroup,omitempty"`
	ControllerPublishSecretRef SecretReference `json:"controllerPublishSecretRef"`
}

type SecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}
