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
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
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

	for _, oldSecret := range secretList.Items {
		if oldSecret.Name == secretName {
			newSecret := configureSecret(cephClusterConnection, controllerNamespace, secretName)
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

// ConfigMap
func IdentifyReconcileFuncForConfigMap(log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, configMapName string) (reconcileType string, err error) {
	if shouldReconcileByDeleteFunc(cephClusterConnection) {
		return internal.DeleteReconcile, nil
	}

	if shouldReconcileConfigMapByCreateFunc(configMapList, cephClusterConnection, configMapName) {
		return internal.CreateReconcile, nil
	}

	should, err := shouldReconcileConfigMapByUpdateFunc(log, configMapList, cephClusterConnection, controllerNamespace, configMapName)
	if err != nil {
		return "", err
	}
	if should {
		return internal.UpdateReconcile, nil
	}

	return "", nil
}

func shouldReconcileConfigMapByCreateFunc(configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, configMapName string) bool {
	if cephClusterConnection.DeletionTimestamp != nil {
		return false
	}

	for _, cm := range configMapList.Items {
		if cm.Name == configMapName {
			if cm.Data["config.json"] == "" {
				return true
			}

			return false
		}
	}

	return true
}

func shouldReconcileConfigMapByUpdateFunc(log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, configMapName string) (bool, error) {
	if cephClusterConnection.DeletionTimestamp != nil {
		return false, nil
	}

	configMapSelector := labels.Set(map[string]string{
		internal.StorageManagedLabelKey: CephClusterConnectionCtrlName,
	})

	for _, oldConfigMap := range configMapList.Items {
		if oldConfigMap.Name == configMapName {
			oldClusterConfigs, err := getClusterConfigsFromConfigMap(oldConfigMap)
			if err != nil {
				return false, err
			}

			equal := false
			clusterConfigExists := false
			for _, oldClusterConfig := range oldClusterConfigs.Items {
				if oldClusterConfig.ClusterID == cephClusterConnection.Spec.ClusterID {
					clusterConfigExists = true
					newClusterConfig := configureClusterConfig(cephClusterConnection)
					equal = reflect.DeepEqual(oldClusterConfig, newClusterConfig)

					log.Trace(fmt.Sprintf("[shouldReconcileConfigMapByUpdateFunc] old cluster config: %+v", oldClusterConfig))
					log.Trace(fmt.Sprintf("[shouldReconcileConfigMapByUpdateFunc] new cluster config: %+v", newClusterConfig))
					log.Trace(fmt.Sprintf("[shouldReconcileConfigMapByUpdateFunc] are cluster configs equal: %t", equal))
					break
				}
			}

			if !equal || !labels.Set(oldConfigMap.Labels).AsSelector().Matches(configMapSelector) {
				if !clusterConfigExists {
					log.Trace(fmt.Sprintf("[shouldReconcileConfigMapByUpdateFunc] a cluster config for the cluster %s does not exist in the ConfigMap %+v", cephClusterConnection.Spec.ClusterID, oldConfigMap))
				}
				if !labels.Set(oldConfigMap.Labels).AsSelector().Matches(configMapSelector) {
					log.Trace(fmt.Sprintf("[shouldReconcileConfigMapByUpdateFunc] a configMap %s labels %+v does not match the selector %+v", oldConfigMap.Name, oldConfigMap.Labels, configMapSelector))
				}

				log.Debug(fmt.Sprintf("[shouldReconcileConfigMapByUpdateFunc] a configMap %s should be updated", configMapName))
				return true, nil
			}

			return false, nil
		}
	}

	err := fmt.Errorf("[shouldReconcileConfigMapByUpdateFunc] a configMap %s not found in the list: %+v. It should be created", configMapName, configMapList.Items)
	return false, err
}

func getClusterConfigsFromConfigMap(configMap corev1.ConfigMap) (v1alpha1.ClusterConfigList, error) {
	jsonData, ok := configMap.Data["config.json"]
	if !ok {
		return v1alpha1.ClusterConfigList{}, fmt.Errorf("[getClusterConfigsFromConfigMap] config.json key not found in the ConfigMap %s", configMap.Name)
	}

	clusterConfigs := v1alpha1.ClusterConfigList{}
	err := json.Unmarshal([]byte(jsonData), &clusterConfigs)
	if err != nil {
		return v1alpha1.ClusterConfigList{}, fmt.Errorf("[getClusterConfigsFromConfigMap] unable to unmarshal data from the ConfigMap %s: %w", configMap.Name, err)
	}

	return clusterConfigs, nil
}

func configureClusterConfig(cephClusterConnection *v1alpha1.CephClusterConnection) v1alpha1.ClusterConfig {
	cephFs := map[string]string{}
	if cephClusterConnection.Spec.CephFS.SubvolumeGroup != "" {
		cephFs = map[string]string{
			"subvolumeGroup": cephClusterConnection.Spec.CephFS.SubvolumeGroup,
		}
	}

	clusterConfig := v1alpha1.ClusterConfig{
		ClusterID: cephClusterConnection.Spec.ClusterID,
		Monitors:  cephClusterConnection.Spec.Monitors,
		CephFS:    cephFs,
	}

	return clusterConfig
}

func reconcileConfigMapCreateFunc(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, configMapName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileConfigMapCreateFunc] starts reconciliation of ConfigMap %s for CephClusterConnection %s", configMapName, cephClusterConnection.Name))

	newClusterConfig := configureClusterConfig(cephClusterConnection)
	newConfigMap := createConfigMap(newClusterConfig, controllerNamespace, configMapName)
	log.Debug(fmt.Sprintf("[reconcileConfigMapCreateFunc] successfully configurated ConfigMap %s for the CephClusterConnection %s", configMapName, cephClusterConnection.Name))
	log.Trace(fmt.Sprintf("[reconcileConfigMapCreateFunc] configMap: %+v", newConfigMap))

	err = cl.Create(ctx, newConfigMap)
	if err != nil {
		err = fmt.Errorf("[reconcileConfigMapCreateFunc] unable to create a ConfigMap %s for CephClusterConnection %s: %w", newConfigMap.Name, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	return false, "Successfully created", nil
}

func reconcileConfigMapUpdateFunc(ctx context.Context, cl client.Client, log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, configMapName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileConfigMapUpdateFunc] starts reconciliation of ConfigMap %s for CephClusterConnection %s", configMapName, cephClusterConnection.Name))

	var oldConfigMap *corev1.ConfigMap
	for _, cm := range configMapList.Items {
		if cm.Name == configMapName {
			oldConfigMap = &cm
			break
		}
	}

	if oldConfigMap == nil {
		err := fmt.Errorf("[reconcileConfigMapUpdateFunc] unable to find a ConfigMap %s for the CephClusterConnection %s", configMapName, cephClusterConnection.Name)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[reconcileConfigMapUpdateFunc] ConfigMap %s was found for the CephClusterConnection %s", configMapName, cephClusterConnection.Name))

	updatedConfigMap := updateConfigMap(oldConfigMap, cephClusterConnection, internal.UpdateConfigMapActionUpdate)
	log.Debug(fmt.Sprintf("[reconcileConfigMapUpdateFunc] successfully configurated new ConfigMap %s for the CephClusterConnection %s", configMapName, cephClusterConnection.Name))
	log.Trace(fmt.Sprintf("[reconcileConfigMapUpdateFunc] updated ConfigMap: %+v", updatedConfigMap))
	log.Trace(fmt.Sprintf("[reconcileConfigMapUpdateFunc] old ConfigMap: %+v", oldConfigMap))

	err = cl.Update(ctx, updatedConfigMap)
	if err != nil {
		err = fmt.Errorf("[reconcileConfigMapUpdateFunc] unable to update the ConfigMap %s for CephClusterConnection %s: %w", updatedConfigMap.Name, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	log.Info(fmt.Sprintf("[reconcileConfigMapUpdateFunc] successfully updated the ConfigMap %s for the CephClusterConnection %s", updatedConfigMap.Name, cephClusterConnection.Name))

	return false, "Successfully updated", nil
}

func reconcileConfigMapDeleteFunc(ctx context.Context, cl client.Client, log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, configMapName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileConfigMapDeleteFunc] starts reconciliation of ConfigMap %s for CephClusterConnection %s", configMapName, cephClusterConnection.Name))

	var configMap *corev1.ConfigMap
	for _, cm := range configMapList.Items {
		if cm.Name == configMapName {
			configMap = &cm
			break
		}
	}

	if configMap == nil {
		log.Info(fmt.Sprintf("[reconcileConfigMapDeleteFunc] no ConfigMap with name %s found for the CephClusterConnection %s", configMapName, cephClusterConnection.Name))
	}

	if configMap != nil {
		log.Info(fmt.Sprintf("[reconcileConfigMapDeleteFunc] successfully found a ConfigMap %s for the CephClusterConnection %s", configMapName, cephClusterConnection.Name))
		newConfigMap := updateConfigMap(configMap, cephClusterConnection, internal.UpdateConfigMapActionDelete)

		err := cl.Update(ctx, newConfigMap)
		if err != nil {
			err = fmt.Errorf("[reconcileConfigMapDeleteFunc] unable to delete cluster config for the CephClusterConnection %s from the ConfigMap %s: %w", cephClusterConnection.Name, configMapName, err)
			return true, err.Error(), err
		}
	}

	_, err = removeFinalizerIfExists(ctx, cl, cephClusterConnection, CephClusterConnectionControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[reconcileConfigMapDeleteFunc] unable to remove finalizer from the CephClusterConnection %s: %w", cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	log.Info(fmt.Sprintf("[reconcileConfigMapDeleteFunc] ends reconciliation of ConfigMap %s for CephClusterConnection %s", configMapName, cephClusterConnection.Name))

	return false, "", nil
}

func createConfigMap(clusterConfig v1alpha1.ClusterConfig, controllerNamespace, configMapName string) *corev1.ConfigMap {
	clusterConfigs := v1alpha1.ClusterConfigList{
		Items: []v1alpha1.ClusterConfig{clusterConfig},
	}
	jsonData, _ := json.Marshal(clusterConfigs)

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: controllerNamespace,
			Labels: map[string]string{
				internal.StorageManagedLabelKey: CephClusterConnectionCtrlName,
			},
			Finalizers: []string{CephClusterConnectionControllerFinalizerName},
		},
		Data: map[string]string{
			"config.json": string(jsonData),
		},
	}

	return configMap
}

func updateConfigMap(oldConfigMap *corev1.ConfigMap, cephClusterConnection *v1alpha1.CephClusterConnection, updateAction string) *corev1.ConfigMap {
	clusterConfigs, _ := getClusterConfigsFromConfigMap(*oldConfigMap)

	for i, clusterConfig := range clusterConfigs.Items {
		if clusterConfig.ClusterID == cephClusterConnection.Spec.ClusterID {
			clusterConfigs.Items = slices.Delete(clusterConfigs.Items, i, i+1)
		}
	}

	if updateAction == internal.UpdateConfigMapActionUpdate {
		newClusterConfig := configureClusterConfig(cephClusterConnection)
		clusterConfigs.Items = append(clusterConfigs.Items, newClusterConfig)
	}

	newJsonData, _ := json.Marshal(clusterConfigs)

	configMap := oldConfigMap.DeepCopy()
	configMap.Data["config.json"] = string(newJsonData)

	if configMap.Labels == nil {
		configMap.Labels = map[string]string{}
	}
	configMap.Labels[internal.StorageManagedLabelKey] = CephClusterConnectionCtrlName

	if configMap.Finalizers == nil {
		configMap.Finalizers = []string{}
	}

	if !slices.Contains(configMap.Finalizers, CephClusterConnectionControllerFinalizerName) {
		configMap.Finalizers = append(configMap.Finalizers, CephClusterConnectionControllerFinalizerName)
	}

	return configMap
}
