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
	storagev1alpha1 "d8-controller/api/v1alpha1"
	"d8-controller/pkg/logger"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"slices"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ReconcileStorageClassCreateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	cephSC *storagev1alpha1.CephStorageClass,
	controllerNamespace string,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] starts for CephStorageClass %q", cephSC.Name))
	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] starts storage class configuration for the CephStorageClass, name: %s", cephSC.Name))
	newSC, err := ConfigureStorageClass(cephSC, controllerNamespace)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassCreateFunc] unable to configure a Storage Class for the CephStorageClass %s: %w", cephSC.Name, err)
		upError := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileStorageClassCreateFunc] unable to update the CephStorageClass %s: %w", cephSC.Name, upError)
			err = errors.Join(err, upError)
		}
		return false, err
	}

	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] successfully configurated storage class for the CephStorageClass, name: %s", cephSC.Name))
	log.Trace(fmt.Sprintf("[reconcileStorageClassCreateFunc] storage class: %+v", newSC))

	created, err := createStorageClassIfNotExists(ctx, cl, scList, newSC)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassCreateFunc] unable to create a Storage Class %s: %w", newSC.Name, err)
		upError := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileStorageClassCreateFunc] unable to update the CephStorageClass %s: %w", cephSC.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}
	log.Debug(fmt.Sprintf("[reconcileStorageClassCreateFunc] a storage class %s was created: %t", newSC.Name, created))
	if created {
		log.Info(fmt.Sprintf("[reconcileStorageClassCreateFunc] successfully create storage class, name: %s", newSC.Name))
	} else {
		log.Warning(fmt.Sprintf("[reconcileLSCCreateFunc] Storage class %s already exists. Adding event to requeue.", newSC.Name))
		return true, nil
	}

	return false, nil
}

func reconcileStorageClassUpdateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	cephSC *storagev1alpha1.CephStorageClass,
	controllerNamespace string,
) (bool, error) {

	log.Debug(fmt.Sprintf("[reconcileStorageClassUpdateFunc] starts for CephStorageClass %q", cephSC.Name))

	var oldSC *v1.StorageClass
	for _, s := range scList.Items {
		if s.Name == cephSC.Name {
			oldSC = &s
			break
		}
	}

	if oldSC == nil {
		err := fmt.Errorf("a storage class %s does not exist", cephSC.Name)
		err = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to find a storage class for the CephStorageClass %s: %w", cephSC.Name, err)
		upError := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to update the CephStorageClass %s: %w", cephSC.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}

	log.Debug(fmt.Sprintf("[reconcileStorageClassUpdateFunc] successfully found a storage class for the CephStorageClass, name: %s", cephSC.Name))

	log.Trace(fmt.Sprintf("[reconcileStorageClassUpdateFunc] storage class: %+v", oldSC))
	newSC, err := ConfigureStorageClass(cephSC, controllerNamespace)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to configure a Storage Class for the CephStorageClass %s: %w", cephSC.Name, err)
		upError := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to update the CephStorageClass %s: %w", cephSC.Name, upError)
			err = errors.Join(err, upError)
		}
		return false, err
	}

	diff, err := GetSCDiff(oldSC, newSC)
	if err != nil {
		err = fmt.Errorf("[reconcileStorageClassUpdateFunc] error occured while identifying the difference between the existed StorageClass %s and the new one: %w", newSC.Name, err)
		upError := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to update the CephStorageClass %s: %w", cephSC.Name, upError)
			err = errors.Join(err, upError)
		}
		return true, err
	}

	if diff != "" {
		log.Info(fmt.Sprintf("[reconcileStorageClassUpdateFunc] current Storage Class LVMVolumeGroups do not match CephStorageClass ones. The Storage Class %s will be recreated with new ones", cephSC.Name))

		err = recreateStorageClass(ctx, cl, oldSC, newSC)
		if err != nil {
			err = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to recreate a Storage Class %s: %w", newSC.Name, err)
			upError := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, err.Error())
			if upError != nil {
				upError = fmt.Errorf("[reconcileStorageClassUpdateFunc] unable to update the CephStorageClass %s: %w", cephSC.Name, upError)
				err = errors.Join(err, upError)
			}
			return true, err
		}

		log.Info(fmt.Sprintf("[reconcileStorageClassUpdateFunc] a Storage Class %s was successfully recreated", newSC.Name))
	}

	return false, nil
}

func IdentifyReconcileFuncForStorageClass(log logger.Logger, scList *v1.StorageClassList, cephSC *storagev1alpha1.CephStorageClass, controllerNamespace string) (reconcileType string, err error) {
	if shouldReconcileByDeleteFunc(cephSC) {
		return DeleteReconcile, nil
	}

	if shouldReconcileStorageClassByCreateFunc(scList, cephSC) {
		return CreateReconcile, nil
	}

	should, err := shouldReconcileStorageClassByUpdateFunc(log, scList, cephSC, controllerNamespace)
	if err != nil {
		return "", err
	}
	if should {
		return UpdateReconcile, nil
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

func shouldReconcileStorageClassByUpdateFunc(log logger.Logger, scList *v1.StorageClassList, cephSC *storagev1alpha1.CephStorageClass, controllerNamespace string) (bool, error) {
	if cephSC.DeletionTimestamp != nil {
		return false, nil
	}

	for _, oldSC := range scList.Items {
		if oldSC.Name == cephSC.Name {
			if slices.Contains(allowedProvisioners, oldSC.Provisioner) {
				newSC, err := ConfigureStorageClass(cephSC, controllerNamespace)
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

				if cephSC.Status != nil && cephSC.Status.Phase == FailedStatusPhase {
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

func reconcileStorageClassDeleteFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	scList *v1.StorageClassList,
	cephSC *storagev1alpha1.CephStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileStorageClassDeleteFunc] tries to find a storage class for the CephStorageClass %s", cephSC.Name))
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
				upErr := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, fmt.Sprintf("Unable to delete a storage class, err: %s", err.Error()))
				if upErr != nil {
					upErr = fmt.Errorf("[reconcileStorageClassDeleteFunc] unable to update the CephStorageClass %s: %w", cephSC.Name, upErr)
					err = errors.Join(err, upErr)
				}
				return true, err
			}
			log.Info(fmt.Sprintf("[reconcileStorageClassDeleteFunc] successfully deleted a storage class, name: %s", sc.Name))
		}

		if !slices.Contains(allowedProvisioners, sc.Provisioner) {
			log.Info(fmt.Sprintf("[reconcileStorageClassDeleteFunc] the storage class %s provisioner %s does not belong to allowed provisioners: %v", sc.Name, sc.Provisioner, allowedProvisioners))

		}
	}

	log.Debug("[reconcileStorageClassDeleteFunc] ends the reconciliation")
	return false, nil
}

func shouldReconcileByDeleteFunc(cephSC *storagev1alpha1.CephStorageClass) bool {
	if cephSC.DeletionTimestamp != nil {
		return true
	}

	return false
}

func removeFinalizerIfExists(ctx context.Context, cl client.Client, obj metav1.Object, finalizerName string) (bool, error) {
	removed := false
	finalizers := obj.GetFinalizers()
	for i, f := range finalizers {
		if f == finalizerName {
			finalizers = append(finalizers[:i], finalizers[i+1:]...)
			removed = true
			break
		}
	}

	if removed {
		obj.SetFinalizers(finalizers)
		err := cl.Update(ctx, obj.(client.Object))
		if err != nil {
			return false, err
		}
	}

	return removed, nil
}

func addFinalizerIfNotExists(ctx context.Context, cl client.Client, obj metav1.Object, finalizerName string) (bool, error) {
	added := false
	finalizers := obj.GetFinalizers()
	if !slices.Contains(finalizers, finalizerName) {
		finalizers = append(finalizers, finalizerName)
		added = true
	}

	if added {
		obj.SetFinalizers(finalizers)
		err := cl.Update(ctx, obj.(client.Object))
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func ConfigureStorageClass(cephSC *storagev1alpha1.CephStorageClass, controllerNamespace string) (*v1.StorageClass, error) {
	if cephSC.Spec.ReclaimPolicy == "" {
		err := fmt.Errorf("CephStorageClass %q: the ReclaimPolicy field is empty", cephSC.Name)
		return nil, err
	}

	if cephSC.Spec.AllowVolumeExpansion == "" {
		err := fmt.Errorf("CephStorageClass %q: the AllowVolumeExpansion field is empty", cephSC.Name)
		return nil, err
	}

	provisioner, err := GetStorageClassProvisioner(cephSC)
	if err != nil {
		err = fmt.Errorf("CephStorageClass %q: unable to get a provisioner: %w", cephSC.Name, err)
		return nil, err
	}

	allowVolumeExpansion, err := strconv.ParseBool(cephSC.Spec.AllowVolumeExpansion)
	if err != nil {
		err = fmt.Errorf("CephStorageClass %q: the AllowVolumeExpansion field is not a boolean value: %w", cephSC.Name, err)
		return nil, err
	}

	reclaimPolicy := corev1.PersistentVolumeReclaimPolicy(cephSC.Spec.ReclaimPolicy)
	volumeBindingMode := v1.VolumeBindingWaitForFirstConsumer

	params, err := GetStoragecClassParams(cephSC, controllerNamespace)
	if err != nil {
		err = fmt.Errorf("CephStorageClass %q: unable to get a storage class parameters: %w", cephSC.Name, err)
		return nil, err
	}

	sc := &v1.StorageClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       StorageClassKind,
			APIVersion: StorageClassAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:       cephSC.Name,
			Namespace:  cephSC.Namespace,
			Finalizers: []string{CephStorageClassControllerFinalizerName},
		},
		Parameters:           params,
		MountOptions:         cephSC.Spec.MountOptions,
		Provisioner:          provisioner,
		ReclaimPolicy:        &reclaimPolicy,
		VolumeBindingMode:    &volumeBindingMode,
		AllowVolumeExpansion: &allowVolumeExpansion,
	}

	return sc, nil
}

func GetStorageClassProvisioner(cephSC *storagev1alpha1.CephStorageClass) (string, error) {
	if cephSC.Spec.Type == "" {
		err := fmt.Errorf("CephStorageClass %q: the Type field is empty", cephSC.Name)
		return "", err
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeRBD && cephSC.Spec.RBD == nil {
		err := fmt.Errorf("CephStorageClass %q type is %q, but the rbd field is empty", cephSC.Name, storagev1alpha1.CephStorageClassTypeRBD)
		return "", err
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeCephFS && cephSC.Spec.CephFS == nil {
		err := fmt.Errorf("CephStorageClass %q type is %q, but the cephfs field is empty", cephSC.Name, storagev1alpha1.CephStorageClassTypeCephFS)
		return "", err
	}

	provisioner := ""
	switch cephSC.Spec.Type {
	case storagev1alpha1.CephStorageClassTypeRBD:
		provisioner = CephStorageClassRBDProvisioner
	case storagev1alpha1.CephStorageClassTypeCephFS:
		provisioner = CephStorageClassCephFSProvisioner
	default:
		err := fmt.Errorf("CephStorageClass %q: the Type field is not valid: %s", cephSC.Name, cephSC.Spec.Type)
		return "", err
	}

	return provisioner, nil

}

func GetStoragecClassParams(cephSC *storagev1alpha1.CephStorageClass, controllerNamespace string) (map[string]string, error) {

	if cephSC.Spec.ClusterName == "" {
		err := errors.New("CephStorageClass ClusterName is empty")
		return nil, err
	}

	if cephSC.Spec.Pool == "" {
		err := errors.New("CephStorageClass Pool is empty")
		return nil, err
	}

	secretName := fmt.Sprintf("csi-ceph-secret-for-%s", cephSC.Spec.ClusterName)

	params := map[string]string{
		"clusterID": cephSC.Spec.ClusterName,
		"pool":      cephSC.Spec.Pool,
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
