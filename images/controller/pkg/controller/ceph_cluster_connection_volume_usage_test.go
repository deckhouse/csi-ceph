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

package controller

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
)

const (
	testSecretNamespace = "d8-csi-ceph"
	testSecretName      = "csi-ceph-secret-for-conn"
)

func TestPVsNeedingSecret(t *testing.T) {
	ref := &corev1.SecretReference{Name: testSecretName, Namespace: testSecretNamespace}
	withCSI := func(set func(csi *corev1.CSIPersistentVolumeSource)) corev1.PersistentVolume {
		pv := corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv"},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{Driver: CephStorageClassRBDProvisioner}},
			},
		}
		set(pv.Spec.CSI)
		return pv
	}

	released := withCSI(func(csi *corev1.CSIPersistentVolumeSource) { csi.NodeStageSecretRef = ref })
	released.Status.Phase = corev1.VolumeReleased
	released.Spec.PersistentVolumeReclaimPolicy = corev1.PersistentVolumeReclaimRetain

	tests := []struct {
		name     string
		pv       corev1.PersistentVolume
		attached bool
		want     bool
	}{
		{name: "controller-publish secret", pv: withCSI(func(csi *corev1.CSIPersistentVolumeSource) { csi.ControllerPublishSecretRef = ref }), want: true},
		{name: "node-stage secret", pv: withCSI(func(csi *corev1.CSIPersistentVolumeSource) { csi.NodeStageSecretRef = ref }), want: true},
		{name: "node-publish secret", pv: withCSI(func(csi *corev1.CSIPersistentVolumeSource) { csi.NodePublishSecretRef = ref }), want: true},
		{name: "controller-expand secret", pv: withCSI(func(csi *corev1.CSIPersistentVolumeSource) { csi.ControllerExpandSecretRef = ref }), want: true},
		{name: "node-expand secret", pv: withCSI(func(csi *corev1.CSIPersistentVolumeSource) { csi.NodeExpandSecretRef = ref }), want: true},
		{
			name: "deletion secret annotations",
			pv: corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv", Annotations: map[string]string{
				provisionerDeletionSecretNameAnnotation:      testSecretName,
				provisionerDeletionSecretNamespaceAnnotation: testSecretNamespace,
			}}},
			want: true,
		},
		{
			name: "deletion secret annotations in another namespace",
			pv: corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv", Annotations: map[string]string{
				provisionerDeletionSecretNameAnnotation:      testSecretName,
				provisionerDeletionSecretNamespaceAnnotation: "other",
			}}},
		},
		{
			name: "same name in another namespace",
			pv: withCSI(func(csi *corev1.CSIPersistentVolumeSource) {
				csi.NodeStageSecretRef = &corev1.SecretReference{Name: testSecretName, Namespace: "other"}
			}),
		},
		{
			name: "another secret",
			pv: withCSI(func(csi *corev1.CSIPersistentVolumeSource) {
				csi.NodeStageSecretRef = &corev1.SecretReference{Name: "csi-ceph-secret-for-other", Namespace: testSecretNamespace}
			}),
		},
		{name: "not a CSI volume", pv: corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv"}}},
		{name: "Released and kept", pv: released},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := volumeUsage{pvs: []corev1.PersistentVolume{tt.pv}, attached: map[string]bool{tt.pv.Name: tt.attached}}
			got := len(usage.pvsNeedingSecret(testSecretNamespace, testSecretName)) > 0
			if got != tt.want {
				t.Errorf("pvsNeedingSecret() lists the PV: %t, want %t", got, tt.want)
			}
		})
	}
}

func TestReconcileSecretListsAtMostTenVolumes(t *testing.T) {
	usage := volumeUsage{}
	for i := range maxListedNames + 1 {
		usage.pvs = append(usage.pvs, corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pv-%02d", i)},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				NodeStageSecretRef: &corev1.SecretReference{Name: testSecretName, Namespace: testSecretNamespace},
			}}},
		})
	}
	now := metav1.Now()
	connection := &storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: "conn", DeletionTimestamp: &now}}

	shouldRequeue, msg, err := reconcileSecret(context.Background(), nil, logger.NewNop(), &corev1.SecretList{}, connection, usage, testSecretNamespace, testSecretName)
	if err == nil || !shouldRequeue {
		t.Fatalf("reconcileSecret() = %t, %v; want a requeue with an error", shouldRequeue, err)
	}
	if !strings.Contains(msg, "used by 11 PersistentVolumes") {
		t.Errorf("message %q should count all 11 PersistentVolumes", msg)
	}
	if !strings.Contains(msg, "pv-09") || strings.Contains(msg, "pv-10") {
		t.Errorf("message %q should name the first %d PersistentVolumes only", msg, maxListedNames)
	}
}

func TestVolumesUsingConfigCephFS(t *testing.T) {
	newPV := func(name string, attributes map[string]string) corev1.PersistentVolume {
		attributes["clusterID"] = "cluster"
		return corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver:             CephStorageClassCephFSProvisioner,
				VolumeAttributes:   attributes,
				NodeStageSecretRef: &corev1.SecretReference{Name: "csi-ceph-secret-for-" + name, Namespace: testSecretNamespace},
			}}},
		}
	}

	pvs := []corev1.PersistentVolume{
		newPV("with-path", map[string]string{"subvolumePath": "/volumes/group-a/csi-vol-1/0b6e1c1e"}),
		newPV("without-path", map[string]string{}),
		newPV("unreadable-path", map[string]string{"subvolumePath": "/somewhere/else"}),
		newPV("static", map[string]string{"staticVolume": "true", "rootPath": "/volumes/group-b/x"}),
	}

	got := volumesUsingConfig(volumeUsage{pvs: pvs}, "cluster", storagev1alpha1.CephStorageClassTypeCephFS, testSecretNamespace)

	if want := []string{"group-a"}; !slices.Equal(slices.Sorted(maps.Keys(got.subvolumeGroups)), want) {
		t.Errorf("subvolume groups = %v, want %v", slices.Sorted(maps.Keys(got.subvolumeGroups)), want)
	}
	if want := []string{"unreadable-path", "without-path"}; !slices.Equal(slices.Sorted(maps.Keys(got.connections)), want) {
		t.Errorf("connections = %v, want %v", slices.Sorted(maps.Keys(got.connections)), want)
	}
}

func TestSubvolumeGroupOfPath(t *testing.T) {
	tests := map[string]struct {
		group string
		ok    bool
	}{
		"/volumes/csi/csi-vol-1/0b6e1c1e": {group: "csi", ok: true},
		"/volumes/_nogroup/sub/0b6e1c1e":  {group: "_nogroup", ok: true},
		"volumes/csi/csi-vol-1":           {group: "csi", ok: true},
		"/volumes/csi":                    {},
		"/volumes//csi-vol-1/0b6e1c1e":    {},
		"/other/csi/csi-vol-1/0b6e1c1e":   {},
		"":                                {},
	}

	for path, want := range tests {
		group, ok := subvolumeGroupOfPath(path)
		if group != want.group || ok != want.ok {
			t.Errorf("subvolumeGroupOfPath(%q) = %q, %t; want %q, %t", path, group, ok, want.group, want.ok)
		}
	}
}

func TestDeletionReadsVolumesPastTheCache(t *testing.T) {
	ctx := context.Background()

	scheme := snapshotUsageScheme(t)

	now := metav1.Now()
	objects := func() []client.Object {
		return []client.Object{
			&storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{
				Name:              "conn",
				DeletionTimestamp: &now,
				Finalizers:        []string{CephClusterConnectionControllerFinalizerName},
			}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
				Name:       testSecretName,
				Namespace:  testSecretNamespace,
				Finalizers: []string{CephClusterConnectionControllerFinalizerName},
			}},
			&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: internal.CSICephConfigMapName, Namespace: testSecretNamespace},
				Data:       map[string]string{"config.json": "[]"},
			},
		}
	}
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects()...).Build()
	live := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects(), &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-new"},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
			ControllerPublishSecretRef: &corev1.SecretReference{Name: testSecretName, Namespace: testSecretNamespace},
		}}},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	})...).Build()

	connection := &storagev1alpha1.CephClusterConnection{}
	if err := cached.Get(ctx, client.ObjectKey{Name: "conn"}, connection); err != nil {
		t.Fatalf("reading the connection: %v", err)
	}

	shouldRequeue, _, _, err := reconcileCephClusterConnection(ctx, cached, live, logger.NewNop(), connection, testSecretNamespace)
	if !shouldRequeue || err == nil || !strings.Contains(err.Error(), "used by 1 PersistentVolumes [pv-new]") {
		t.Fatalf("reconcile error = %v, want the secret held by pv-new, which only the live reader sees", err)
	}
	if err := cached.Get(ctx, client.ObjectKey{Namespace: testSecretNamespace, Name: testSecretName}, &corev1.Secret{}); err != nil {
		t.Errorf("the secret must stay while pv-new needs it: %v", err)
	}
}

func TestReadyReasonOfConnectionHeldByVolumes(t *testing.T) {
	ctx := context.Background()
	now := metav1.Now()
	connection := &storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: "conn", DeletionTimestamp: &now, Finalizers: []string{CephClusterConnectionControllerFinalizerName}}}
	cl := newStatusTestClient(t, connection)
	usage := volumeUsage{pvs: []corev1.PersistentVolume{{
		ObjectMeta: metav1.ObjectMeta{Name: "pv"},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
			NodeStageSecretRef: &corev1.SecretReference{Name: testSecretName, Namespace: testSecretNamespace},
		}}},
	}}}

	_, msg, err := reconcileSecret(ctx, nil, logger.NewNop(), &corev1.SecretList{}, connection, usage, testSecretNamespace, testSecretName)
	if err == nil {
		t.Fatal("reconcileSecret() should hold the connection")
	}
	if err := updateCephClusterConnectionStatus(ctx, cl, connection, err, msg); err != nil {
		t.Fatalf("updating status: %v", err)
	}

	got := &storagev1alpha1.CephClusterConnection{}
	if err := cl.Get(ctx, client.ObjectKey{Name: "conn"}, got); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	cond := conditions.Get(got.Status.Conditions, storagev1alpha1.ConditionTypeReady)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != storagev1alpha1.ReasonPersistentVolumesExist || !strings.Contains(cond.Message, "[pv]") {
		t.Fatalf("Ready = %+v, want False with %s naming the PersistentVolume", cond, storagev1alpha1.ReasonPersistentVolumesExist)
	}
}

// unreadableReader fails every read, so a test can tell that it was not used.
type unreadableReader struct{ client.Reader }

func (unreadableReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return fmt.Errorf("the live reader must not be used")
}

func TestDeletionHeldByTheCacheSkipsTheLiveRead(t *testing.T) {
	ctx := context.Background()

	scheme := snapshotUsageScheme(t)

	now := metav1.Now()
	cached := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&storagev1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{
			Name:              "conn",
			DeletionTimestamp: &now,
			Finalizers:        []string{CephClusterConnectionControllerFinalizerName},
		}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Name:       testSecretName,
			Namespace:  testSecretNamespace,
			Finalizers: []string{CephClusterConnectionControllerFinalizerName},
		}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: internal.CSICephConfigMapName, Namespace: testSecretNamespace},
			Data:       map[string]string{"config.json": "[]"},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-cached"},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				ControllerPublishSecretRef: &corev1.SecretReference{Name: testSecretName, Namespace: testSecretNamespace},
			}}},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		},
	).Build()

	connection := &storagev1alpha1.CephClusterConnection{}
	if err := cached.Get(ctx, client.ObjectKey{Name: "conn"}, connection); err != nil {
		t.Fatalf("reading the connection: %v", err)
	}

	shouldRequeue, _, _, err := reconcileCephClusterConnection(ctx, cached, unreadableReader{}, logger.NewNop(), connection, testSecretNamespace)
	if !shouldRequeue || err == nil || !strings.Contains(err.Error(), "used by 1 PersistentVolumes [pv-cached]") {
		t.Fatalf("reconcile error = %v, want the secret held by pv-cached without a live read", err)
	}
}
