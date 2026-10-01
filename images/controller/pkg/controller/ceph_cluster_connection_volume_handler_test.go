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
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func TestPersistentVolumeConnectionHandler(t *testing.T) {
	newPV := func(phase corev1.PersistentVolumePhase) *corev1.PersistentVolume {
		return &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pv",
				Annotations: map[string]string{
					provisionerDeletionSecretNameAnnotation:      "csi-ceph-secret-for-a",
					provisionerDeletionSecretNamespaceAnnotation: testSecretNamespace,
				},
			},
			Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				ControllerPublishSecretRef: &corev1.SecretReference{Name: "csi-ceph-secret-for-a", Namespace: testSecretNamespace},
				NodeStageSecretRef:         &corev1.SecretReference{Name: "csi-ceph-secret-for-b", Namespace: testSecretNamespace},
				NodePublishSecretRef:       &corev1.SecretReference{Name: "csi-ceph-secret-for-c", Namespace: "elsewhere"},
				ControllerExpandSecretRef:  &corev1.SecretReference{Name: "some-other-secret", Namespace: testSecretNamespace},
			}}},
			Status: corev1.PersistentVolumeStatus{Phase: phase},
		}
	}
	h := persistentVolumeConnectionHandler(testSecretNamespace)

	tests := []struct {
		name  string
		fire  func(q workqueue.TypedRateLimitingInterface[reconcile.Request])
		wants []string
	}{
		{
			name: "phase change",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Update(context.Background(), event.TypedUpdateEvent[*corev1.PersistentVolume]{ObjectOld: newPV(corev1.VolumeBound), ObjectNew: newPV(corev1.VolumeReleased)}, q)
			},
			wants: []string{"a", "b"},
		},
		{
			name: "update in the same phase",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Update(context.Background(), event.TypedUpdateEvent[*corev1.PersistentVolume]{ObjectOld: newPV(corev1.VolumeBound), ObjectNew: newPV(corev1.VolumeBound)}, q)
			},
		},
		{
			name: "delete",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Delete(context.Background(), event.TypedDeleteEvent[*corev1.PersistentVolume]{Object: newPV(corev1.VolumeReleased)}, q)
			},
			wants: []string{"a", "b"},
		},
		{
			name: "create",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Create(context.Background(), event.TypedCreateEvent[*corev1.PersistentVolume]{Object: newPV(corev1.VolumeBound)}, q)
			},
		},
		{
			name: "delete of a volume of another driver",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h.Delete(context.Background(), event.TypedDeleteEvent[*corev1.PersistentVolume]{Object: &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "other"}}}, q)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()

			tt.fire(q)

			var got []string
			for q.Len() > 0 {
				item, _ := q.Get()
				got = append(got, item.Name)
				q.Done(item)
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.wants) {
				t.Errorf("queued the CephClusterConnections %v, want %v", got, tt.wants)
			}
		})
	}
}
