/*
Copyright 2025 Flant JSC

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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/logger"
)

// EnsureConfigMapExists ensures that the ceph-csi-config ConfigMap exists in the given namespace.
// If it doesn't exist, creates an empty one with config.json containing an empty array.
func EnsureConfigMapExists(ctx context.Context, cl client.Client, namespace string, log logger.Logger) error {
	configMapName := internal.CSICephConfigMapName
	configMap := &corev1.ConfigMap{}

	err := cl.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: namespace}, configMap)
	if err == nil {
		log.Info(fmt.Sprintf("[EnsureConfigMapExists] ConfigMap %s/%s already exists", namespace, configMapName))
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("unable to check if ConfigMap %s/%s exists: %w", namespace, configMapName, err)
	}

	// ConfigMap doesn't exist, create an empty one
	emptyConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"config.json": "[]",
		},
	}

	err = cl.Create(ctx, emptyConfigMap)
	if err != nil {
		return fmt.Errorf("unable to create ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	log.Info(fmt.Sprintf("[EnsureConfigMapExists] successfully created empty ConfigMap %s/%s", namespace, configMapName))
	return nil
}
