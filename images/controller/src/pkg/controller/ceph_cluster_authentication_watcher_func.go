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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func validateCephClusterAuthenticationSpec(cephClusterAuthentication *v1alpha1.CephClusterAuthentication) (bool, string) {
	if cephClusterAuthentication.DeletionTimestamp != nil {
		return true, ""
	}

	var (
		failedMsgBuilder strings.Builder
		validationPassed = true
	)

	failedMsgBuilder.WriteString("Validation of CephClusterAuthentication failed: ")

	if cephClusterAuthentication.Spec.UserID == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.userID field is empty; ")
	}

	if cephClusterAuthentication.Spec.UserKey == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.userKey field is empty; ")
	}

	return validationPassed, failedMsgBuilder.String()
}

func IdentifyReconcileFuncForSecret(log logger.Logger, secretList *corev1.SecretList, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace, secretName string) (reconcileType string, err error) {
	if shouldReconcileByDeleteFunc(cephClusterAuthentication) {
		return internal.DeleteReconcile, nil
	}

	if shouldReconcileSecretByCreateFunc(secretList, cephClusterAuthentication, secretName) {
		return internal.CreateReconcile, nil
	}

	should, err := shouldReconcileSecretByUpdateFunc(log, secretList, cephClusterAuthentication, controllerNamespace, secretName)
	if err != nil {
		return "", err
	}
	if should {
		return internal.UpdateReconcile, nil
	}

	return "", nil
}

func shouldReconcileSecretByCreateFunc(secretList *corev1.SecretList, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, secretName string) bool {
	if cephClusterAuthentication.DeletionTimestamp != nil {
		return false
	}

	for _, s := range secretList.Items {
		if s.Name == secretName {
			return false
		}
	}

	return true
}

func shouldReconcileSecretByUpdateFunc(log logger.Logger, secretList *corev1.SecretList, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace, secretName string) (bool, error) {
	if cephClusterAuthentication.DeletionTimestamp != nil {
		return false, nil
	}

	for _, oldSecret := range secretList.Items {
		if oldSecret.Name == secretName {
			newSecret := configureSecret(cephClusterAuthentication, controllerNamespace, secretName)
			equal := areSecretsEqual(&oldSecret, newSecret)

			log.Trace(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] old secret: %+v", oldSecret))
			log.Trace(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] new secret: %+v", newSecret))
			log.Trace(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] are secrets equal: %t", equal))

			if !equal {
				log.Debug(fmt.Sprintf("[shouldReconcileSecretByUpdateFunc] a secret %s should be updated", secretName))
				return true, nil
			}

			return false, nil
		}
	}
	err := fmt.Errorf("[shouldReconcileSecretByUpdateFunc] a secret %s not found in the list: %+v. It should be created", secretName, secretList.Items)
	return false, err
}

func configureSecret(cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace, secretName string) *corev1.Secret {
	userID := cephClusterAuthentication.Spec.UserID
	userKey := cephClusterAuthentication.Spec.UserKey
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: controllerNamespace,
			Labels: map[string]string{
				internal.StorageManagedLabelKey: CephClusterAuthenticationCtrlName,
			},
			Finalizers: []string{CephClusterAuthenticationControllerFinalizerName},
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

func reconcileSecretCreateFunc(ctx context.Context, cl client.Client, log logger.Logger, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretCreateFunc] starts reconciliation of CephClusterAuthentication %s for Secret %s", cephClusterAuthentication.Name, secretName))

	newSecret := configureSecret(cephClusterAuthentication, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("[reconcileSecretCreateFunc] successfully configurated secret %s for the CephClusterAuthentication %s", secretName, cephClusterAuthentication.Name))
	log.Trace(fmt.Sprintf("[reconcileSecretCreateFunc] secret: %+v", newSecret))

	err = cl.Create(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[reconcileSecretCreateFunc] unable to create a Secret %s for CephClusterAuthentication %s: %w", newSecret.Name, cephClusterAuthentication.Name, err)
		return true, err.Error(), err
	}

	return false, "Successfully created", nil
}

func reconcileSecretUpdateFunc(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] starts reconciliation of CephClusterAuthentication %s for Secret %s", cephClusterAuthentication.Name, secretName))

	var oldSecret *corev1.Secret
	for _, s := range secretList.Items {
		if s.Name == secretName {
			oldSecret = &s
			break
		}
	}

	if oldSecret == nil {
		err := fmt.Errorf("[reconcileSecretUpdateFunc] unable to find a secret %s for the CephClusterAuthentication %s", secretName, cephClusterAuthentication.Name)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] secret %s was found for the CephClusterAuthentication %s", secretName, cephClusterAuthentication.Name))

	newSecret := configureSecret(cephClusterAuthentication, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("[reconcileSecretUpdateFunc] successfully configurated new secret %s for the CephClusterAuthentication %s", secretName, cephClusterAuthentication.Name))
	log.Trace(fmt.Sprintf("[reconcileSecretUpdateFunc] new secret: %+v", newSecret))
	log.Trace(fmt.Sprintf("[reconcileSecretUpdateFunc] old secret: %+v", oldSecret))

	err = cl.Update(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[reconcileSecretUpdateFunc] unable to update the Secret %s for CephClusterAuthentication %s: %w", newSecret.Name, cephClusterAuthentication.Name, err)
		return true, err.Error(), err
	}

	log.Info(fmt.Sprintf("[reconcileSecretUpdateFunc] successfully updated the Secret %s for the CephClusterAuthentication %s", newSecret.Name, cephClusterAuthentication.Name))

	return false, "Successfully updated", nil
}

func reconcileSecretDeleteFunc(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, secretName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileSecretDeleteFunc] starts reconciliation of CephClusterAuthentication %s for Secret %s", cephClusterAuthentication.Name, secretName))

	var secret *corev1.Secret
	for _, s := range secretList.Items {
		if s.Name == secretName {
			secret = &s
			break
		}
	}

	if secret == nil {
		log.Info(fmt.Sprintf("[reconcileSecretDeleteFunc] no secret with name %s found for the CephClusterAuthentication %s", secretName, cephClusterAuthentication.Name))
	}

	if secret != nil {
		log.Info(fmt.Sprintf("[reconcileSecretDeleteFunc] successfully found a secret %s for the CephClusterAuthentication %s", secretName, cephClusterAuthentication.Name))
		err = deleteSecret(ctx, cl, secret)

		if err != nil {
			err = fmt.Errorf("[reconcileSecretDeleteFunc] unable to delete the Secret %s for the CephCluster %s: %w", secret.Name, cephClusterAuthentication.Name, err)
			return true, err.Error(), err
		}
	}

	_, err = removeFinalizerIfExists(ctx, cl, cephClusterAuthentication, CephClusterAuthenticationControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[reconcileSecretDeleteFunc] unable to remove a finalizer %s from the CephClusterAuthentication %s: %w", CephClusterAuthenticationControllerFinalizerName, cephClusterAuthentication.Name, err)
		return true, err.Error(), err
	}

	log.Info(fmt.Sprintf("[reconcileSecretDeleteFunc] ends reconciliation of CephClusterAuthentication %s for Secret %s", cephClusterAuthentication.Name, secretName))

	return false, "", nil
}

func deleteSecret(ctx context.Context, cl client.Client, secret *corev1.Secret) error {
	_, err := removeFinalizerIfExists(ctx, cl, secret, CephClusterAuthenticationControllerFinalizerName)
	if err != nil {
		return err
	}

	err = cl.Delete(ctx, secret)
	if err != nil {
		return err
	}

	return nil
}

func updateCephClusterAuthenticationPhase(ctx context.Context, cl client.Client, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, phase, reason string) error {
	if cephClusterAuthentication.Status == nil {
		cephClusterAuthentication.Status = &v1alpha1.CephClusterAuthenticationStatus{}
	}

	cephClusterAuthentication.Status.Phase = phase
	cephClusterAuthentication.Status.Reason = reason

	err := cl.Status().Update(ctx, cephClusterAuthentication)
	if err != nil {
		return err
	}

	return nil
}
