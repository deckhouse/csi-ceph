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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
)

func TestCephStorageClassConnectionHandler(t *testing.T) {
	newSC := func(connectionName string) *storagev1alpha1.CephStorageClass {
		return &storagev1alpha1.CephStorageClass{
			ObjectMeta: metav1.ObjectMeta{Name: "ceph-sc"},
			Spec: storagev1alpha1.CephStorageClassSpec{
				Type:                  storagev1alpha1.CephStorageClassTypeRBD,
				ClusterConnectionName: connectionName,
			},
		}
	}

	tests := []struct {
		name  string
		fire  func(q workqueue.TypedRateLimitingInterface[reconcile.Request])
		wants []string
	}{
		{
			name: "create",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				cephStorageClassConnectionHandler().Create(context.Background(), event.TypedCreateEvent[*storagev1alpha1.CephStorageClass]{Object: newSC("conn")}, q)
			},
			wants: []string{"conn"},
		},
		{
			name: "delete",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				cephStorageClassConnectionHandler().Delete(context.Background(), event.TypedDeleteEvent[*storagev1alpha1.CephStorageClass]{Object: newSC("conn")}, q)
			},
			wants: []string{"conn"},
		},
		{
			name: "update",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				cephStorageClassConnectionHandler().Update(context.Background(), event.TypedUpdateEvent[*storagev1alpha1.CephStorageClass]{ObjectOld: newSC("conn"), ObjectNew: newSC("conn")}, q)
			},
		},
		{
			name: "create without a connection",
			fire: func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				cephStorageClassConnectionHandler().Create(context.Background(), event.TypedCreateEvent[*storagev1alpha1.CephStorageClass]{Object: newSC("")}, q)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()

			tt.fire(q)

			if q.Len() != len(tt.wants) {
				t.Fatalf("queue length = %d, want %d", q.Len(), len(tt.wants))
			}
			for _, want := range tt.wants {
				got, _ := q.Get()
				if got != (reconcile.Request{NamespacedName: types.NamespacedName{Name: want}}) {
					t.Errorf("queued %v, want the CephClusterConnection %q", got, want)
				}
				q.Done(got)
			}
		})
	}
}
