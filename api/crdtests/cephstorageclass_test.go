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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/sds-common-lib/capacity"
)

func rbdClass(name string) *v1alpha1.CephStorageClass {
	return &v1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.CephStorageClassSpec{
			ClusterConnectionName: "ceph",
			ReclaimPolicy:         "Delete",
			Type:                  v1alpha1.CephStorageClassTypeRBD,
			RBD:                   &v1alpha1.CephStorageClassRBD{Pool: "rbd", DefaultFSType: "ext4"},
		},
	}
}

func cephFSClass(name string) *v1alpha1.CephStorageClass {
	return &v1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.CephStorageClassSpec{
			ClusterConnectionName: "ceph",
			ReclaimPolicy:         "Delete",
			Type:                  v1alpha1.CephStorageClassTypeCephFS,
			CephFS:                &v1alpha1.CephStorageClassCephFS{FSName: "cephfs"},
		},
	}
}

func createClass(t *testing.T, csc *v1alpha1.CephStorageClass) {
	t.Helper()
	if err := k8sClient.Create(context.Background(), csc); err != nil {
		t.Fatalf("create CephStorageClass %s: %v", csc.Name, err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), csc) })
}

func TestCephStorageClassCreate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		csc     func(name string) *v1alpha1.CephStorageClass
		wantErr []string
	}{
		{name: "RBD", csc: rbdClass},
		{name: "CephFS", csc: cephFSClass},
		{
			name: "CephFS without fsName",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := cephFSClass(name)
				csc.Spec.CephFS = nil
				return csc
			},
			wantErr: []string{"Field 'cephFS.fsName' is required for 'CephFS' type."},
		},
		{
			name: "CephFS with an empty fsName",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := cephFSClass(name)
				csc.Spec.CephFS.FSName = ""
				return csc
			},
			wantErr: []string{"Field 'cephFS.fsName' is required for 'CephFS' type."},
		},
		{
			name: "CephFS with an RBD pool",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := cephFSClass(name)
				csc.Spec.RBD = &v1alpha1.CephStorageClassRBD{Pool: "rbd"}
				return csc
			},
			wantErr: []string{"Field 'rbd' is forbidden for 'CephFS' type."},
		},
		{
			name: "RBD without rbd",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := rbdClass(name)
				csc.Spec.RBD = nil
				return csc
			},
			wantErr: []string{"Field 'rbd.pool' is required for 'RBD' type."},
		},
		{
			name: "RBD with an empty pool",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := rbdClass(name)
				csc.Spec.RBD.Pool = ""
				return csc
			},
			wantErr: []string{"Field 'rbd.pool' is required for 'RBD' type."},
		},
		{
			name: "RBD with a CephFS fsName",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := rbdClass(name)
				csc.Spec.CephFS = &v1alpha1.CephStorageClassCephFS{FSName: "cephfs"}
				return csc
			},
			wantErr: []string{"Field 'cephFS' is forbidden for 'RBD' type."},
		},
		{
			name: "unknown type",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := rbdClass(name)
				csc.Spec.Type = "NFS"
				return csc
			},
			wantErr: []string{"Unsupported value"},
		},
		{
			name: "unknown reclaim policy",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := rbdClass(name)
				csc.Spec.ReclaimPolicy = "Recycle"
				return csc
			},
			wantErr: []string{"Unsupported value"},
		},
		{
			name: "empty clusterConnectionName",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := rbdClass(name)
				csc.Spec.ClusterConnectionName = ""
				return csc
			},
			wantErr: []string{"spec.clusterConnectionName"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			csc := tt.csc(objectName("create", tt.name))
			err := k8sClient.Create(context.Background(), csc)
			if err == nil {
				t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), csc) })
			}
			expectAdmission(t, err, tt.wantErr...)
		})
	}
}

func TestCephStorageClassUpdate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		csc     func(name string) *v1alpha1.CephStorageClass
		change  func(*v1alpha1.CephStorageClass)
		wantErr []string
	}{
		{
			name: "type",
			csc:  rbdClass,
			change: func(c *v1alpha1.CephStorageClass) {
				c.Spec.Type = v1alpha1.CephStorageClassTypeCephFS
				c.Spec.RBD = nil
				c.Spec.CephFS = &v1alpha1.CephStorageClassCephFS{FSName: "cephfs"}
			},
			wantErr: []string{"spec.type", "Value is immutable."},
		},
		{
			name:    "reclaimPolicy",
			csc:     rbdClass,
			change:  func(c *v1alpha1.CephStorageClass) { c.Spec.ReclaimPolicy = "Retain" },
			wantErr: []string{"spec.reclaimPolicy", "Value is immutable."},
		},
		{
			name:    "clusterConnectionName",
			csc:     rbdClass,
			change:  func(c *v1alpha1.CephStorageClass) { c.Spec.ClusterConnectionName = "other" },
			wantErr: []string{"spec.clusterConnectionName", "Value is immutable."},
		},
		{
			name:    "rbd.pool",
			csc:     rbdClass,
			change:  func(c *v1alpha1.CephStorageClass) { c.Spec.RBD.Pool = "other" },
			wantErr: []string{"spec.rbd.pool", "Value is immutable."},
		},
		{
			name:    "rbd.defaultFSType",
			csc:     rbdClass,
			change:  func(c *v1alpha1.CephStorageClass) { c.Spec.RBD.DefaultFSType = "xfs" },
			wantErr: []string{"spec.rbd.defaultFSType", "Value is immutable."},
		},
		{
			name: "rbd.defaultFSType set once when empty",
			csc: func(name string) *v1alpha1.CephStorageClass {
				csc := rbdClass(name)
				csc.Spec.RBD.DefaultFSType = ""
				return csc
			},
			change: func(c *v1alpha1.CephStorageClass) { c.Spec.RBD.DefaultFSType = "xfs" },
		},
		{
			name:    "cephFS.fsName",
			csc:     cephFSClass,
			change:  func(c *v1alpha1.CephStorageClass) { c.Spec.CephFS.FSName = "other" },
			wantErr: []string{"spec.cephFS.fsName", "Value is immutable."},
		},
		{
			name:   "clusterAuthenticationName is mutable",
			csc:    rbdClass,
			change: func(c *v1alpha1.CephStorageClass) { c.Spec.ClusterAuthenticationName = "auth" },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			csc := tt.csc(objectName("update", tt.name))
			createClass(t, csc)
			tt.change(csc)
			expectAdmission(t, k8sClient.Update(context.Background(), csc), tt.wantErr...)
		})
	}
}

func TestCephStorageClassStatusCapacity(t *testing.T) {
	ctx := context.Background()
	csc := rbdClass("status-capacity")
	createClass(t, csc)

	at := metav1.NewTime(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC))
	quota := resource.MustParse("50Gi")
	freeByQuota := resource.MustParse("40Gi")
	csc.Status = &v1alpha1.CephStorageClassStatus{Capacity: &v1alpha1.CephStorageClassCapacity{
		Capacity: capacity.Capacity{
			Size:        resource.MustParse("100Gi"),
			Used:        resource.MustParse("10Gi"),
			FreeOnDisk:  resource.MustParse("90Gi"),
			Quota:       &quota,
			FreeByQuota: &freeByQuota,
			UpdatedAt:   at,
		},
		Pool:          "rbd",
		ImagesInTrash: 2,
	}}
	if err := k8sClient.Status().Update(ctx, csc); err != nil {
		t.Fatalf("update the status with capacity: %v", err)
	}

	got := &v1alpha1.CephStorageClass{}
	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(csc), got); err != nil {
		t.Fatal(err)
	}
	c := got.Status.Capacity
	if c == nil || c.Pool != "rbd" || c.FreeOnDisk.Cmp(resource.MustParse("90Gi")) != 0 || c.FreeByQuota == nil || c.FreeByQuota.Cmp(freeByQuota) != 0 || c.ImagesInTrash != 2 || !c.UpdatedAt.Equal(&at) {
		t.Fatalf("capacity read back as %+v", c)
	}

	for name, capacity := range map[string]string{
		"without pool":       `{"pool":null}`,
		"without freeOnDisk": `{"freeOnDisk":null}`,
		"without updatedAt":  `{"updatedAt":null}`,
		"not a quantity":     `{"freeOnDisk":"lots"}`,
	} {
		t.Run(name, func(t *testing.T) {
			patch := client.RawPatch(types.MergePatchType, []byte(`{"status":{"capacity":`+capacity+`}}`))
			err := k8sClient.Status().Patch(ctx, got.DeepCopy(), patch)
			if !apierrors.IsInvalid(err) {
				t.Fatalf("status patch %s: %v; want Invalid", capacity, err)
			}
		})
	}
}
