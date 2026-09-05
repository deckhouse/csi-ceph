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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/sds-common-lib/fsreserve"
)

// An update event that is dropped here never reaches the reconciler, so
// whatever it changed never reaches the StorageClass either.
func TestShouldEnqueueCephStorageClassUpdate(t *testing.T) {
	newSC := func(mutate func(*storagev1alpha1.CephStorageClass)) *storagev1alpha1.CephStorageClass {
		sc := &storagev1alpha1.CephStorageClass{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "ceph-sc",
				Labels:      map[string]string{"heritage": "deckhouse"},
				Annotations: map[string]string{},
			},
			Spec: storagev1alpha1.CephStorageClassSpec{
				Type:                  storagev1alpha1.CephStorageClassTypeRBD,
				ClusterConnectionName: "conn",
				RBD:                   &storagev1alpha1.CephStorageClassRBD{DefaultFSType: "ext4", Pool: "pool"},
			},
		}
		if mutate != nil {
			mutate(sc)
		}
		return sc
	}

	tests := []struct {
		name    string
		mutate  func(*storagev1alpha1.CephStorageClass)
		enqueue bool
	}{
		{
			name:    "nothing changed",
			mutate:  nil,
			enqueue: false,
		},
		{
			// The one this test exists for: the reserve is read from the
			// annotations and ends up in the StorageClass parameters, so an
			// annotation-only edit has to be reconciled. A consumer that
			// annotates a class it created earlier - sds-elastic does, from an
			// ElasticStorageClass - depends on exactly this event.
			name: "the reserve annotation is added to an existing class",
			mutate: func(sc *storagev1alpha1.CephStorageClass) {
				sc.Annotations[fsreserve.AnnotationKey] = "5"
			},
			enqueue: true,
		},
		{
			name: "the reserve annotation is taken off",
			mutate: func(sc *storagev1alpha1.CephStorageClass) {
				delete(sc.Annotations, fsreserve.AnnotationKey)
			},
			enqueue: false, // the old object below carries no annotation either
		},
		{
			name: "the spec changed",
			mutate: func(sc *storagev1alpha1.CephStorageClass) {
				sc.Spec.RBD.Pool = "other-pool"
			},
			enqueue: true,
		},
		{
			name: "a label changed",
			mutate: func(sc *storagev1alpha1.CephStorageClass) {
				sc.Labels["heritage"] = "someone-else"
			},
			enqueue: true,
		},
		{
			name: "the class is being deleted",
			mutate: func(sc *storagev1alpha1.CephStorageClass) {
				now := metav1.Now()
				sc.DeletionTimestamp = &now
			},
			enqueue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldSC := newSC(nil)
			newSCObj := newSC(tt.mutate)

			if got := shouldEnqueueCephStorageClassUpdate(oldSC, newSCObj); got != tt.enqueue {
				t.Errorf("shouldEnqueueCephStorageClassUpdate() = %v, want %v", got, tt.enqueue)
			}
		})
	}

	t.Run("the reserve annotation is cleared from a class that had it", func(t *testing.T) {
		oldSC := newSC(func(sc *storagev1alpha1.CephStorageClass) {
			sc.Annotations[fsreserve.AnnotationKey] = "5"
		})
		newSCObj := newSC(nil)

		if !shouldEnqueueCephStorageClassUpdate(oldSC, newSCObj) {
			t.Error("clearing the reserve annotation must reach the reconciler: the parameter has to go with it")
		}
	})
}
