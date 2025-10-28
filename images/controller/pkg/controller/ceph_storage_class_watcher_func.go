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
	"slices"
	"strings"

	"github.com/google/go-cmp/cmp"
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/logger"
)

var (
	defaultImageFeatures = "layering,exclusive-lock,object-map,fast-diff"
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
				newSC := updateStorageClass(cephSC, &oldSC, controllerNamespace, clusterID)
				diff, err := GetSCDiff(&oldSC, newSC)
				if err != nil {
					return false, err
				}

				if diff != "" {
					log.Debug(fmt.Sprintf("[shouldReconcileStorageClassByUpdateFunc] a storage class %s should be updated. Diff: %s", oldSC.Name, diff))
					return true, nil
				}

				if cephSC.Status != nil && cephSC.Status.Phase == internal.PhaseFailed {
					return true, nil
				}

				return false, nil
			}
			err := fmt.Errorf("a storage class %s with provisioner % s does not belong to allowed provisioners: %v", oldSC.Name, oldSC.Provisioner, allowedProvisioners)
			return false, err
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
	newSC := ConfigureStorageClass(cephSC, clusterID)

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

	newSC := updateStorageClass(cephSC, oldSC, controllerNamespace, clusterID)
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

	log.Debug("[reconcileStorageClassDeleteFunc] ends the reconciliation")
	return false, "", nil
}

func ConfigureStorageClass(cephSC *storagev1alpha1.CephStorageClass, clusterID string) *v1.StorageClass {
	provisioner := GetStorageClassProvisioner(cephSC.Spec.Type)
	allowVolumeExpansion := true
	reclaimPolicy := corev1.PersistentVolumeReclaimPolicy(cephSC.Spec.ReclaimPolicy)
	volumeBindingMode := v1.VolumeBindingImmediate

	params := GetStoragecClassParams(cephSC, clusterID)
	sc := &v1.StorageClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       StorageClassKind,
			APIVersion: StorageClassAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cephSC.Name,
			Namespace: cephSC.Namespace,
			Annotations: map[string]string{
				internal.CephStorageClassVolumeSnapshotClassAnnotationKey: cephSC.Name,
			},
			Finalizers: []string{CephStorageClassControllerFinalizerName},
		},
		Parameters:           params,
		Provisioner:          provisioner,
		ReclaimPolicy:        &reclaimPolicy,
		VolumeBindingMode:    &volumeBindingMode,
		AllowVolumeExpansion: &allowVolumeExpansion,
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeRBD {
		sc.MountOptions = storagev1alpha1.DefaultMountOptionsRBD
	}

	if cephSC.Labels != nil {
		sc.Labels = cephSC.Labels
		sc.Labels[internal.StorageManagedLabelKey] = CephStorageClassCtrlName
	} else {
		sc.Labels = map[string]string{
			internal.StorageManagedLabelKey: CephStorageClassCtrlName,
		}
	}

	return sc
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

func GetStoragecClassParams(cephSC *storagev1alpha1.CephStorageClass, clusterID string) map[string]string {
	params := map[string]string{
		"clusterID": clusterID,
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeRBD {
		params["imageFeatures"] = defaultImageFeatures
		params["csi.storage.k8s.io/fstype"] = cephSC.Spec.RBD.DefaultFSType
		params["pool"] = cephSC.Spec.RBD.Pool
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeCephFS {
		params["fsName"] = cephSC.Spec.CephFS.FSName
	}

	return params
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

	if !cmp.Equal(oldSC.Parameters, newSC.Parameters) {
		diff := fmt.Sprintf("Parameters: %+v -> %+v", oldSC.Parameters, newSC.Parameters)
		return diff, nil
	}

	if !cmp.Equal(oldSC.MountOptions, newSC.MountOptions) {
		diff := fmt.Sprintf("MountOptions: %v -> %v", oldSC.MountOptions, newSC.MountOptions)
		return diff, nil
	}

	if !cmp.Equal(oldSC.Labels, newSC.Labels) {
		diff := fmt.Sprintf("Labels: %+v -> %+v", oldSC.Labels, newSC.Labels)
		return diff, nil
	}

	if !cmp.Equal(oldSC.Annotations, newSC.Annotations) {
		diff := fmt.Sprintf("Annotations: %+v -> %+v", oldSC.Annotations, newSC.Annotations)
		return diff, nil
	}

	if !cmp.Equal(oldSC.Finalizers, newSC.Finalizers) {
		diff := fmt.Sprintf("Finalizers: %+v -> %+v", oldSC.Finalizers, newSC.Finalizers)
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

	err := removeFinalizerIfExists(ctx, cl, sc, CephStorageClassControllerFinalizerName)
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
		} else if cephSC.Spec.CephFS.FSName == "" {
			validationPassed = false
			failedMsgBuilder.WriteString("the spec.cephfs.fsName field is empty; ")
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

func updateStorageClass(cephSC *storagev1alpha1.CephStorageClass, oldSC *v1.StorageClass, controllerNamespace, clusterID string) *v1.StorageClass {
	newSC := ConfigureStorageClass(cephSC, clusterID)

	if oldSC.Annotations != nil {
		newSC.Annotations = oldSC.Annotations
	}

	return newSC
}

// VolumeSnaphotClass
func IdentifyReconcileFuncForVSClass(log logger.Logger, vsClassList *snapshotv1.VolumeSnapshotClassList, cephSC *storagev1alpha1.CephStorageClass, clusterID, controllerNamespace string) (reconcileType string, oldVSClass, newVSClass *snapshotv1.VolumeSnapshotClass) {
	oldVSClass = findVSClass(vsClassList, cephSC.Name)

	if oldVSClass == nil {
		log.Debug(fmt.Sprintf("[IdentifyReconcileFuncForVSClass] no volume snapshot class found for the CephStorageClass %s", cephSC.Name))
	} else {
		log.Debug(fmt.Sprintf("[IdentifyReconcileFuncForVSClass] finds old volume snapshot class for the CephStorageClass %s", cephSC.Name))
		log.Trace(fmt.Sprintf("[IdentifyReconcileFuncForVSClass] old volume snapshot class: %+v", oldVSClass))
	}

	newVSClass = ConfigureVSClass(oldVSClass, cephSC, clusterID, controllerNamespace)
	log.Debug(fmt.Sprintf("[IdentifyReconcileFuncForVSClass] successfully configurated new volume snapshot class for the CephStorageClass %s", cephSC.Name))
	log.Trace(fmt.Sprintf("[IdentifyReconcileFuncForVSClass] new volume snapshot class: %+v", newVSClass))

	if shouldReconcileVSClassByCreateFunc(oldVSClass, cephSC) {
		return internal.CreateReconcile, nil, newVSClass
	}

	if shouldReconcileVSClassByUpdateFunc(log, oldVSClass, newVSClass, cephSC) {
		return internal.UpdateReconcile, oldVSClass, newVSClass
	}

	return "", oldVSClass, newVSClass
}

func shouldReconcileVSClassByCreateFunc(oldVSClass *snapshotv1.VolumeSnapshotClass, cephSC *storagev1alpha1.CephStorageClass) bool {
	if cephSC.DeletionTimestamp != nil {
		return false
	}

	if oldVSClass != nil {
		return false
	}

	return true
}

func shouldReconcileVSClassByUpdateFunc(log logger.Logger, oldVSClass, newVSClass *snapshotv1.VolumeSnapshotClass, cephSC *storagev1alpha1.CephStorageClass) bool {
	if cephSC.DeletionTimestamp != nil {
		return false
	}

	if oldVSClass == nil {
		return false
	}

	diff := CompareVSClasses(oldVSClass, newVSClass)
	if diff != "" {
		log.Debug(fmt.Sprintf("[shouldReconcileVSClassByUpdateFunc] a volume snapshot class %s should be updated. Diff: %s", oldVSClass.Name, diff))
		return true
	}

	return false
}

func findVSClass(vsClassList *snapshotv1.VolumeSnapshotClassList, name string) *snapshotv1.VolumeSnapshotClass {
	for _, vsClass := range vsClassList.Items {
		if vsClass.Name == name {
			return &vsClass
		}
	}
	return nil
}

func ConfigureVSClass(oldVSClass *snapshotv1.VolumeSnapshotClass, cephSC *storagev1alpha1.CephStorageClass, clusterID, controllerNamespace string) *snapshotv1.VolumeSnapshotClass {
	deletionPolicy := snapshotv1.DeletionPolicy(cephSC.Spec.ReclaimPolicy)
	provisioner := GetStorageClassProvisioner(cephSC.Spec.Type)
	params := GetVSClassParams(cephSC, clusterID, controllerNamespace)

	newVSClass := &snapshotv1.VolumeSnapshotClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: cephSC.Name,
			Labels: map[string]string{
				CephStorageClassManagedLabelKey: CephStorageClassManagedLabelValue,
			},
			Finalizers: []string{CephStorageClassControllerFinalizerName},
		},
		Driver:         provisioner,
		DeletionPolicy: deletionPolicy,
		Parameters:     params,
	}

	if oldVSClass != nil {
		if oldVSClass.Labels != nil {
			newVSClass.Labels = labels.Merge(oldVSClass.Labels, newVSClass.Labels)
		}
		if oldVSClass.Annotations != nil {
			newVSClass.Annotations = oldVSClass.Annotations
		}
	}

	return newVSClass
}

func CompareVSClasses(vsClass, newVSClass *snapshotv1.VolumeSnapshotClass) string {
	var diffs []string

	if vsClass.DeletionPolicy != newVSClass.DeletionPolicy {
		diffs = append(diffs, fmt.Sprintf("DeletionPolicy: %s -> %s", vsClass.DeletionPolicy, newVSClass.DeletionPolicy))
	}

	if !cmp.Equal(vsClass.Parameters, newVSClass.Parameters) {
		diffs = append(diffs,
			fmt.Sprintf("Parameters diff: %s", cmp.Diff(vsClass.Parameters, newVSClass.Parameters)))
	}

	if !cmp.Equal(vsClass.ObjectMeta.Labels, newVSClass.ObjectMeta.Labels) {
		diffs = append(diffs,
			fmt.Sprintf("Labels diff: %s", cmp.Diff(vsClass.ObjectMeta.Labels, newVSClass.ObjectMeta.Labels)))
	}

	if !cmp.Equal(vsClass.ObjectMeta.Annotations, newVSClass.ObjectMeta.Annotations) {
		diffs = append(diffs,
			fmt.Sprintf("Annotations diff: %s", cmp.Diff(vsClass.ObjectMeta.Annotations, newVSClass.ObjectMeta.Annotations)))
	}

	return strings.Join(diffs, ", ")
}

func reconcileVolumeSnapshotClassCreateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	newVSClass *snapshotv1.VolumeSnapshotClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileVolumeSnapshotClassCreateFunc] starts for VolumeSnapshotClass %q", newVSClass.Name))
	log.Trace(fmt.Sprintf("[reconcileVolumeSnapshotClassCreateFunc] volume snapshot class: %+v", newVSClass))

	err := cl.Create(ctx, newVSClass)
	if err != nil {
		err = fmt.Errorf("[reconcileVolumeSnapshotClassCreateFunc] unable to create a VolumeSnapshotClass %s: %w", newVSClass.Name, err)
		return true, err
	}

	log.Info(fmt.Sprintf("[reconcileVolumeSnapshotClassCreateFunc] successfully create volume snapshot class, name: %s", newVSClass.Name))

	return false, nil
}

func reconcileVolumeSnapshotClassUpdateFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	oldVSClass *snapshotv1.VolumeSnapshotClass,
	newVSClass *snapshotv1.VolumeSnapshotClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileVolumeSnapshotClassUpdateFunc] starts for VolumeSnapshotClass %q", newVSClass.Name))

	newVSClass.ObjectMeta.ResourceVersion = oldVSClass.ObjectMeta.ResourceVersion
	err := cl.Update(ctx, newVSClass)
	if err != nil {
		err = fmt.Errorf("[reconcileVolumeSnapshotClassUpdateFunc] unable to update a VolumeSnapshotClass %s: %w", newVSClass.Name, err)
		return true, err
	}

	log.Info(fmt.Sprintf("[reconcileVolumeSnapshotClassUpdateFunc] successfully updated a VolumeSnapshotClass, name: %s", newVSClass.Name))

	return false, nil
}

func reconcileVolumeSnapshotClassDeleteFunc(
	ctx context.Context,
	cl client.Client,
	log logger.Logger,
	oldVSClass *snapshotv1.VolumeSnapshotClass,
	cephSC *storagev1alpha1.CephStorageClass,
) (bool, error) {
	log.Debug(fmt.Sprintf("[reconcileVolumeSnapshotClassDeleteFunc] starts for VolumeSnapshotClass %q", cephSC.Name))

	if oldVSClass == nil {
		log.Info(fmt.Sprintf("[reconcileVolumeSnapshotClassDeleteFunc] no volume snapshot class found for the CephStorageClass, name: %s", cephSC.Name))
		log.Debug("[reconcileVolumeSnapshotClassDeleteFunc] ends the reconciliation")
		return false, nil
	}

	log.Info(fmt.Sprintf("[reconcileVolumeSnapshotClassDeleteFunc] successfully found a volume snapshot class %s for the CephStorageClass %s", oldVSClass.Name, cephSC.Name))
	log.Trace(fmt.Sprintf("[reconcileVolumeSnapshotClassDeleteFunc] volume snapshot class: %+v", oldVSClass))

	if !slices.Contains(allowedProvisioners, oldVSClass.Driver) {
		log.Info(fmt.Sprintf("[reconcileVolumeSnapshotClassDeleteFunc] the volume snapshot class %s driver %s does not belong to allowed provisioners: %v. Skip deletion", oldVSClass.Name, oldVSClass.Driver, allowedProvisioners))
		return false, nil
	}

	err := deleteVolumeSnapshotClass(ctx, cl, oldVSClass)
	if err != nil {
		err = fmt.Errorf("[reconcileVolumeSnapshotClassDeleteFunc] unable to delete a volume snapshot class %s: %w", oldVSClass.Name, err)
		return true, err
	}

	log.Info(fmt.Sprintf("[reconcileVolumeSnapshotClassDeleteFunc] successfully deleted a volume snapshot class, name: %s", oldVSClass.Name))

	log.Debug("[reconcileVolumeSnapshotClassDeleteFunc] ends the reconciliation")
	return false, nil
}

func deleteVolumeSnapshotClass(ctx context.Context, cl client.Client, vsClass *snapshotv1.VolumeSnapshotClass) error {
	if !slices.Contains(allowedProvisioners, vsClass.Driver) {
		return fmt.Errorf("a volume snapshot class %s with driver %s does not belong to allowed provisioners: %v", vsClass.Name, vsClass.Driver, allowedProvisioners)
	}

	err := removeFinalizerIfExists(ctx, cl, vsClass, CephStorageClassControllerFinalizerName)
	if err != nil {
		return err
	}

	err = cl.Delete(ctx, vsClass)
	if err != nil {
		return err
	}

	return nil
}

func GetVSClassParams(cephSC *storagev1alpha1.CephStorageClass, clusterID, controllerNamespace string) map[string]string {
	secretName := internal.CephClusterConnectionSecretPrefix + cephSC.Spec.ClusterConnectionName

	params := map[string]string{
		"clusterID":                               clusterID,
		internal.CSISnapshotterSecretNameKey:      secretName,
		internal.CSISnapshotterSecretNamespaceKey: controllerNamespace,
	}

	if cephSC.Spec.Type == storagev1alpha1.CephStorageClassTypeRBD {
		params["imageFeatures"] = defaultImageFeatures
	}

	return params
}
