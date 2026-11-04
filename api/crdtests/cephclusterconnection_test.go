/*
Copyright 2026 Flant JSC

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
package crdtests

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
)

func connection(name string) *v1alpha1.CephClusterConnection {
	return &v1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.CephClusterConnectionSpec{
			ClusterID: "2bf085fc-5119-404f-bb19-820ca6a1b07e",
			Monitors:  []string{"10.0.0.1:6789"},
			UserID:    "admin",
			UserKey:   "user-key",
		},
	}
}

func withSubvolumeGroup(name string) *v1alpha1.CephClusterConnection {
	ccc := connection(name)
	ccc.Spec.CephFS = &v1alpha1.CephClusterConnectionSpecCephFS{SubvolumeGroup: "csi"}
	return ccc
}

func TestCephClusterConnectionCreate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		ccc     func(name string) *v1alpha1.CephClusterConnection
		wantErr []string
	}{
		{name: "without cephFS", ccc: connection},
		{name: "with a subvolume group", ccc: withSubvolumeGroup},
		{
			name: "empty userKey",
			ccc: func(name string) *v1alpha1.CephClusterConnection {
				ccc := connection(name)
				ccc.Spec.UserKey = ""
				return ccc
			},
			wantErr: []string{"spec.userKey"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ccc := tt.ccc(objectName("create", tt.name))
			err := k8sClient.Create(context.Background(), ccc)
			if err == nil {
				t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), ccc) })
			}
			expectAdmission(t, err, tt.wantErr...)
		})
	}
}

func TestCephClusterConnectionUpdate(t *testing.T) {
	const cephFSImmutable = "The spec.cephFS field is immutable (cannot be added, removed, or changed)."

	for _, tt := range []struct {
		name    string
		ccc     func(name string) *v1alpha1.CephClusterConnection
		change  func(*v1alpha1.CephClusterConnection)
		wantErr []string
	}{
		{
			name:    "clusterID",
			ccc:     connection,
			change:  func(c *v1alpha1.CephClusterConnection) { c.Spec.ClusterID = "0a0f5b1e-0000-4000-8000-000000000000" },
			wantErr: []string{"The clusterID field is immutable."},
		},
		{
			name: "cephFS added",
			ccc:  connection,
			change: func(c *v1alpha1.CephClusterConnection) {
				c.Spec.CephFS = &v1alpha1.CephClusterConnectionSpecCephFS{SubvolumeGroup: "csi"}
			},
			wantErr: []string{cephFSImmutable},
		},
		{
			name:    "cephFS removed",
			ccc:     withSubvolumeGroup,
			change:  func(c *v1alpha1.CephClusterConnection) { c.Spec.CephFS = nil },
			wantErr: []string{cephFSImmutable},
		},
		{
			name:    "subvolume group changed",
			ccc:     withSubvolumeGroup,
			change:  func(c *v1alpha1.CephClusterConnection) { c.Spec.CephFS.SubvolumeGroup = "other" },
			wantErr: []string{"spec.cephFS.subvolumeGroup", "Value is immutable.", cephFSImmutable},
		},
		{
			name: "monitors and credentials are mutable",
			ccc:  withSubvolumeGroup,
			change: func(c *v1alpha1.CephClusterConnection) {
				c.Spec.Monitors = []string{"10.0.0.2:6789", "10.0.0.3:6789"}
				c.Spec.UserID = "csi"
				c.Spec.UserKey = "rotated-user-key"
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ccc := tt.ccc(objectName("update", tt.name))
			if err := k8sClient.Create(context.Background(), ccc); err != nil {
				t.Fatalf("create CephClusterConnection %s: %v", ccc.Name, err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), ccc) })
			tt.change(ccc)
			expectAdmission(t, k8sClient.Update(context.Background(), ccc), tt.wantErr...)
		})
	}
}
