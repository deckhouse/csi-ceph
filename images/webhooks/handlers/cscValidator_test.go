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

package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/slok/kubewebhook/v2/pkg/model"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCSCValidate(t *testing.T) {
	csc := &metav1.ObjectMeta{Name: "ceph-rbd"}
	storageClass := func(provisioner string) *storagev1.StorageClass {
		return &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "ceph-rbd"}, Provisioner: provisioner}
	}

	tests := []struct {
		name      string
		operation model.AdmissionReviewOp
		existing  []runtime.Object
		getErr    error
		wantValid bool
		wantErr   bool
		wantMsg   string
	}{
		{name: "no StorageClass with the name", operation: model.OperationCreate, wantValid: true},
		{name: "StorageClass of the rbd provisioner", operation: model.OperationCreate, existing: []runtime.Object{storageClass("rbd.csi.ceph.com")}, wantValid: true},
		{name: "StorageClass of the cephfs provisioner", operation: model.OperationCreate, existing: []runtime.Object{storageClass("cephfs.csi.ceph.com")}, wantValid: true},
		{name: "StorageClass of another provisioner", operation: model.OperationCreate, existing: []runtime.Object{storageClass("nfs.csi.k8s.io")}, wantMsg: `StorageClass "ceph-rbd" already exists with provisioner "nfs.csi.k8s.io"`},
		{name: "update is not checked", operation: model.OperationUpdate, existing: []runtime.Object{storageClass("nfs.csi.k8s.io")}, wantValid: true},
		{name: "the StorageClass cannot be read", operation: model.OperationCreate, getErr: errors.New("forbidden"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cl := fake.NewSimpleClientset(tt.existing...)
			if tt.getErr != nil {
				cl.PrependReactor("get", "storageclasses", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, tt.getErr
				})
			}

			res, err := CSCValidate(cl)(context.Background(), &model.AdmissionReview{Operation: tt.operation}, csc)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantMsg != "" {
				if res.Valid || !strings.Contains(res.Message, tt.wantMsg) {
					t.Fatalf("expected a rejection containing %q, got %+v", tt.wantMsg, res)
				}
				return
			}
			if res.Valid != tt.wantValid {
				t.Fatalf("expected valid=%v, got %+v", tt.wantValid, res)
			}
		})
	}
}
