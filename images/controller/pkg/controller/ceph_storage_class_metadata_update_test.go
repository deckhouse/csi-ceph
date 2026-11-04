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
	"maps"
	"testing"

	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/logger"
)

const (
	defaultClassAnnotationKey = "storageclass.kubernetes.io/is-default-class"
	foreignFinalizer          = "example.com/foreign"
)

type storageClassWrites struct {
	creates, updates, deletes int
}

// newStorageClassUpdateClient returns a fake client holding sc and counting the
// writes to StorageClasses.
func newStorageClassUpdateClient(t *testing.T, sc *v1.StorageClass) (client.Client, *storageClassWrites) {
	t.Helper()

	scheme := apiruntime.NewScheme()
	for _, add := range []func(*apiruntime.Scheme) error{clientgoscheme.AddToScheme, storagev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatalf("adding scheme: %v", err)
		}
	}

	writes := &storageClassWrites{}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sc).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1.StorageClass); ok {
				writes.creates++
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*v1.StorageClass); ok {
				writes.updates++
			}
			return c.Update(ctx, obj, opts...)
		},
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*v1.StorageClass); ok {
				writes.deletes++
			}
			return c.Delete(ctx, obj, opts...)
		},
	}).Build()

	return cl, writes
}

func TestReconcileStorageClassUpdateFuncMetadata(t *testing.T) {
	const (
		controllerNamespace = "d8-csi-ceph"
		clusterID           = "cluster-id"
	)
	ctx := context.Background()
	log := logger.NewNop()

	newCephSC := func(reclaimPolicy string, labels map[string]string) *storagev1alpha1.CephStorageClass {
		return &storagev1alpha1.CephStorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: "sc-1", Labels: labels},
			Spec: storagev1alpha1.CephStorageClassSpec{
				ClusterConnectionName: "cc-1",
				ReclaimPolicy:         reclaimPolicy,
				Type:                  storagev1alpha1.CephStorageClassTypeRBD,
				RBD: &storagev1alpha1.CephStorageClassRBD{
					DefaultFSType: "ext4",
					Pool:          "pool",
				},
			},
		}
	}

	// existingSC is the StorageClass a reconcile of cephSC has left, marked as
	// the default class and carrying a finalizer of someone else.
	existingSC := func(t *testing.T, cephSC *storagev1alpha1.CephStorageClass, labels map[string]string) *v1.StorageClass {
		t.Helper()
		sc, err := ConfigureStorageClass(cephSC, controllerNamespace, clusterID, nil)
		if err != nil {
			t.Fatalf("configuring the storage class: %v", err)
		}
		sc.Annotations[defaultClassAnnotationKey] = "true"
		sc.Finalizers = append(sc.Finalizers, foreignFinalizer)
		if labels != nil {
			sc.Labels = labels
		}
		return sc
	}

	reconcile := func(t *testing.T, cl client.Client, cephSC *storagev1alpha1.CephStorageClass) *v1.StorageClass {
		t.Helper()
		scList := &v1.StorageClassList{}
		if err := cl.List(ctx, scList); err != nil {
			t.Fatalf("listing storage classes: %v", err)
		}

		reconcileType, err := IdentifyReconcileFuncForStorageClass(log, scList, cephSC, controllerNamespace, clusterID, nil)
		if err != nil {
			t.Fatalf("identifying the reconcile func: %v", err)
		}
		if reconcileType != internal.UpdateReconcile {
			t.Fatalf("reconcile type = %q, want %q", reconcileType, internal.UpdateReconcile)
		}

		if _, _, err := reconcileStorageClassUpdateFunc(ctx, cl, log, scList, cephSC, controllerNamespace, clusterID, nil); err != nil {
			t.Fatalf("reconcileStorageClassUpdateFunc() = %v", err)
		}

		sc := &v1.StorageClass{}
		if err := cl.Get(ctx, client.ObjectKey{Name: cephSC.Name}, sc); err != nil {
			t.Fatalf("getting the storage class: %v", err)
		}

		scList = &v1.StorageClassList{}
		if err := cl.List(ctx, scList); err != nil {
			t.Fatalf("listing storage classes: %v", err)
		}
		if diff, err := GetSCDiff(&scList.Items[0], mustUpdateStorageClass(t, cephSC, &scList.Items[0], controllerNamespace, clusterID)); err != nil || diff != "" {
			t.Errorf("after the reconcile GetSCDiff() = %q, %v, want no difference", diff, err)
		}
		return sc
	}

	t.Run("a label difference, such as the managed-by value of an older release, is applied in place", func(t *testing.T) {
		cephSC := newCephSC("Delete", map[string]string{"team": "storage"})
		cl, writes := newStorageClassUpdateClient(t, existingSC(t, cephSC, map[string]string{
			internal.StorageManagedLabelKey: CephStorageClassCtrlName,
			"stale":                         "label",
		}))

		sc := reconcile(t, cl, cephSC)

		if writes.deletes != 0 || writes.creates != 0 || writes.updates != 1 {
			t.Errorf("writes = %+v, want exactly one update", *writes)
		}
		if got, want := sc.Labels, map[string]string{"team": "storage", internal.StorageManagedLabelKey: StorageClassManagedByValue}; !maps.Equal(got, want) {
			t.Errorf("labels = %v, want %v", got, want)
		}
		if sc.Annotations[defaultClassAnnotationKey] != "true" {
			t.Errorf("annotations = %v, the default-class mark is lost", sc.Annotations)
		}
		if len(sc.Finalizers) != 2 || sc.Finalizers[0] != CephStorageClassControllerFinalizerName || sc.Finalizers[1] != foreignFinalizer {
			t.Errorf("finalizers = %v, want both the own and the foreign one", sc.Finalizers)
		}
	})

	t.Run("a missing own finalizer is restored in place", func(t *testing.T) {
		cephSC := newCephSC("Delete", nil)
		existing := existingSC(t, cephSC, nil)
		existing.Finalizers = []string{foreignFinalizer}
		cl, writes := newStorageClassUpdateClient(t, existing)

		sc := reconcile(t, cl, cephSC)

		if writes.deletes != 0 || writes.creates != 0 || writes.updates != 1 {
			t.Errorf("writes = %+v, want exactly one update", *writes)
		}
		if len(sc.Finalizers) != 2 || sc.Finalizers[0] != foreignFinalizer || sc.Finalizers[1] != CephStorageClassControllerFinalizerName {
			t.Errorf("finalizers = %v, want the foreign one and the own one", sc.Finalizers)
		}
	})

	t.Run("a spec difference recreates the class", func(t *testing.T) {
		cephSC := newCephSC("Retain", nil)
		existing := existingSC(t, newCephSC("Delete", nil), nil)
		existing.Finalizers = []string{CephStorageClassControllerFinalizerName}
		cl, writes := newStorageClassUpdateClient(t, existing)

		sc := reconcile(t, cl, cephSC)

		if writes.deletes != 1 || writes.creates != 1 {
			t.Errorf("writes = %+v, want one delete and one create", *writes)
		}
		if *sc.ReclaimPolicy != "Retain" {
			t.Errorf("reclaimPolicy = %q, want Retain", *sc.ReclaimPolicy)
		}
	})

	t.Run("a foreign finalizer alone is no difference", func(t *testing.T) {
		cephSC := newCephSC("Delete", nil)
		existing := existingSC(t, cephSC, nil)

		newSC := mustUpdateStorageClass(t, cephSC, existing, controllerNamespace, clusterID)
		if diff, err := GetSCDiff(existing, newSC); err != nil || diff != "" {
			t.Errorf("GetSCDiff() = %q, %v, want no difference", diff, err)
		}
	})
}

func mustUpdateStorageClass(t *testing.T, cephSC *storagev1alpha1.CephStorageClass, oldSC *v1.StorageClass, controllerNamespace, clusterID string) *v1.StorageClass {
	t.Helper()
	newSC, err := updateStorageClass(cephSC, oldSC, controllerNamespace, clusterID, nil)
	if err != nil {
		t.Fatalf("building the storage class: %v", err)
	}
	return newSC
}
