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
	"d8-controller/pkg/logger"
	"errors"
	"fmt"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func validateCephClusterConnectionSpec(cephClusterConnection *v1alpha1.CephClusterConnection) error {
	if cephClusterConnection.Spec.ClusterID == "" {
		return fmt.Errorf("[validateCephClusterConnectionSpec] %s: spec.clusterID is required", cephClusterConnection.Name)
	}

	if cephClusterConnection.Spec.Monitors == nil {
		return fmt.Errorf("[validateCephClusterConnectionSpec] %s: spec.monitors is required", cephClusterConnection.Name)
	}

	if cephClusterConnection.Spec.UserID == "" {
		return fmt.Errorf("[validateCephClusterConnectionSpec] %s: spec.userID is required", cephClusterConnection.Name)
	}

	if cephClusterConnection.Spec.UserKey == "" {
		return fmt.Errorf("[validateCephClusterConnectionSpec] %s: spec.userKey is required", cephClusterConnection.Name)
	}

	return nil
}

func IdentifyReconcileFuncForSecret(log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (reconcileType string, err error) {
	if shouldReconcileByDeleteFunc(cephClusterConnection) {
		return DeleteReconcile, nil
	}

	if shouldReconcileSecretByCreateFunc(secretList, cephClusterConnection, secretName) {
		return CreateReconcile, nil
	}

	should, err := shouldReconcileSecretByUpdateFunc(log, secretList, cephClusterConnection, controllerNamespace, secretName)
	if err != nil {
		return "", err
	}
	if should {
		return UpdateReconcile, nil
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
		StorageManagedLabelKey: CephClusterConnectionCtrlName,
	})

	for _, oldSecret := range secretList.Items {
		if oldSecret.Name == secretName {
			newSecret := configureSecret(cephClusterConnection, controllerNamespace, secretName)
			equal := areSecretsEqual(&oldSecret, newSecret)
			if !equal {
				log.Debug(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] a secret %s should be updated", secretName))
				if !labels.Set(oldSecret.Labels).AsSelector().Matches(secretSelector) {
					err := fmt.Errorf("a secret %q does not have a label %s=%s", oldSecret.Name, StorageManagedLabelKey, CephClusterConnectionCtrlName)
					return false, err
				}
				return true, nil
			}

			if !labels.Set(oldSecret.Labels).AsSelector().Matches(secretSelector) {
				log.Debug(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] a secret %s should be updated. The label %s=%s is missing", oldSecret.Name, StorageManagedLabelKey, CephClusterConnectionCtrlName))
				return true, nil
			}

			return false, nil
		}
	}

	log.Debug(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] a secret %s not found in the list: %+v. It should be created", secretName, secretList.Items))
	return true, nil
}

func configureSecret(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) *corev1.Secret {
	userID := cephClusterConnection.Spec.UserID
	userKey := cephClusterConnection.Spec.UserKey
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: controllerNamespace,
			Labels: map[string]string{
				StorageManagedLabelKey: CephClusterConnectionCtrlName,
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

	return true
}

func reconcileSecretCreateFunc(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (shouldRequeue bool, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretCreateFunc] starts reconciliataion of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

	newSecret := configureSecret(cephClusterConnection, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("[reconcileSecretCreateFunc] successfully configurated secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name))
	log.Trace(fmt.Sprintf("[reconcileSecretCreateFunc] secret: %+v", newSecret))

	err = cl.Create(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[reconcileSecretCreateFunc] unable to create a Secret %s for CephClusterConnection %s: %w", newSecret.Name, cephClusterConnection.Name, err)
		upError := updateCephClusterConnectionPhase(ctx, cl, cephClusterConnection, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileSecretCreateFunc] unable to update the CephClusterConnection %s: %w", cephClusterConnection.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}

	return false, nil
}

func reconcileSecretUpdateFunc(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (shouldRequeue bool, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] starts reconciliataion of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

	var oldSecret *corev1.Secret
	for _, s := range secretList.Items {
		if s.Name == secretName {
			oldSecret = &s
			break
		}
	}

	if oldSecret == nil {
		err := fmt.Errorf("[reconcileSecretUpdateFunc] unable to find a secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name)
		upError := updateCephClusterConnectionPhase(ctx, cl, cephClusterConnection, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileSecretUpdateFunc] unable to update the CephClusterConnection %s: %w", cephClusterConnection.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}

	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] secret %s was found for the CephClusterConnection %s", secretName, cephClusterConnection.Name))

	newSecret := configureSecret(cephClusterConnection, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] successfully configurated new secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name))
	log.Trace(fmt.Sprintf("[reconcileSecretUpdateFunc] new secret: %+v", newSecret))
	log.Trace(fmt.Sprintf("[reconcileSecretUpdateFunc] old secret: %+v", oldSecret))

	err = cl.Update(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[reconcileSecretUpdateFunc] unable to update the Secret %s for CephClusterConnection %s: %w", newSecret.Name, cephClusterConnection.Name, err)
		upError := updateCephClusterConnectionPhase(ctx, cl, cephClusterConnection, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileSecretUpdateFunc] unable to update the CephClusterConnection %s: %w", cephClusterConnection.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}

	return false, nil
}

func reconcileSecretDeleteFunc(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, secretName string) (shouldRequeue bool, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretDeleteFunc] starts reconciliataion of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

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
		log.Debug(fmt.Sprintf("[reconcileSecretDeleteFunc] starts removing a finalizer %s from the Secret %s", CephClusterConnectionControllerFinalizerName, secret.Name))
		_, err := removeFinalizerIfExists(ctx, cl, secret, CephClusterConnectionControllerFinalizerName)
		if err != nil {
			err = fmt.Errorf("[reconcileSecretDeleteFunc] unable to remove a finalizer %s from the Secret %s: %w", CephClusterConnectionControllerFinalizerName, secret.Name, err)
			upErr := updateCephClusterConnectionPhase(ctx, cl, cephClusterConnection, FailedStatusPhase, fmt.Sprintf("Unable to remove a finalizer, err: %s", err.Error()))
			if upErr != nil {
				upErr = fmt.Errorf("[reconcileSecretDeleteFunc] unable to update the CephClusterConnection %s: %w", cephClusterConnection.Name, upErr)
				err = errors.Join(err, upErr)
			}
			return true, err
		}

		err = cl.Delete(ctx, secret)
		if err != nil {
			err = fmt.Errorf("[reconcileSecretDeleteFunc] unable to delete a secret %s: %w", secret.Name, err)
			upErr := updateCephClusterConnectionPhase(ctx, cl, cephClusterConnection, FailedStatusPhase, fmt.Sprintf("Unable to delete a secret, err: %s", err.Error()))
			if upErr != nil {
				upErr = fmt.Errorf("[reconcileSecretDeleteFunc] unable to update the CephClusterConnection %s: %w", cephClusterConnection.Name, upErr)
				err = errors.Join(err, upErr)
			}
			return true, err
		}
	}

	log.Info(fmt.Sprintf("[reconcileSecretDeleteFunc] ends reconciliataion of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))

	return false, nil
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
