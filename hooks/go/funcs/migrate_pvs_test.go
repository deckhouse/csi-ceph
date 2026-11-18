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

package funcs

import (
	"context"
	"errors"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
)

const (
	migrateSC        = "ceph-rbd"
	migrateOldSecret = "csi-ceph-old"
	migrateNewSecret = "csi-ceph-secret-for-conn"
	migrateSource    = "migrate-from-ceph-csi-module"
)

var migrateBackupTime = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

func migratePV(name, secretNamespace, secretName string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				PVAnnotationProvisionerDeletionSecretNamespace: secretNamespace,
				PVAnnotationProvisionerDeletionSecretName:      secretName,
			},
		},
		Spec: v1.PersistentVolumeSpec{
			Capacity:                      v1.ResourceList{v1.ResourceStorage: resource.MustParse("1Gi")},
			AccessModes:                   []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
			StorageClassName:              migrateSC,
			PersistentVolumeReclaimPolicy: v1.PersistentVolumeReclaimDelete,
			ClaimRef:                      &v1.ObjectReference{Namespace: "app", Name: "data-" + name},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:                    "rbd.csi.ceph.com",
					VolumeHandle:              "0001-0009-rook-ceph-0000000000000002-" + name,
					NodeStageSecretRef:        &v1.SecretReference{Namespace: secretNamespace, Name: secretName},
					ControllerExpandSecretRef: &v1.SecretReference{Namespace: secretNamespace, Name: secretName},
				},
			},
		},
	}
}

func migrateScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func newMigrateClient(t *testing.T, collide string, competitor *v1.PersistentVolume, pvs ...*v1.PersistentVolume) client.Client {
	t.Helper()

	objs := make([]client.Object, 0, len(pvs))
	for _, pv := range pvs {
		objs = append(objs, pv.DeepCopy())
	}

	return fake.NewClientBuilder().
		WithScheme(migrateScheme(t)).
		WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if pv, ok := obj.(*v1.PersistentVolume); ok && pv.Name == collide && competitor != nil {
					if err := c.Create(ctx, competitor.DeepCopy()); err != nil {
						return err
					}
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()
}

func migratePVList(pvs ...*v1.PersistentVolume) *v1.PersistentVolumeList {
	list := &v1.PersistentVolumeList{}
	for _, pv := range pvs {
		list.Items = append(list.Items, *pv.DeepCopy())
	}
	return list
}

func backupRecreatedLabel(t *testing.T, cl client.Client, pvName string) string {
	t.Helper()
	backup := &v1alpha1.CephMetadataBackup{}
	name := migrateBackupTime.Format("20060102-150405") + "-persistentvolume-" + pvName
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, backup); err != nil {
		t.Fatalf("get backup %s: %v", name, err)
	}
	return backup.Labels[PVRecreatedSuccesfullyLabelKey]
}

func referencesNewSecret(pv *v1.PersistentVolume) bool {
	want := v1.SecretReference{Namespace: ModuleNamespace, Name: migrateNewSecret}
	return pv.Spec.CSI != nil &&
		pv.Spec.CSI.NodeStageSecretRef != nil && *pv.Spec.CSI.NodeStageSecretRef == want &&
		pv.Spec.CSI.ControllerExpandSecretRef != nil && *pv.Spec.CSI.ControllerExpandSecretRef == want &&
		pv.Annotations[PVAnnotationProvisionerDeletionSecretNamespace] == ModuleNamespace &&
		pv.Annotations[PVAnnotationProvisionerDeletionSecretName] == migrateNewSecret
}

func getMigratePV(t *testing.T, cl client.Client, name string) *v1.PersistentVolume {
	t.Helper()
	pv := &v1.PersistentVolume{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, pv); err != nil {
		t.Fatalf("get PV %s: %v", name, err)
	}
	return pv
}

func TestMigratePVsToNewSecretContinuesAfterExistingMigratedPV(t *testing.T) {
	pvs := []*v1.PersistentVolume{
		migratePV("pvc-a", "d8-ceph-csi", migrateOldSecret),
		migratePV("pvc-b", "d8-ceph-csi", migrateOldSecret),
		migratePV("pvc-c", "d8-ceph-csi", migrateOldSecret),
	}
	competitor := migratePV("pvc-a", ModuleNamespace, migrateNewSecret)
	competitor.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimRetain

	cl := newMigrateClient(t, "pvc-a", competitor, pvs...)

	err := MigratePVsToNewSecret(context.Background(), cl, migratePVList(pvs...), migrateBackupTime, migrateSC, migrateNewSecret, migrateSource, "test")
	if err != nil {
		t.Fatalf("MigratePVsToNewSecret: %v", err)
	}

	for _, name := range []string{"pvc-a", "pvc-b", "pvc-c"} {
		pv := getMigratePV(t, cl, name)
		if !referencesNewSecret(pv) {
			t.Errorf("PV %s references %v and %v, want %s/%s", name, pv.Spec.CSI.NodeStageSecretRef, pv.Spec.CSI.ControllerExpandSecretRef, ModuleNamespace, migrateNewSecret)
		}
		if got := backupRecreatedLabel(t, cl, name); got != LabelValueTrue {
			t.Errorf("backup of PV %s has %s=%q, want %q", name, PVRecreatedSuccesfullyLabelKey, got, LabelValueTrue)
		}
	}

	if got := getMigratePV(t, cl, "pvc-a").Spec.PersistentVolumeReclaimPolicy; got != v1.PersistentVolumeReclaimDelete {
		t.Errorf("PV pvc-a reclaim policy %s, want %s", got, v1.PersistentVolumeReclaimDelete)
	}
}

func TestMigratePVsToNewSecretFailsOnExistingUnmigratedPV(t *testing.T) {
	cases := map[string]func(pv *v1.PersistentVolume){
		"old secret": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI.NodeStageSecretRef = &v1.SecretReference{Namespace: "d8-ceph-csi", Name: migrateOldSecret}
			pv.Spec.CSI.ControllerExpandSecretRef = &v1.SecretReference{Namespace: "d8-ceph-csi", Name: migrateOldSecret}
		},
		"old node stage secret": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI.NodeStageSecretRef = &v1.SecretReference{Namespace: "d8-ceph-csi", Name: migrateOldSecret}
		},
		"old controller expand secret": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI.ControllerExpandSecretRef = &v1.SecretReference{Namespace: "d8-ceph-csi", Name: migrateOldSecret}
		},
		"another secret of the module": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI.NodeStageSecretRef.Name = "csi-ceph-secret-for-other"
			pv.Spec.CSI.ControllerExpandSecretRef.Name = "csi-ceph-secret-for-other"
		},
		"new secret name in another namespace": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI.NodeStageSecretRef.Namespace = "app"
			pv.Spec.CSI.ControllerExpandSecretRef.Namespace = "app"
		},
		"no node stage secret": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI.NodeStageSecretRef = nil
		},
		"no controller expand secret": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI.ControllerExpandSecretRef = nil
		},
		"not a CSI volume": func(pv *v1.PersistentVolume) {
			pv.Spec.CSI = nil
			pv.Spec.HostPath = &v1.HostPathVolumeSource{Path: "/srv"}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			pvs := []*v1.PersistentVolume{
				migratePV("pvc-a", "d8-ceph-csi", migrateOldSecret),
				migratePV("pvc-b", "d8-ceph-csi", migrateOldSecret),
			}
			competitor := migratePV("pvc-a", ModuleNamespace, migrateNewSecret)
			competitor.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimRetain
			mutate(competitor)

			cl := newMigrateClient(t, "pvc-a", competitor, pvs...)

			err := MigratePVsToNewSecret(context.Background(), cl, migratePVList(pvs...), migrateBackupTime, migrateSC, migrateNewSecret, migrateSource, "test")
			if err == nil {
				t.Fatal("MigratePVsToNewSecret returned nil, want an error")
			}

			if got := backupRecreatedLabel(t, cl, "pvc-a"); got != LabelValueFalse {
				t.Errorf("backup of PV pvc-a has %s=%q, want %q", PVRecreatedSuccesfullyLabelKey, got, LabelValueFalse)
			}
			if got := getMigratePV(t, cl, "pvc-a").Spec.PersistentVolumeReclaimPolicy; got != v1.PersistentVolumeReclaimRetain {
				t.Errorf("PV pvc-a reclaim policy %s, want %s", got, v1.PersistentVolumeReclaimRetain)
			}
		})
	}
}

func TestMigratePVsToNewSecretReturnsCreateError(t *testing.T) {
	pvs := []*v1.PersistentVolume{
		migratePV("pvc-a", "d8-ceph-csi", migrateOldSecret),
		migratePV("pvc-b", "d8-ceph-csi", migrateOldSecret),
	}
	cl := fake.NewClientBuilder().
		WithScheme(migrateScheme(t)).
		WithObjects(pvs[0].DeepCopy(), pvs[1].DeepCopy()).
		WithInterceptorFuncs(interceptor.Funcs{
			Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
				if _, ok := obj.(*v1.PersistentVolume); ok {
					return apierrors.NewInternalError(errors.New("etcd is unavailable"))
				}
				return c.Create(ctx, obj, opts...)
			},
		}).
		Build()

	err := MigratePVsToNewSecret(context.Background(), cl, migratePVList(pvs...), migrateBackupTime, migrateSC, migrateNewSecret, migrateSource, "test")
	if !apierrors.IsInternalError(err) {
		t.Fatalf("MigratePVsToNewSecret returned %v, want the create error", err)
	}
	if got := backupRecreatedLabel(t, cl, "pvc-a"); got != LabelValueFalse {
		t.Errorf("backup of PV pvc-a has %s=%q, want %q", PVRecreatedSuccesfullyLabelKey, got, LabelValueFalse)
	}
}

func TestMigratePVsToNewSecretMigratesEveryPVOfClass(t *testing.T) {
	pvs := []*v1.PersistentVolume{
		migratePV("pvc-a", "d8-ceph-csi", migrateOldSecret),
		migratePV("pvc-b", "d8-ceph-csi", migrateOldSecret),
	}

	cl := newMigrateClient(t, "", nil, pvs...)

	err := MigratePVsToNewSecret(context.Background(), cl, migratePVList(pvs...), migrateBackupTime, migrateSC, migrateNewSecret, migrateSource, "test")
	if err != nil {
		t.Fatalf("MigratePVsToNewSecret: %v", err)
	}

	for _, name := range []string{"pvc-a", "pvc-b"} {
		pv := getMigratePV(t, cl, name)
		if !referencesNewSecret(pv) {
			t.Errorf("PV %s references %v and %v, want %s/%s", name, pv.Spec.CSI.NodeStageSecretRef, pv.Spec.CSI.ControllerExpandSecretRef, ModuleNamespace, migrateNewSecret)
		}
		if got := backupRecreatedLabel(t, cl, name); got != LabelValueTrue {
			t.Errorf("backup of PV %s has %s=%q, want %q", name, PVRecreatedSuccesfullyLabelKey, got, LabelValueTrue)
		}
	}
}

func TestMigratePVsToNewSecretContinuesAfterExistingPVWithoutControllerExpandSecret(t *testing.T) {
	withoutExpand := func(pv *v1.PersistentVolume) *v1.PersistentVolume {
		pv.Spec.CSI.ControllerExpandSecretRef = nil
		return pv
	}
	pvs := []*v1.PersistentVolume{
		withoutExpand(migratePV("pvc-a", "d8-ceph-csi", migrateOldSecret)),
		withoutExpand(migratePV("pvc-b", "d8-ceph-csi", migrateOldSecret)),
	}
	competitor := withoutExpand(migratePV("pvc-a", ModuleNamespace, migrateNewSecret))
	competitor.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimRetain

	cl := newMigrateClient(t, "pvc-a", competitor, pvs...)

	err := MigratePVsToNewSecret(context.Background(), cl, migratePVList(pvs...), migrateBackupTime, migrateSC, migrateNewSecret, migrateSource, "test")
	if err != nil {
		t.Fatalf("MigratePVsToNewSecret: %v", err)
	}

	want := v1.SecretReference{Namespace: ModuleNamespace, Name: migrateNewSecret}
	for _, name := range []string{"pvc-a", "pvc-b"} {
		pv := getMigratePV(t, cl, name)
		if ref := pv.Spec.CSI.NodeStageSecretRef; ref == nil || *ref != want {
			t.Errorf("PV %s NodeStageSecretRef = %v, want %v", name, ref, want)
		}
		if ref := pv.Spec.CSI.ControllerExpandSecretRef; ref != nil {
			t.Errorf("PV %s ControllerExpandSecretRef = %v, want none", name, ref)
		}
		if got := backupRecreatedLabel(t, cl, name); got != LabelValueTrue {
			t.Errorf("backup of PV %s has %s=%q, want %q", name, PVRecreatedSuccesfullyLabelKey, got, LabelValueTrue)
		}
	}
	if got := getMigratePV(t, cl, "pvc-a").Spec.PersistentVolumeReclaimPolicy; got != v1.PersistentVolumeReclaimDelete {
		t.Errorf("PV pvc-a reclaim policy %s, want %s", got, v1.PersistentVolumeReclaimDelete)
	}
}

func TestMigratePVsToNewSecretFailsOnExistingPVWithExtraControllerExpandSecret(t *testing.T) {
	pvs := []*v1.PersistentVolume{
		migratePV("pvc-a", "d8-ceph-csi", migrateOldSecret),
		migratePV("pvc-b", "d8-ceph-csi", migrateOldSecret),
	}
	pvs[0].Spec.CSI.ControllerExpandSecretRef = nil
	competitor := migratePV("pvc-a", ModuleNamespace, migrateNewSecret)
	competitor.Spec.PersistentVolumeReclaimPolicy = v1.PersistentVolumeReclaimRetain

	cl := newMigrateClient(t, "pvc-a", competitor, pvs...)

	err := MigratePVsToNewSecret(context.Background(), cl, migratePVList(pvs...), migrateBackupTime, migrateSC, migrateNewSecret, migrateSource, "test")
	if err == nil {
		t.Fatal("MigratePVsToNewSecret returned nil, want an error")
	}
	if got := backupRecreatedLabel(t, cl, "pvc-a"); got != LabelValueFalse {
		t.Errorf("backup of PV pvc-a has %s=%q, want %q", PVRecreatedSuccesfullyLabelKey, got, LabelValueFalse)
	}
}
