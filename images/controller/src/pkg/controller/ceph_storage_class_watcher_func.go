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
	"d8-controller/api/v1alpha1"
	storagev1alpha1 "d8-controller/api/v1alpha1"
	"d8-controller/pkg/internal"
	"d8-controller/pkg/logger"
	"fmt"
	"reflect"
	"strings"

	"slices"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func IdentifyReconcileFuncForStorageClass(log logger.Logger, scList *v1.StorageClassList, cephSC *storagev1alpha1.CephStorageClass, controllerNamespace, clusterID string) (reconcileType string, err error) {
	if shouldReconcileStorageClassByCreateFunc(scList, cephSC) {
		return internal.CreateReconcile, nil
	}

	should, err := shouldReconcileStorageClassByUpdateFunc(log, scList, cephSC, controllerNamespace, clusterID)
	if err != nil {
		return "", err
	}
	if should {
		return internal.UpdateReconcile, nil
	}

	return "", nil
}

func shouldReconcileStorageClassByCreateFunc(scList *v1.StorageClassList, cephSC *storagev1alpha1.CephStorageClass) bool {
	if cephSC.DeletionTimestamp != nil {
		return false
	}

	for _, sc := range scList.Items {
		if sc.Name == cephSC.Name {
			return false
		}
	}

	return true
}

func shouldReconcileStorageClassByUpdateFunc(log logger.Logger, scList *v1.StorageClassList, cephSC *storagev1alpha1.CephStorageClass, controllerNamespace, clusterID string) (bool, error) {
	if cephSC.DeletionTimestamp != nil {
		return false, nil
	}

	for _, oldSC := range scList.Items {
		if oldSC.Name == cephSC.Name {
			if slices.Contains(allowedProvisioners, oldSC.Provisioner) {
				newSC, err := updateStorageClass(cephSC, &oldSC, controllerNamespace, clusterID)
				if err != nil {
					return false, err
				}

				diff, err := GetSCDiff(&oldSC, newSC)
				if err != nil {
					return false, err
				}

				if diff != "" {
					log.Debug(fmt.Sprintf("[shouldReconcileStorageClassByUpdateFunc] a storage class %s should be updated. Diff: %s", oldSC.Name, diff))
					return true, nil
				}

				if cephSC.Status != nil && cephSC.Status.Phase == v1alpha1.PhaseFailed {
					return true, nil
				}

				return false, nil

			} else {
				err := fmt.Errorf("a storage class %s with provisioner % s does not belong to allowed provisioners: %v", oldSC.Name, oldSC.Provisioner, allowedProvisioners)
				return false, err
			}
		}
	}

	err := fmt.Errorf("a storage class %s does not exist", cephSC.Name)
	return false, err
}

func reconcileStorageClassCreateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	cephSC *storagev1alpha1.CephStorageClass,
	controllerNamespace, clusterID string,
) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] starts for CephStorageClass %q", cephSC.Name))

	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] starts storage class configuration for the CephStorageClass, name: %s", cephSC.Name))
	newSC, err := ConfigureStorageClass(cephSC, controllerNamespace, clusterID)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassCreateFunc] unable to configure a Storage Class for the CephStorageClass %s: %w", cephSC.Name, err)
		return false, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] successfully configurated storage class for the CephStorageClass, name: %s", cephSC.Name))
	log.Trace(fmt.Sprintf("[reconcileStorageClassCreateFunc] storage class: %+v", newSC))

	created, err := createStorageClassIfNotExists(ctx, cl, scList, newSC)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassCreateFunc] unable to create a Storage Class %s: %w", newSC.Name, err)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] a storage class %s was created: %t", newSC.Name, created))
	if created {
		log.Info(fmt.Sprintf("[reconcileStorageClassCreateFunc] successfully create storage class, name: %s", newSC.Name))
	} else {
		err = fmt.Errorf("[reconcileStorageClassCreateFunc] Storage class %s already exists", newSC.Name)
		return true, err.Error(), err
	}

	return false, "Successfully created", nil
}

func reconcileStorageClassUpdateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	cephSC *storagev1alpha1.CephStorageClass,
	controllerNamespace, clusterID string,
) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileStorageClassUpdateFunc] starts for CephStorageClass %q", cephSC.Name))

	var oldSC *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == cephSC.Name {
			oldSC = &s
			break
		}
	}

	if oldSC == nil {
		err = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to find a storage class for the CephStorageClass %s: %w", cephSC.Name, err)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("[reconcileStorageClassUpdateFunc] successfully found a storage class for the CephStorageClass, name: %s", cephSC.Name))
	log.Trace(fmt.Sprintf("[reconcileStorageClassUpdateFunc] storage class: %+v", oldSC))

	newSC, err := updateStorageClass(cephSC, oldSC, controllerNamespace, clusterID)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to configure a Storage Class for the CephStorageClass %s: %w", cephSC.Name, err)
		return false, err.Error(), err
	}
	log.Debug(fmt.Sprintf("[reconcileStorageClassUpdateFunc] successfully configurated storage class for the CephStorageClass, name: %s", cephSC.Name))
	log.Trace(fmt.Sprintf("[reconcileStorageClassUpdateFunc] new storage class: %+v", newSC))
	log.Trace(fmt.Sprintf("[reconcileStorageClassUpdateFunc] old storage class: %+v", oldSC))

	err = recreateStorageClass(ctx, cl, oldSC, newSC)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to recreate a Storage Class %s: %w", newSC.Name, err)
		return true, err.Error(), err
	}

	log.Info(fmt.Sprintf("[reconcileStorageClassUpdateFunc] a Storage Class %s was successfully recreated", newSC.Name))

	return false, "Successfully updated", nil
}

func reconcileStorageClassDeleteFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	cephSC *storagev1alpha1.CephStorageClass,
) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[reconcileStorageClassDeleteFunc] starts for CephStorageClass %q", cephSC.Name))

	var sc *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == cephSC.Name {
			sc = &s
			break
		}
	}

	if sc == nil {
		log.Info(fmt.Sprintf("[reconcileStorageClassDeleteFunc] no storage class found for the CephStorageClass, name: %s", cephSC.Name))
	}

	if sc != nil {
		log.Info(fmt.Sprintf("[reconcileStorageClassDeleteFunc] successfully found a storage class for the CephStorageClass %s", cephSC.Name))
		log.Debug(fmt.Sprintf("[reconcileStorageClassDeleteFunc] starts identifying a provisioner for the storage class %s", sc.Name))

		if slices.Contains(allowedProvisioners, sc.Provisioner) {
			log.Info(fmt.Sprintf("[reconcileStorageClassDeleteFunc] the storage class %s provisioner %s belongs to allowed provisioners: %v", sc.Name, sc.Provisioner, allowedProvisioners))
			err := deleteStorageClass(ctx, cl, sc)
			if err != nil {
				err = fmt.Errorf("[reconcileStorageClassDeleteFunc] unable to delete a storage class %s: %w", sc.Name, err)
				return true, err.Error(), err
			}
			log.Info(fmt.Sprintf("[reconcileStorageClassDeleteFunc] successfully deleted a storage class, name: %s", sc.Name))
		}

		if !slices.Contains(allowedProvisioners, sc.Provisioner) {
			log.Info(fmt.Sprintf("[reconcileStorageClassDeleteFunc] a storage class %s with provisioner %s does not belong to allowed provisioners: %v. Skip deletion of storage class", sc.Name, sc.Provisioner, allowedProvisioners))
		}
	}

	_, err = removeFinalizerIfExists(ctx, cl, cephSC, CephStorageClassControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassDeleteFunc] unable to remove a finalizer %s from the CephStorageClass %s: %w", CephStorageClassControllerFinalizerName, cephSC.Name, err)
		return true, err.Error(), err
	}

	log.Debug("[reconcileStorageClassDeleteFunc] ends the reconciliation")
	return false, "", nil
}

func ConfigureStorageClass(cephSC *storagev1alpha1.CephStorageClass, controllerNamespace, clusterID string) (*v1.StorageClass, error) {
	provisioner := GetStorageClassProvisioner(cephSC.Spec.Type)
	allowVolumeExpansion := true
	reclaimPolicy := corev1.PersistentVolumeReclaimPolicy(cephSC.Spec.ReclaimPolicy)
	volumeBindingMode := v1.VolumeBindingImmediate

	params, err := GetStoragecClassParams(cephSC, controllerNamespace, clusterID)
	if err != nil {
		err = fmt.Errorf("CephStorageClass %q: unable to get a storage class parameters: %w", cephSC.Name, err)
		return nil, err
	}

	mountOpt := storagev1alpha1.DefaultMountOptions

	sc := &v1.StorageClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       StorageClassKind,
			APIVersion: StorageClassAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       cephSC.Name,
			Namespace:  cephSC.Namespace,
			Finalizers: []string{CephStorageClassControllerFinalizerName},
			Labels: map[string]string{
				internal.StorageManagedLabelKey: CephStorageClassCtrlName,
			},
		},
		Parameters:           params,
		Provisioner:          provisioner,
		ReclaimPolicy:        &reclaimPolicy,
		VolumeBindingMode:    &volumeBindingMode,
		AllowVolumeExpansion: &allowVolumeExpansion,
		MountOptions:         mountOpt,
	}

	return sc, nil
}

func GetStorageClassProvisioner(cephStorageClasstype string) string {
	provisioner := ""
	switch cephStorageClasstype {
	case storagev1alpha1.CephStorageClassTypeRBD:
		provisioner = CephStorageClassRBDProvisioner
	case storagev1alpha1.CephStorageClassTypeCephFS:
		provisioner = CephStorageClassCephFSProvisioner
	}

	return provisioner

}

func GetStoragecClassParams(cephSC *storagev1alpha1.CephStorageClass, controllerNamespace, clusterID string) (map[string]string, error) {
	secretName := internal.CephClusterAuthenticationSecretPrefix + cephSC.Spec.ClusterAuthenticationName

	params := map[string]string{
		"clusterID": clusterID,
		"csi.storage.k8s.io/provisioner-secret-name":            secretName,
		"csi.storage.k8s.io/provisioner-secret-namespace":       controllerNamespace,
		"csi.storage.k8s.io/controller-expand-secret-name":      secretName,
		"csi.storage.k8s.io/controller-expand-secret-namespace": controllerNamespace,
		"csi.storage.k8s.io/node-stage-secret-name":             secretName,
		"csi.storage.k8s.io/node-stage-secret-namespace":        controllerNamespace,
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeRBD {
		params["imageFeatures"] = "layering"
		params["csi.storage.k8s.io/fstype"] = cephSC.Spec.RBD.DefaultFSType
		params["pool"] = cephSC.Spec.RBD.Pool
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeCephFS {
		params["fsName"] = cephSC.Spec.CephFS.FSName
	}

	return params, nil
}

func updateCephStorageClassPhase(ctx context.Context, cl client.Client, cephSC *storagev1alpha1.CephStorageClass, phase, reason string) error {
	if cephSC.Status == nil {
		cephSC.Status = &storagev1alpha1.CephStorageClassStatus{}
	}
	cephSC.Status.Phase = phase
	cephSC.Status.Reason = reason

	// TODO: add retry logic
	err := cl.Status().Update(ctx, cephSC)
	if err != nil {
		return err
	}

	return nil
}

func createStorageClassIfNotExists(ctx context.Context, cl client.Client, scList *v1.StorageClassList, sc *v1.StorageClass) (bool, error) {
	for _, s := range scList.Items {
		if s.Name == sc.Name {
			return false, nil
		}
	}

	err := cl.Create(ctx, sc)
	if err != nil {
		return false, err
	}

	return true, err
}

func GetSCDiff(oldSC, newSC *v1.StorageClass) (string, error) {

	if oldSC.Provisioner != newSC.Provisioner {
		err := fmt.Errorf("CephStorageClass %q: the provisioner field is different in the StorageClass %q", newSC.Name, oldSC.Name)
		return "", err
	}

	if *oldSC.ReclaimPolicy != *newSC.ReclaimPolicy {
		diff := fmt.Sprintf("ReclaimPolicy: %q -> %q", *oldSC.ReclaimPolicy, *newSC.ReclaimPolicy)
		return diff, nil
	}

	if *oldSC.VolumeBindingMode != *newSC.VolumeBindingMode {
		diff := fmt.Sprintf("VolumeBindingMode: %q -> %q", *oldSC.VolumeBindingMode, *newSC.VolumeBindingMode)
		return diff, nil
	}

	if *oldSC.AllowVolumeExpansion != *newSC.AllowVolumeExpansion {
		diff := fmt.Sprintf("AllowVolumeExpansion: %t -> %t", *oldSC.AllowVolumeExpansion, *newSC.AllowVolumeExpansion)
		return diff, nil
	}

	if !reflect.DeepEqual(oldSC.Parameters, newSC.Parameters) {
		diff := fmt.Sprintf("Parameters: %+v -> %+v", oldSC.Parameters, newSC.Parameters)
		return diff, nil
	}

	if !reflect.DeepEqual(oldSC.MountOptions, newSC.MountOptions) {
		diff := fmt.Sprintf("MountOptions: %v -> %v", oldSC.MountOptions, newSC.MountOptions)
		return diff, nil
	}

	return "", nil
}

func recreateStorageClass(ctx context.Context, cl client.Client, oldSC, newSC *v1.StorageClass) error {
	// It is necessary to pass the original StorageClass to the delete operation because
	// the deletion will not succeed if the fields in the StorageClass provided to delete
	// differ from those currently in the cluster.
	err := deleteStorageClass(ctx, cl, oldSC)
	if err != nil {
		err = fmt.Errorf("[recreateStorageClass] unable to delete a storage class %s: %s", oldSC.Name, err.Error())
		return err
	}

	err = cl.Create(ctx, newSC)
	if err != nil {
		err = fmt.Errorf("[recreateStorageClass] unable to create a storage class %s: %s", newSC.Name, err.Error())
		return err
	}

	return nil
}

func deleteStorageClass(ctx context.Context, cl client.Client, sc *v1.StorageClass) error {
	if !slices.Contains(allowedProvisioners, sc.Provisioner) {
		return fmt.Errorf("a storage class %s with provisioner %s does not belong to allowed provisioners: %v", sc.Name, sc.Provisioner, allowedProvisioners)
	}

	_, err := removeFinalizerIfExists(ctx, cl, sc, CephStorageClassControllerFinalizerName)
	if err != nil {
		return err
	}

	err = cl.Delete(ctx, sc)
	if err != nil {
		return err
	}

	return nil
}

func validateCephStorageClassSpec(cephSC *storagev1alpha1.CephStorageClass) (bool, string) {
	if cephSC.DeletionTimestamp != nil {
		return true, ""
	}

	var (
		failedMsgBuilder strings.Builder
		validationPassed = true
	)

	failedMsgBuilder.WriteString("Validation of CephStorageClass failed: ")

	if cephSC.Spec.ClusterConnectionName == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.clusterConnectionName field is empty; ")
	}

	if cephSC.Spec.ClusterAuthenticationName == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.clusterAuthenticationName field is empty; ")
	}

	if cephSC.Spec.ReclaimPolicy == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.reclaimPolicy field is empty; ")
	}

	if cephSC.Spec.Type == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.type field is empty; ")
	}

	switch cephSC.Spec.Type {
	case storagev1alpha1.CephStorageClassTypeRBD:
		if cephSC.Spec.RBD == nil {
			validationPassed = false
			failedMsgBuilder.WriteString(fmt.Sprintf("CephStorageClass type is %s but the spec.rbd field is empty; ", storagev1alpha1.CephStorageClassTypeRBD))
		} else {
			if cephSC.Spec.RBD.DefaultFSType == "" {
				validationPassed = false
				failedMsgBuilder.WriteString("the spec.rbd.defaultFSType field is empty; ")
			}

			if cephSC.Spec.RBD.Pool == "" {
				validationPassed = false
				failedMsgBuilder.WriteString("the spec.rbd.pool field is empty; ")
			}
		}
	case storagev1alpha1.CephStorageClassTypeCephFS:
		if cephSC.Spec.CephFS == nil {
			validationPassed = false
			failedMsgBuilder.WriteString(fmt.Sprintf("CephStorageClass type is %s but the spec.cephfs field is empty; ", storagev1alpha1.CephStorageClassTypeRBD))
		} else {
			if cephSC.Spec.CephFS.FSName == "" {
				validationPassed = false
				failedMsgBuilder.WriteString("the spec.cephfs.fsName field is empty; ")
			}
		}
	default:
		validationPassed = false
		failedMsgBuilder.WriteString(fmt.Sprintf("the spec.type field is not valid: %s. Allowed values: %s, %s", cephSC.Spec.Type, storagev1alpha1.CephStorageClassTypeRBD, storagev1alpha1.CephStorageClassTypeCephFS))
	}

	return validationPassed, failedMsgBuilder.String()
}

func getClusterID(ctx context.Context, cl client.Client, cephSC *storagev1alpha1.CephStorageClass) (string, error) {
	clusterConnectionName := cephSC.Spec.ClusterConnectionName
	clusterConnection := &storagev1alpha1.CephClusterConnection{}
	err := cl.Get(ctx, client.ObjectKey{Namespace: cephSC.Namespace, Name: clusterConnectionName}, clusterConnection)
	if err != nil {
		err = fmt.Errorf("[getClusterID] CephStorageClass %q: unable to get a CephClusterConnection %q: %w", cephSC.Name, clusterConnectionName, err)
		return "", err
	}

	clusterID := clusterConnection.Spec.ClusterID
	if clusterID == "" {
		err = fmt.Errorf("[getClusterID] CephStorageClass %q: the CephClusterConnection %q has an empty spec.clusterID field", cephSC.Name, clusterConnectionName)
		return "", err
	}

	return clusterID, nil
}

func updateStorageClass(cephSC *storagev1alpha1.CephStorageClass, oldSC *v1.StorageClass, controllerNamespace, clusterID string) (*v1.StorageClass, error) {
	newSC, err := ConfigureStorageClass(cephSC, controllerNamespace, clusterID)
	if err != nil {
		err = fmt.Errorf("[updateStorageClass] unable to configure a Storage Class for the CephStorageClass %s: %w", cephSC.Name, err)
		return nil, err
	}

	if oldSC.Annotations != nil {
		newSC.Annotations = oldSC.Annotations
	}

	return newSC, nil
}
