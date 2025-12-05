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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/logger"
)

func validateCephClusterConnectionSpec(cephClusterConnection *v1alpha1.CephClusterConnection) (bool, string) {
	if cephClusterConnection.DeletionTimestamp != nil {
		return true, ""
	}

	var (
		failedMsgBuilder strings.Builder
		validationPassed = true
	)

	failedMsgBuilder.WriteString("Validation of CephClusterConnection failed: ")

	if cephClusterConnection.Spec.ClusterID == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.clusterID field is empty; ")
	}

	if len(cephClusterConnection.Spec.Monitors) == 0 {
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

func updateCephClusterConnectionPhaseIfNeeded(ctx context.Context, cl client.Client, cephClusterConnection *v1alpha1.CephClusterConnection, phase, reason string) error {
	needUpdate := false

	if cephClusterConnection.Status == nil {
		cephClusterConnection.Status = &v1alpha1.CephClusterConnectionStatus{}
		needUpdate = true
	}
	if cephClusterConnection.Status.Phase != phase {
		cephClusterConnection.Status.Phase = phase
		needUpdate = true
	}

	if cephClusterConnection.Status.Reason != reason {
		cephClusterConnection.Status.Reason = reason
		needUpdate = true
	}

	if needUpdate {
		err := cl.Status().Update(ctx, cephClusterConnection)
		if err != nil {
			return err
		}
	}

	return nil
}

func reconcileConfigMap(ctx context.Context, cl client.Client, log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, configMapNamespace, configMapName, controllerNamespace string) (shouldRequeue bool, msg string, err error) {
	var configMap *corev1.ConfigMap
	for _, cm := range configMapList.Items {
		if cm.Name == configMapName {
			configMap = &cm
			break
		}
	}

	if configMap == nil && cephClusterConnection.DeletionTimestamp == nil {
		return createConfigMap(ctx, cl, log, cephClusterConnection, configMapNamespace, configMapName)
	}

	if configMap == nil {
		if cephClusterConnection.DeletionTimestamp == nil {
			return createConfigMap(ctx, cl, log, cephClusterConnection, configMapNamespace, configMapName)
		}
		log.Info("cephClusterConnection is being deleted and Config map not found.")
		return false, "", nil
	}

	updateAction := internal.UpdateConfigMapActionUpdate
	if cephClusterConnection.DeletionTimestamp != nil {
		updateAction = internal.UpdateConfigMapActionDelete
	}

	err = updateConfigMapIfNeeded(ctx, cl, log, configMap, cephClusterConnection, updateAction, controllerNamespace)
	if err != nil {
		if errors.Is(err, errUpdateIncompleted) {
			return true, "", nil
		}
		return true, err.Error(), err
	}

	return false, fmt.Sprintf("Successfully reconciled ConfigMap %s", configMapName), nil
}

func getClusterConfigsFromConfigMap(configMap *corev1.ConfigMap) ([]v1alpha1.ClusterConfig, error) {
	var clusterConfigs []v1alpha1.ClusterConfig

	jsonData, ok := configMap.Data["config.json"]
	if !ok {
		return nil, fmt.Errorf("[getClusterConfigsFromConfigMap] config.json key not found in the ConfigMap %s", configMap.Name)
	}

	err := json.Unmarshal([]byte(jsonData), &clusterConfigs)
	if err != nil {
		return nil, fmt.Errorf("[getClusterConfigsFromConfigMap] unable to unmarshal data from the ConfigMap %s: %w", configMap.Name, err)
	}

	return clusterConfigs, nil
}

func generateClusterConfig(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) v1alpha1.ClusterConfig {
	// Generate secret name and namespace
	secretName := internal.CephClusterConnectionSecretPrefix + cephClusterConnection.Name

	secretRef := v1alpha1.SecretReference{
		Name:      secretName,
		Namespace: controllerNamespace,
	}

	cephFs := v1alpha1.CephFSConfig{
		ControllerPublishSecretRef: secretRef,
	}

	// if SubvolumeGroup given in connection config we should put it to configmap
	if cephClusterConnection.Spec.CephFS != nil && cephClusterConnection.Spec.CephFS.SubvolumeGroup != "" {
		cephFs.SubvolumeGroup = cephClusterConnection.Spec.CephFS.SubvolumeGroup
	}

	rbd := v1alpha1.RBDConfig{
		ControllerPublishSecretRef: secretRef,
	}

	clusterConfig := v1alpha1.ClusterConfig{
		ClusterID: cephClusterConnection.Spec.ClusterID,
		Monitors:  cephClusterConnection.Spec.Monitors,
		CephFS:    cephFs,
		RBD:       rbd,
	}

	return clusterConfig
}

func createConfigMap(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, configMapName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[createConfigMap] starts creation of ConfigMap %s for CephClusterConnection %s", configMapName, cephClusterConnection.Name))

	newClusterConfig := generateClusterConfig(cephClusterConnection, controllerNamespace)
	newConfigMap, err := generateNewConfigMap(newClusterConfig, controllerNamespace, configMapName)
	if err != nil {
		err = fmt.Errorf("[createConfigMap] unable to generate the ConfigMap %s for CephClusterConnection %s: %w", configMapName, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}
	log.Debug(fmt.Sprintf("[createConfigMap] successfully generate the ConfigMap %s for the CephClusterConnection %s", configMapName, cephClusterConnection.Name))
	log.Trace(fmt.Sprintf("[createConfigMap] configMap: %+v", newConfigMap))

	err = cl.Create(ctx, newConfigMap)
	if err != nil {
		err = fmt.Errorf("[createConfigMap] unable to create a ConfigMap %s for CephClusterConnection %s: %w", newConfigMap.Name, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[createConfigMap] successfully created ConfigMap %s for the CephClusterConnection %s", newConfigMap.Name, cephClusterConnection.Name))
		return false, fmt.Sprintf("Successfully created ConfigMap %s", newConfigMap.Name), nil
}

func generateNewConfigMap(clusterConfig v1alpha1.ClusterConfig, controllerNamespace, configMapName string) (*corev1.ConfigMap, error) {
	clusterConfigs := []v1alpha1.ClusterConfig{clusterConfig}
	jsonData, err := json.Marshal(clusterConfigs)
	if err != nil {
		return nil, fmt.Errorf("[generateConfigMap] unable to marshal clusterConfigs: %w", err)
	}

	if controllerNamespace == "" {
		return nil, fmt.Errorf("[generateConfigMap] controllerNamespace is empty")
	}

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

	return configMap, nil
}

var errUpdateIncompleted = errors.New("update was not completed yet, should requeue")

func updateConfigMapIfNeeded(ctx context.Context, cl client.Client, log logger.Logger, configMap *corev1.ConfigMap, cephClusterConnection *v1alpha1.CephClusterConnection, updateAction string, controllerNamespace string) error {
	log.Debug(fmt.Sprintf("[updateConfigMapIfNeeded] starts for the ConfigMap %s/%s", configMap.Namespace, configMap.Name))
	log.Trace(fmt.Sprintf("[updateConfigMapIfNeeded] ConfigMap: %+v", configMap))
	log.Trace(fmt.Sprintf("[updateConfigMapIfNeeded] Update action: %s", updateAction))

	if configMap.DeletionTimestamp != nil {
		// delete finalizers
		configMap.Finalizers = slices.DeleteFunc(
			configMap.Finalizers,
			func(f string) bool { return f == CephClusterConnectionControllerFinalizerName },
		)
		if err := cl.Update(ctx, configMap); err != nil {
			return err
		}
		return errUpdateIncompleted
	}

	needUpdate := false

	clusterConfigs, err := getClusterConfigsFromConfigMap(configMap)
	if err != nil {
		log.Warning(fmt.Sprintf("[updateConfigMapIfNeeded] unable to get cluster configs from the ConfigMap %s. New cluster config will be created. Error: %s", configMap.Name, err.Error()))
		clusterConfigs = []v1alpha1.ClusterConfig{}
		needUpdate = true
	}

	log.Trace(fmt.Sprintf("[updateConfigMapIfNeeded] clusterConfigs: %+v", clusterConfigs))

	clusterConfigs, updated := updateClusterConfigsIfNeeded(log, clusterConfigs, cephClusterConnection, updateAction, controllerNamespace)
	if updated {
		needUpdate = true
	}

	obj, metadataAdded := addRequiredMetadataIfNeeded(configMap)
	configMap = obj.(*corev1.ConfigMap)
	if metadataAdded {
		needUpdate = true
	}

	if !needUpdate {
		log.Debug(fmt.Sprintf("[updateConfigMapIfNeeded] no changes required for the ConfigMap %s", configMap.Name))
		return nil
	}

	log.Debug(fmt.Sprintf("[updateConfigMapIfNeeded] changes required for the ConfigMap %s", configMap.Name))
	newJSONData, err := json.Marshal(clusterConfigs)
	if err != nil {
		return fmt.Errorf("[updateConfigMapIfNeeded] unable to marshal clusterConfigs: %w", err)
	}
	log.Trace(fmt.Sprintf("[updateConfigMapIfNeeded] newJSONData: %s", newJSONData))

	if configMap.Data == nil {
		configMap.Data = map[string]string{}
	}

	if newJSONData == nil {
		return fmt.Errorf("[updateConfigMapIfNeeded] newJSONData is nil")
	}

	configMap.Data["config.json"] = string(newJSONData)

	log.Trace(fmt.Sprintf("[updateConfigMapIfNeeded] updated ConfigMap: %+v", configMap))

	err = cl.Update(ctx, configMap)
	if err != nil {
		return fmt.Errorf("[updateConfigMapIfNeeded] unable to update the ConfigMap %s: %w", configMap.Name, err)
	}

	return nil
}

func findClusterConfigByClusterID(clusterConfigs []v1alpha1.ClusterConfig, clusterID string) (int, bool) {
	for i, clusterConfig := range clusterConfigs {
		if clusterConfig.ClusterID == clusterID {
			return i, true
		}
	}

	return -1, false
}

func updateClusterConfigsIfNeeded(log logger.Logger, clusterConfigs []v1alpha1.ClusterConfig, cephClusterConnection *v1alpha1.CephClusterConnection, updateAction string, controllerNamespace string) ([]v1alpha1.ClusterConfig, bool) {
	updated := false

	clusterConfigIndex, clusterConfigExists := findClusterConfigByClusterID(clusterConfigs, cephClusterConnection.Spec.ClusterID)
	log.Debug(fmt.Sprintf("[updateClusterConfigsIfNeeded] Find cluster config by cluster ID: %d, %t. Index: %d", clusterConfigIndex, clusterConfigExists, clusterConfigIndex))

	switch updateAction {
	case internal.UpdateConfigMapActionDelete:
		if clusterConfigExists {
			log.Debug(fmt.Sprintf("[updateClusterConfigsIfNeeded] clusterConfigExists %t and updateAction == internal.UpdateConfigMapActionDelete. Delete clusterConfig", clusterConfigExists))
			clusterConfigs = append(clusterConfigs[:clusterConfigIndex], clusterConfigs[clusterConfigIndex+1:]...)
			updated = true
		} else {
			log.Debug(fmt.Sprintf("[updateClusterConfigsIfNeeded] clusterConfigExists %t. No need to delete clusterConfig", clusterConfigExists))
		}
	default:
		newClusterConfig := generateClusterConfig(cephClusterConnection, controllerNamespace)
		log.Trace(fmt.Sprintf("[updateClusterConfigsIfNeeded] updateAction %s, newClusterConfig: %+v", updateAction, newClusterConfig))

		if clusterConfigExists {
			log.Trace(fmt.Sprintf("[updateClusterConfigsIfNeeded] existedClusterConfig: %+v", clusterConfigs[clusterConfigIndex]))
			if !reflect.DeepEqual(clusterConfigs[clusterConfigIndex], newClusterConfig) {
				log.Debug(fmt.Sprintf("[updateClusterConfigsIfNeeded] clusterConfigExists: %t and configs differ. Updating clusterConfig at index %d", clusterConfigExists, clusterConfigIndex))
				clusterConfigs[clusterConfigIndex] = newClusterConfig
				updated = true
			} else {
				log.Debug(fmt.Sprintf("[updateClusterConfigsIfNeeded] clusterConfigExists: %t and configs are equal. No need to update", clusterConfigExists))
			}
		} else {
			log.Debug(fmt.Sprintf("[updateClusterConfigsIfNeeded] clusterConfigExists %t. Append newClusterConfig", clusterConfigExists))
			clusterConfigs = append(clusterConfigs, newClusterConfig)
			updated = true
		}
	}

	return clusterConfigs, updated
}

// Secret
func reconcileSecret(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, secretNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	var secret *corev1.Secret
	for _, s := range secretList.Items {
		if s.Name == secretName {
			secret = &s
			break
		}
	}

	if cephClusterConnection.DeletionTimestamp != nil {
		return removeFinalizerAndDeleteSecret(ctx, cl, log, secret, CephClusterConnectionControllerFinalizerName)
	}

	if secret == nil {
		return createSecret(ctx, cl, log, cephClusterConnection, secretNamespace, secretName)
	}

	err = updateSecretIfNeeded(ctx, cl, log, secret, cephClusterConnection)
	if err != nil {
		return true, err.Error(), err
	}

	return false, fmt.Sprintf("Successfully reconciled Secret %s", secretName), nil
}

func createSecret(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[createSecret] starts creation of Secret %s for CephClusterConnection %s", secretName, cephClusterConnection.Name))

	newSecret := generateNewSecret(cephClusterConnection, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("[createSecret] successfully generate the Secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name))

	err = cl.Create(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[createSecret] unable to create a Secret %s for CephClusterConnection %s: %w", newSecret.Name, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[createSecret] successfully created Secret %s for the CephClusterConnection %s", newSecret.Name, cephClusterConnection.Name))
	return false, fmt.Sprintf("Successfully created Secret %s", newSecret.Name), nil
}

func generateNewSecret(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) *corev1.Secret {
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

func removeFinalizerAndDeleteSecret(ctx context.Context, cl client.Client, log logger.Logger, secret *corev1.Secret, finalizerName string) (shouldRequeue bool, msg string, err error) {
	if secret == nil {
		log.Debug("[deleteSecret] Secret is nil. No need to delete")
		return false, "[deleteSecret] Secret is nil. No need to delete", nil
	}

	err = removeFinalizerIfExists(ctx, cl, secret, finalizerName)
	if err != nil {
		err = fmt.Errorf("[deleteSecret] unable to remove finalizer from the Secret %s: %w", secret.Name, err)
		return true, err.Error(), err
	}

	err = cl.Delete(ctx, secret)
	if err != nil {
		err = fmt.Errorf("[deleteSecret] unable to delete the Secret %s: %w", secret.Name, err)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[deleteSecret] successfully deleted Secret %s", secret.Name))

	return false, fmt.Sprintf("[deleteSecret] Successfully deleted Secret %s", secret.Name), nil
}

func updateSecretIfNeeded(ctx context.Context, cl client.Client, log logger.Logger, oldSecret *corev1.Secret, cephClusterConnection *v1alpha1.CephClusterConnection) error {
	log.Debug(fmt.Sprintf("[updateSecretIfNeeded] starts for the Secret %s/%s", oldSecret.Namespace, oldSecret.Name))

	needUpdate := false

	newSecret := generateNewSecret(cephClusterConnection, oldSecret.Namespace, oldSecret.Name)
	needUpdate = compareSecretsData(oldSecret, newSecret)
	if needUpdate {
		log.Debug(fmt.Sprintf("[updateSecretIfNeeded] data in the Secret %s has been changed", oldSecret.Name))
		oldSecret.StringData = newSecret.StringData
	}

	obj, metadataAdded := addRequiredMetadataIfNeeded(oldSecret)
	oldSecret = obj.(*corev1.Secret)
	if metadataAdded {
		log.Trace(fmt.Sprintf("[updateSecretIfNeeded] metadataAdded: %t", metadataAdded))
		needUpdate = true
	}

	if !needUpdate {
		log.Debug(fmt.Sprintf("[updateSecretIfNeeded] no changes required for the Secret %s", oldSecret.Name))
		return nil
	}

	log.Debug(fmt.Sprintf("[updateSecretIfNeeded] changes required for the Secret %s", oldSecret.Name))

	err := cl.Update(ctx, oldSecret)
	if err != nil {
		return fmt.Errorf("[updateSecretIfNeeded] unable to update the Secret %s: %w", oldSecret.Name, err)
	}

	return nil
}

func addRequiredMetadataIfNeeded(obj metav1.Object) (metav1.Object, bool) {
	labelsAdded := false
	finalizersAdded := false

	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		labelsAdded = true
	}

	if labels[internal.StorageManagedLabelKey] != CephClusterConnectionCtrlName {
		labels[internal.StorageManagedLabelKey] = CephClusterConnectionCtrlName
		labelsAdded = true
	}

	if labelsAdded {
		obj.SetLabels(labels)
	}

	finalizers := obj.GetFinalizers()
	if finalizers == nil {
		finalizers = []string{}
		finalizersAdded = true
	}

	if !slices.Contains(finalizers, CephClusterConnectionControllerFinalizerName) {
		finalizers = append(finalizers, CephClusterConnectionControllerFinalizerName)
		finalizersAdded = true
	}

	if finalizersAdded {
		obj.SetFinalizers(finalizers)
	}

	return obj, labelsAdded || finalizersAdded
}

func compareSecretsData(oldSecret, newSecret *corev1.Secret) bool {
	if len(newSecret.StringData) != len(oldSecret.Data) {
		return true
	}

	for key, newValue := range newSecret.StringData {
		oldValBytes, found := oldSecret.Data[key]
		if !found {
			return true
		}

		oldValue := string(oldValBytes)
		if newValue != oldValue {
			return true
		}
	}

	return false
}

// GenerateClusterConfigForTesting is a wrapper for testing the generateClusterConfig function
func GenerateClusterConfigForTesting(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) v1alpha1.ClusterConfig {
	return generateClusterConfig(cephClusterConnection, controllerNamespace)
}
