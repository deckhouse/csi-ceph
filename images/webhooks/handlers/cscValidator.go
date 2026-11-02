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
	"fmt"

	"github.com/slok/kubewebhook/v2/pkg/model"
	kwhvalidating "github.com/slok/kubewebhook/v2/pkg/webhook/validating"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// CSCValidate rejects a new CephStorageClass whose name is already taken by a
// StorageClass of another provisioner. The controller names the StorageClass
// after the CephStorageClass, so such a resource could only ever be Failed.
func CSCValidate(cl kubernetes.Interface) func(ctx context.Context, arReview *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
	return func(ctx context.Context, arReview *model.AdmissionReview, obj metav1.Object) (*kwhvalidating.ValidatorResult, error) {
		if arReview.Operation != model.OperationCreate {
			return &kwhvalidating.ValidatorResult{Valid: true}, nil
		}

		sc, err := cl.StorageV1().StorageClasses().Get(ctx, obj.GetName(), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return &kwhvalidating.ValidatorResult{Valid: true}, nil
		}
		if err != nil {
			return nil, fmt.Errorf("unable to get the StorageClass %s: %w", obj.GetName(), err)
		}

		if contains(managedStorageClassProvisioners, sc.Provisioner) {
			return &kwhvalidating.ValidatorResult{Valid: true}, nil
		}

		return &kwhvalidating.ValidatorResult{
			Valid:   false,
			Message: fmt.Sprintf("StorageClass %q already exists with provisioner %q; choose a different CephStorageClass name", sc.Name, sc.Provisioner),
		}, nil
	}
}
