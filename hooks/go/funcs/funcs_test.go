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
	"encoding/base64"
	"encoding/json"
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

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
)

const (
	testBackupSource = "migrate-from-ceph-csi-module"
	testBackupDate   = "20250401-120000"
)

func newTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func rbdPV(name string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.PersistentVolumeSpec{
			Capacity:         v1.ResourceList{v1.ResourceStorage: resource.MustParse("1Gi")},
			AccessModes:      []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
			StorageClassName: "ceph-rbd",
			ClaimRef:         &v1.ObjectReference{Namespace: "app", Name: "data"},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       "rbd.csi.ceph.com",
					VolumeHandle: "0001-0009-rook-ceph-0000000000000002-" + name,
				},
			},
		},
	}
}

func pendingBackup(t *testing.T, name string, obj runtime.Object) *v1alpha1.CephMetadataBackup {
	t.Helper()
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return &v1alpha1.CephMetadataBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				BackupDateLabelKey:             testBackupDate,
				BackupSourceLabelKey:           testBackupSource,
				ResourceKindLabelKey:           ResourceKindPersistentVolume,
				PVRecreatedSuccesfullyLabelKey: LabelValueFalse,
			},
		},
		Spec: v1alpha1.CephMetadataBackupSpec{Data: base64.StdEncoding.EncodeToString(data)},
	}
}

func recreatedLabel(t *testing.T, cl client.Client, name string) string {
	t.Helper()
	backup := &v1alpha1.CephMetadataBackup{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name}, backup); err != nil {
		t.Fatal(err)
	}
	return backup.Labels[PVRecreatedSuccesfullyLabelKey]
}

func pvExists(t *testing.T, cl client.Client, name string) bool {
	t.Helper()
	err := cl.Get(context.Background(), types.NamespacedName{Name: name}, &v1.PersistentVolume{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatal(err)
	}
	return true
}

func TestProcessRemovedPVsRestoresBackupOfTheHook(t *testing.T) {
	ctx := context.Background()
	cl := newTestClient(t)
	pv := rbdPV("pvc-1")

	backupTime, err := time.Parse("20060102-150405", testBackupDate)
	if err != nil {
		t.Fatal(err)
	}
	backupName, err := BackupResource(ctx, cl, pv, backupTime, testBackupSource, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := setRecreateLabelToBackupResource(ctx, cl, backupName, LabelValueFalse); err != nil {
		t.Fatal(err)
	}

	if err := ProcessRemovedPVs(ctx, cl, testBackupSource, "test"); err != nil {
		t.Fatal(err)
	}

	restored := &v1.PersistentVolume{}
	if err := cl.Get(ctx, types.NamespacedName{Name: pv.Name}, restored); err != nil {
		t.Fatalf("PersistentVolume was not restored: %v", err)
	}
	if restored.Spec.CSI == nil || restored.Spec.CSI.VolumeHandle != pv.Spec.CSI.VolumeHandle {
		t.Fatalf("restored PersistentVolume has CSI source %+v, want %+v", restored.Spec.CSI, pv.Spec.CSI)
	}
	if got := recreatedLabel(t, cl, backupName); got != LabelValueTrue {
		t.Fatalf("backup label %s = %q, want %q", PVRecreatedSuccesfullyLabelKey, got, LabelValueTrue)
	}
}

func TestProcessRemovedPVsSkipsForeignBackups(t *testing.T) {
	hostPath := rbdPV("pvc-host")
	hostPath.Spec.CSI = nil
	hostPath.Spec.HostPath = &v1.HostPathVolumeSource{Path: "/"}

	csiAndHostPath := rbdPV("pvc-both")
	csiAndHostPath.Spec.HostPath = &v1.HostPathVolumeSource{Path: "/"}

	cephfsAndLocal := rbdPV("pvc-local")
	cephfsAndLocal.Spec.CSI.Driver = "cephfs.csi.ceph.com"
	cephfsAndLocal.Spec.Local = &v1.LocalVolumeSource{Path: "/"}

	otherDriver := rbdPV("pvc-other")
	otherDriver.Spec.CSI.Driver = "hostpath.csi.k8s.io"

	misnamed := rbdPV("pvc-misnamed")

	noSource := rbdPV("pvc-none")
	noSource.Spec.CSI = nil

	undecodable := pendingBackup(t, testBackupDate+"-persistentvolume-pvc-garbage", rbdPV("pvc-garbage"))
	undecodable.Spec.Data = "not base64"

	noDate := pendingBackup(t, "-persistentvolume-pvc-nodate", rbdPV("pvc-nodate"))
	delete(noDate.Labels, BackupDateLabelKey)

	tests := []struct {
		name   string
		backup *v1alpha1.CephMetadataBackup
		pv     string
	}{
		{"hostPath volume", pendingBackup(t, testBackupDate+"-persistentvolume-pvc-host", hostPath), "pvc-host"},
		{"CSI volume with hostPath", pendingBackup(t, testBackupDate+"-persistentvolume-pvc-both", csiAndHostPath), "pvc-both"},
		{"CephFS volume with local path", pendingBackup(t, testBackupDate+"-persistentvolume-pvc-local", cephfsAndLocal), "pvc-local"},
		{"volume of another CSI driver", pendingBackup(t, testBackupDate+"-persistentvolume-pvc-other", otherDriver), "pvc-other"},
		{"name of another volume", pendingBackup(t, testBackupDate+"-persistentvolume-pvc-1", misnamed), "pvc-misnamed"},
		{"volume without a source", pendingBackup(t, testBackupDate+"-persistentvolume-pvc-none", noSource), "pvc-none"},
		{"undecodable data", undecodable, "pvc-garbage"},
		{"backup without a date", noDate, "pvc-nodate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cl := newTestClient(t, tt.backup)

			if err := ProcessRemovedPVs(ctx, cl, testBackupSource, "test"); err != nil {
				t.Fatalf("ProcessRemovedPVs returned %v, want the backup skipped", err)
			}
			if pvExists(t, cl, tt.pv) {
				t.Fatalf("PersistentVolume %s was created from a backup the hook did not make", tt.pv)
			}
			if got := recreatedLabel(t, cl, tt.backup.Name); got != LabelValueFalse {
				t.Fatalf("backup label %s = %q, want it left %q", PVRecreatedSuccesfullyLabelKey, got, LabelValueFalse)
			}
		})
	}
}

func TestProcessRemovedPVsRestoresValidBackupBetweenForeignOnes(t *testing.T) {
	ctx := context.Background()
	hostPathPV := func(name string) *v1.PersistentVolume {
		pv := rbdPV(name)
		pv.Spec.CSI = nil
		pv.Spec.HostPath = &v1.HostPathVolumeSource{Path: "/"}
		return pv
	}

	valid := testBackupDate + "-persistentvolume-pvc-m"
	cl := newTestClient(t,
		pendingBackup(t, testBackupDate+"-persistentvolume-pvc-a-host", hostPathPV("pvc-a-host")),
		pendingBackup(t, valid, rbdPV("pvc-m")),
		pendingBackup(t, testBackupDate+"-persistentvolume-pvc-z-host", hostPathPV("pvc-z-host")),
	)

	if err := ProcessRemovedPVs(ctx, cl, testBackupSource, "test"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pvc-a-host", "pvc-z-host"} {
		if pvExists(t, cl, name) {
			t.Fatalf("hostPath PersistentVolume %s was created", name)
		}
	}
	if !pvExists(t, cl, "pvc-m") {
		t.Fatal("PersistentVolume pvc-m was not restored")
	}
	if got := recreatedLabel(t, cl, valid); got != LabelValueTrue {
		t.Fatalf("backup label %s = %q, want %q", PVRecreatedSuccesfullyLabelKey, got, LabelValueTrue)
	}
}

func TestPendingPVBackupNames(t *testing.T) {
	ctx := context.Background()
	pending := pendingBackup(t, testBackupDate+"-persistentvolume-pvc-1", rbdPV("pvc-1"))

	recreated := pendingBackup(t, testBackupDate+"-persistentvolume-pvc-2", rbdPV("pvc-2"))
	recreated.Labels[PVRecreatedSuccesfullyLabelKey] = LabelValueTrue

	otherSource := pendingBackup(t, testBackupDate+"-persistentvolume-pvc-3", rbdPV("pvc-3"))
	otherSource.Labels[BackupSourceLabelKey] = "other"

	cl := newTestClient(t, pending, recreated, otherSource)

	names, err := PendingPVBackupNames(ctx, cl, testBackupSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != pending.Name {
		t.Fatalf("PendingPVBackupNames = %v, want [%s]", names, pending.Name)
	}
}

func TestMigratePVsToNewSecretWithoutControllerExpandSecret(t *testing.T) {
	ctx := context.Background()
	pv := rbdPV("pvc-1")
	pv.Spec.CSI.NodeStageSecretRef = &v1.SecretReference{Namespace: "d8-ceph-csi", Name: "csi-old"}
	cl := newTestClient(t, pv)

	pvList := &v1.PersistentVolumeList{}
	if err := cl.List(ctx, pvList); err != nil {
		t.Fatal(err)
	}
	if err := MigratePVsToNewSecret(ctx, cl, pvList, time.Now(), pv.Spec.StorageClassName, "csi-new", testBackupSource, "test"); err != nil {
		t.Fatal(err)
	}

	migrated := &v1.PersistentVolume{}
	if err := cl.Get(ctx, types.NamespacedName{Name: pv.Name}, migrated); err != nil {
		t.Fatalf("PersistentVolume was not recreated: %v", err)
	}
	want := v1.SecretReference{Namespace: ModuleNamespace, Name: "csi-new"}
	if ref := migrated.Spec.CSI.NodeStageSecretRef; ref == nil || *ref != want {
		t.Fatalf("NodeStageSecretRef = %v, want %v", ref, want)
	}
	if ref := migrated.Spec.CSI.ControllerExpandSecretRef; ref != nil {
		t.Fatalf("ControllerExpandSecretRef = %v, want none", ref)
	}
}
