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

	failedMsgBuilder.WriteString("Validation of CephClusterConnection failed: ")

	if cephClusterConnection.Spec.ClusterID == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.clusterID field is empty; ")
	}

	if len(cephClusterConnection.Spec.Monitors) == 0 {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.monitors field is empty; ")
	}

	return validationPassed, failedMsgBuilder.String()
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

// ConfigMap
func IdentifyReconcileFuncForConfigMap(log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, configMapName string) (reconcileType string, err error) {
	if shouldReconcileByDeleteFunc(cephClusterConnection) {
		return internal.DeleteReconcile, nil
	}

	if shouldReconcileConfigMapByCreateFunc(configMapList, cephClusterConnection, configMapName) {
		return internal.CreateReconcile, nil
	}

	should, err := shouldReconcileConfigMapByUpdateFunc(log, configMapList, cephClusterConnection, configMapName)
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

func shouldReconcileConfigMapByUpdateFunc(log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, configMapName string) (bool, error) {
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
			for _, oldClusterConfig := range oldClusterConfigs {
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

func getClusterConfigsFromConfigMap(configMap corev1.ConfigMap) ([]v1alpha1.ClusterConfig, error) {
	jsonData, ok := configMap.Data["config.json"]
	if !ok {
		return nil, fmt.Errorf("[getClusterConfigsFromConfigMap] config.json key not found in the ConfigMap %s", configMap.Name)
	}

	var clusterConfigs []v1alpha1.ClusterConfig
	err := json.Unmarshal([]byte(jsonData), &clusterConfigs)
	if err != nil {
		return nil, fmt.Errorf("[getClusterConfigsFromConfigMap] unable to unmarshal data from the ConfigMap %s: %w", configMap.Name, err)
	}

	return clusterConfigs, nil
}

func configureClusterConfig(cephClusterConnection *v1alpha1.CephClusterConnection) v1alpha1.ClusterConfig {
	cephFs := map[string]string{}

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
	clusterConfigs := []v1alpha1.ClusterConfig{clusterConfig}
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

	for i, clusterConfig := range clusterConfigs {
		if clusterConfig.ClusterID == cephClusterConnection.Spec.ClusterID {
			clusterConfigs = slices.Delete(clusterConfigs, i, i+1)
		}
	}

	if updateAction == internal.UpdateConfigMapActionUpdate {
		newClusterConfig := configureClusterConfig(cephClusterConnection)
		clusterConfigs = append(clusterConfigs, newClusterConfig)
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
