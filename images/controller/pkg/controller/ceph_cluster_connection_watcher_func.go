/*
Copyright 2024 Flant JSC

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
	v1alpha1 "d8-controller/api/v1alpha1"
	"d8-controller/pkg/internal"
	"d8-controller/pkg/logger"
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func validateCephClusterConnectionSpec(cephClusterConnection *v1alpha1.CephClusterConnection) (bool, string) {
	if cephClusterConnection.DeletionTimestamp != nil {
		return true, ""
	}

	var (
		failedMsgBuilder strings.Builder
		validationPassed = true
	)

	failedMsgBuilder.WriteString("Validation of CeohClusterConnection failed: ")

	if cephClusterConnection.Spec.ClusterID == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.clusterID field is empty; ")
	}

	if cephClusterConnection.Spec.Monitors == nil {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.monitors field is empty; ")
	}

	if cephClusterConnection.Spec.UserID == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.userID field is empty; ")
	}

	if cephClusterConnection.Spec.UserKey == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.userKey field is empty; ")
	}

	return validationPassed, failedMsgBuilder.String()
}

func IdentifyReconcileFuncForSecret(log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (reconcileType string, err error) {
	if shouldReconcileByDeleteFunc(cephClusterConnection) {
		return internal.DeleteReconcile, nil
	}

	if shouldReconcileSecretByCreateFunc(secretList, cephClusterConnection, secretName) {
		return internal.CreateReconcile, nil
	}

	should, err := shouldReconcileSecretByUpdateFunc(log, secretList, cephClusterConnection, controllerNamespace, secretName)
	if err != nil {
		return "", err
	}
	if should {
		return internal.UpdateReconcile, nil
	}

	return "", nil
}

func shouldReconcileSecretByCreateFunc(secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, secretName string) bool {
	if cephClusterConnection.DeletionTimestamp != nil {
		return false
	}

	for _, s := range secretList.Items {
		if s.Name == secretName {
			return false
		}
	}

	return true
}

func shouldReconcileSecretByUpdateFunc(log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (bool, error) {
	if cephClusterConnection.DeletionTimestamp != nil {
		return false, nil
	}

	secretSelector := labels.Set(map[string]string{
		internal.StorageManagedLabelKey: CephClusterConnectionCtrlName,
	})

	for _, oldSecret := range secretList.Items {
		if oldSecret.Name == secretName {
			newSecret := configureSecret(cephClusterConnection, controllerNamespace, secretName)
			equal := areSecretsEqual(&oldSecret, newSecret)
			if !equal {
				log.Debug(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] a secret %s should be updated", secretName))
				if !labels.Set(oldSecret.Labels).AsSelector().Matches(secretSelector) {
					err := fmt.Errorf("a secret %q does not have a label %s=%s", oldSecret.Name, internal.StorageManagedLabelKey, CephClusterConnectionCtrlName)
					return false, err
				}
				return true, nil
			}

			if !labels.Set(oldSecret.Labels).AsSelector().Matches(secretSelector) {
				log.Debug(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] a secret %s should be updated. The label %s=%s is missing", oldSecret.Name, internal.StorageManagedLabelKey, CephClusterConnectionCtrlName))
				return true, nil
			}

			return false, nil
		}
	}
	err := fmt.Errorf("[shouldReconcileSecretByUpdateFunc] a secret %s not found in the list: %+v. It should be created", secretName, secretList.Items)
	return false, err
}

func configureSecret(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) *corev1.Secret {
	userID := cephClusterConnection.Spec.UserID
	userKey := cephClusterConnection.Spec.UserKey
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: controllerNamespace,
			Labels: map[string]string{
				internal.StorageManagedLabelKey: CephClusterConnectionCtrlName,
			},
			Finalizers: []string{CephClusterConnectionControllerFinalizerName},
		},
		StringData: map[string]string{
			// Credentials for RBD
			"userID":  userID,
			"userKey": userKey,

			// Credentials for CephFS
			"adminID":  userID,
			"adminKey": userKey,
		},
	}

	return secret
}

func areSecretsEqual(old, new *corev1.Secret) bool {
	if reflect.DeepEqual(old.StringData, new.StringData) && reflect.DeepEqual(old.Labels, new.Labels) {
		return true
	}

	return false
}

func reconcileSecretCreateFunc(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretCreateFunc] starts reconciliation of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

	newSecret := configureSecret(cephClusterConnection, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("[reconcileSecretCreateFunc] successfully configurated secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name))
	log.Trace(fmt.Sprintf("[reconcileSecretCreateFunc] secret: %+v", newSecret))

	err = cl.Create(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[reconcileSecretCreateFunc] unable to create a Secret %s for CephClusterConnection %s: %w", newSecret.Name, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	return false, "Successfully created", nil
}

func reconcileSecretUpdateFunc(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] starts reconciliation of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

	var oldSecret *corev1.Secret
	for _, s := range secretList.Items {
		if s.Name == secretName {
			oldSecret = &s
			break
		}
	}

	if oldSecret == nil {
		err := fmt.Errorf("[reconcileSecretUpdateFunc] unable to find a secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] secret %s was found for the CephClusterConnection %s", secretName, cephClusterConnection.Name))

	newSecret := configureSecret(cephClusterConnection, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] successfully configurated new secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name))
	log.Trace(fmt.Sprintf("[reconcileSecretUpdateFunc] new secret: %+v", newSecret))
	log.Trace(fmt.Sprintf("[reconcileSecretUpdateFunc] old secret: %+v", oldSecret))

	err = cl.Update(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[reconcileSecretUpdateFunc] unable to update the Secret %s for CephClusterConnection %s: %w", newSecret.Name, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	log.Info(fmt.Sprintf("[reconcileSecretUpdateFunc] successfully updated the Secret %s for the CephClusterConnection %s", newSecret.Name, cephClusterConnection.Name))

	return false, "Successfully updated", nil
}

func reconcileSecretDeleteFunc(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, secretName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretDeleteFunc] starts reconciliation of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

	var secret *corev1.Secret
	for _, s := range secretList.Items {
		if s.Name == secretName {
			secret = &s
			break
		}
	}

	if secret == nil {
		log.Info(fmt.Sprintf("[reconcileSecretDeleteFunc] no secret with name %s found for the CephClusterConnection %s", secretName, cephClusterConnection.Name))
	}

	if secret != nil {
		log.Info(fmt.Sprintf("[reconcileSecretDeleteFunc] successfully found a secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name))
		err = deleteSecret(ctx, cl, secret)

		if err != nil {
			err = fmt.Errorf("[reconcileSecretDeleteFunc] unable to delete the Secret %s for the CephCluster %s: %w", secret.Name, cephClusterConnection.Name, err)
			return true, err.Error(), err
		}
	}

	log.Info(fmt.Sprintf("[reconcileSecretDeleteFunc] ends reconciliation of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

	return false, "", nil
}

func updateCephClusterConnectionPhase(ctx context.Context, cl client.Client, cephClusterConnection *v1alpha1.CephClusterConnection, phase, reason string) error {
	if cephClusterConnection.Status == nil {
		cephClusterConnection.Status = &v1alpha1.CephClusterConnectionStatus{}
	}
	cephClusterConnection.Status.Phase = phase
	cephClusterConnection.Status.Reason = reason

	err := cl.Status().Update(ctx, cephClusterConnection)
	if err != nil {
		return err
	}

	return nil
}

func deleteSecret(ctx context.Context, cl client.Client, secret *corev1.Secret) error {
	_, err := removeFinalizerIfExists(ctx, cl, secret, CephClusterConnectionControllerFinalizerName)
	if err != nil {
		return err
	}

	err = cl.Delete(ctx, secret)
	if err != nil {
		return err
	}

	return nil
}
