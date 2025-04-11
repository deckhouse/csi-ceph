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

package funcs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/go-cmp/cmp"
	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	oldApi "github.com/deckhouse/csi-ceph/hooks/go/api/v1alpha1"
)

const (
	ModuleNamespace = "d8-csi-ceph"

	LabelValueTrue  = "true"
	LabelValueFalse = "false"

	VSClassParametersClusterIDKey = "clusterID"

	CSISnapshotterSecretNameKey      = "csi.storage.k8s.io/snapshotter-secret-name"
	CSISnapshotterSecretNamespaceKey = "csi.storage.k8s.io/snapshotter-secret-namespace"

	VSCAnnotationDeletionSecretName      = "snapshot.storage.kubernetes.io/deletion-secret-name"
	VSCAnnotationDeletionSecretNamespace = "snapshot.storage.kubernetes.io/deletion-secret-namespace"

	BackupDateLabelKey   = "storage.deckhouse.io/backup-date"
	BackupSourceLabelKey = "storage.deckhouse.io/backup-source"

	ResourceKindLabelKey         = "storage.deckhouse.io/resource-kind"
	ResourceKindPersistentVolume = "PersistentVolume"

	PVRecreatedSuccesfullyLabelKey = "storage.deckhouse.io/pv-recreated-successfully"

	PVAnnotationProvisionerDeletionSecretNamespace = "volume.kubernetes.io/provisioner-deletion-secret-namespace"
	PVAnnotationProvisionerDeletionSecretName      = "volume.kubernetes.io/provisioner-deletion-secret-name"
)

var (
	AllowedProvisioners = []string{
		"rbd.csi.ceph.com",
		"cephfs.csi.ceph.com",
	}
)

func NewKubeClient() (client.Client, error) {
	var config *rest.Config
	var err error

	config, err = clientcmd.BuildConfigFromFlags("", "")

	if err != nil {
		return nil, err
	}

	var (
		resourcesSchemeFuncs = []func(*apiruntime.Scheme) error{
			v1alpha1.AddToScheme,
			clientgoscheme.AddToScheme,
			extv1.AddToScheme,
			v1.AddToScheme,
			sv1.AddToScheme,
			snapv1.AddToScheme,
			oldApi.AddToScheme,
		}
	)

	scheme := apiruntime.NewScheme()
	for _, f := range resourcesSchemeFuncs {
		err = f(scheme)
		if err != nil {
			return nil, err
		}
	}

	clientOpts := client.Options{
		Scheme: scheme,
	}

	return client.New(config, clientOpts)
}

func MigrateVSClassesAndVSContentsToNewSecret(ctx context.Context, cl client.Client, migratedLabelKey, oldSecretPrefix, newSecretPrefix, logPrefix string) error {
	vsClassList := &snapv1.VolumeSnapshotClassList{}
	err := cl.List(ctx, vsClassList)
	if err != nil {
		fmt.Printf("[%s]: VolumeSnapshotClassList get error %s\n", logPrefix, err)
		return err
	}

	vsContentList := &snapv1.VolumeSnapshotContentList{}
	err = cl.List(ctx, vsContentList)
	if err != nil {
		fmt.Printf("[%s]: VolumeSnapshotContentList get error %s\n", logPrefix, err)
		return err
	}

	cephClusterConnectionList := &v1alpha1.CephClusterConnectionList{}
	err = cl.List(ctx, cephClusterConnectionList)
	if err != nil {
		fmt.Printf("[%s]: CephClusterConnectionList get error %s\n", logPrefix, err)
		return err
	}

	for _, vsClass := range vsClassList.Items {
		if !slices.Contains(AllowedProvisioners, vsClass.Driver) {
			fmt.Printf("[%s]: VolumeSnapshotClass %s has not allowed driver %s. Skipping\n", logPrefix, vsClass.Name, vsClass.Driver)
			continue
		}

		if vsClass.Labels[migratedLabelKey] == LabelValueTrue {
			fmt.Printf("[%s]: VolumeSnapshotClass %s already migrated. Skipping\n", logPrefix, vsClass.Name)
			continue
		}

		fmt.Printf("[%s]: Migrating VolumeSnapshotClass %s\n", logPrefix, vsClass.Name)
		oldSecretName := vsClass.Parameters[CSISnapshotterSecretNameKey]
		fmt.Printf("[%s]: Finding new secret name for VolumeSnapshotClass %s by old secret name %s and old prefix %s\n", logPrefix, vsClass.Name, oldSecretName, oldSecretPrefix)
		clusterConnectionName := getClusterConnectionNameByOldSecretName(oldSecretName, oldSecretPrefix, cephClusterConnectionList.Items)
		if clusterConnectionName == "" {
			clusterID := vsClass.Parameters[VSClassParametersClusterIDKey]
			fmt.Printf("[%s]: cannot find CephClusterConnection by secretName %s. Trying to find by clusterID %s\n", logPrefix, oldSecretName, clusterID)
			clusterConnectionName := getClusterConnectionNameByClusterID(clusterID, cephClusterConnectionList.Items)
			if clusterConnectionName == "" {
				fmt.Printf("[%s]: cannot find CephClusterConnection by clusterID %s. Labeling VolumeSnapshotClass %s with %s=%s and skipping\n", logPrefix, clusterID, vsClass.Name, migratedLabelKey, LabelValueFalse)

				newVSClass := vsClass.DeepCopy()
				if newVSClass.Labels == nil {
					newVSClass.Labels = map[string]string{}
				}
				newVSClass.Labels[migratedLabelKey] = LabelValueFalse
				err = UpdateVolumeSnapshotClassIfNeeded(ctx, cl, &vsClass, newVSClass, logPrefix)
				if err != nil {
					fmt.Printf("[%s]: VolumeSnapshotClass %s update error %s\n", logPrefix, vsClass.Name, err)
					return err
				}
				continue
			}
		}

		newSecretName := newSecretPrefix + clusterConnectionName
		fmt.Printf("[%s]: New secret name for VolumeSnapshotClass %s is %s\n", logPrefix, vsClass.Name, newSecretName)

		newVSClass := vsClass.DeepCopy()
		newVSClass.Parameters[CSISnapshotterSecretNameKey] = newSecretName
		newVSClass.Parameters[CSISnapshotterSecretNamespaceKey] = ModuleNamespace
		if newVSClass.Labels == nil {
			newVSClass.Labels = map[string]string{}
		}
		newVSClass.Labels[migratedLabelKey] = LabelValueTrue

		err = UpdateVolumeSnapshotClassIfNeeded(ctx, cl, &vsClass, newVSClass, logPrefix)
		if err != nil {
			fmt.Printf("[%s]: VolumeSnapshotClass %s update error %s\n", logPrefix, vsClass.Name, err)
			return err
		}

		fmt.Printf("[%s]: VolumeSnapshotClass %s migrated. Processing VolumeSnapshotContent\n", logPrefix, vsClass.Name)
		err = MigrateVSContentsToNewSecret(ctx, cl, vsContentList, vsClass.Name, newSecretName, migratedLabelKey, logPrefix)
		if err != nil {
			fmt.Printf("[%s]: MigrateVSContentsToNewSecret error %s\n", logPrefix, err)
			return err
		}

		fmt.Printf("[%s]: VolumeSnapshotClass %s and VolumeSnapshotContent for it migrated\n", logPrefix, vsClass.Name)
	}

	return nil
}

func getClusterConnectionNameByClusterID(clusterID string, cephClusterConnectionList []v1alpha1.CephClusterConnection) string {
	if clusterID == "" {
		return ""
	}

	for _, cephClusterConnection := range cephClusterConnectionList {
		if cephClusterConnection.Spec.ClusterID == clusterID {
			return cephClusterConnection.Name
		}
	}

	return ""
}

func getClusterConnectionNameByOldSecretName(secretName, secretPrefix string, cephClusterConnectionList []v1alpha1.CephClusterConnection) string {
	if secretName == "" {
		return ""
	}

	cephClusterConnectionName := strings.TrimPrefix(secretName, secretPrefix)
	if cephClusterConnectionName == secretName {
		return ""
	}

	for _, cephClusterConnection := range cephClusterConnectionList {
		if cephClusterConnection.Name == cephClusterConnectionName {
			return cephClusterConnection.Name
		}
	}

	return ""
}

func GetClusterConnectionByName(clusterConnectionList *v1alpha1.CephClusterConnectionList, clusterConnectionName, logPrefix string) *v1alpha1.CephClusterConnection {
	for _, clusterConnection := range clusterConnectionList.Items {
		if clusterConnection.Name == clusterConnectionName {
			fmt.Printf("[%s]: Found CephClusterConnection %s\n", logPrefix, clusterConnectionName)
			return &clusterConnection
		}
	}

	fmt.Printf("[%s]: CephClusterConnection %s not found\n", logPrefix, clusterConnectionName)
	return nil
}

func GetCephStorageClassByName(scList *v1alpha1.CephStorageClassList, cephStorageClassName, logPrefix string) *v1alpha1.CephStorageClass {
	for _, sc := range scList.Items {
		if sc.Name == cephStorageClassName {
			fmt.Printf("[%s]: Found CephStorageClass %s\n", logPrefix, cephStorageClassName)
			return &sc
		}
	}

	fmt.Printf("[%s]: CephStorageClass %s not found\n", logPrefix, cephStorageClassName)
	return nil
}

func UpdateVolumeSnapshotClassIfNeeded(ctx context.Context, cl client.Client, oldVSClass, newVSClass *snapv1.VolumeSnapshotClass, logPrefix string) error {
	if cmp.Equal(oldVSClass, newVSClass) {
		fmt.Printf("[%s]: VolumeSnapshotClass %s is up-to-date. Skipping update\n", logPrefix, oldVSClass.Name)
		return nil
	}

	fmt.Printf("[%s]: Updating VolumeSnapshotClass %s\n", logPrefix, oldVSClass.Name)
	err := cl.Update(ctx, newVSClass)
	if err != nil {
		return err
	}

	fmt.Printf("[%s]: VolumeSnapshotClass %s updated\n", logPrefix, oldVSClass.Name)
	return nil
}

func MigrateVSContentsToNewSecret(ctx context.Context, cl client.Client, vsContentList *snapv1.VolumeSnapshotContentList, vsClassName, newSecretName, migratedLabelKey, logPrefix string) error {
	for _, vsContent := range vsContentList.Items {
		fmt.Printf("[%s]: Processing VolumeSnapshotContent %s\n", logPrefix, vsContent.Name)
		if !slices.Contains(AllowedProvisioners, vsContent.Spec.Driver) {
			fmt.Printf("[%s]: VolumeSnapshotContent %s has not allowed driver %s (allowed: %v). Skipping\n", logPrefix, vsContent.Name, vsContent.Spec.Driver, AllowedProvisioners)
			continue
		}

		if vsContent.Spec.VolumeSnapshotClassName == nil {
			fmt.Printf("[%s]: VolumeSnapshotContent %s doesn't have VolumeSnapshotClassName. Skipping\n", logPrefix, vsContent.Name)
			continue
		}

		if *vsContent.Spec.VolumeSnapshotClassName != vsClassName {
			fmt.Printf("[%s]: VolumeSnapshotContent %s has different VolumeSnapshotClassName %s (expected: %s). Skipping\n", logPrefix, vsContent.Name, *vsContent.Spec.VolumeSnapshotClassName, vsClassName)
			continue
		}

		if vsContent.Labels[migratedLabelKey] == LabelValueTrue {
			fmt.Printf("[%s]: VolumeSnapshotContent %s already migrated. Skipping\n", logPrefix, vsContent.Name)
			continue
		}

		newVolumeSnapshotContent := vsContent.DeepCopy()
		if newVolumeSnapshotContent.Labels == nil {
			newVolumeSnapshotContent.Labels = make(map[string]string)
		}
		newVolumeSnapshotContent.Labels[migratedLabelKey] = LabelValueTrue

		if newVolumeSnapshotContent.Annotations == nil {
			newVolumeSnapshotContent.Annotations = make(map[string]string)
		}
		newVolumeSnapshotContent.Annotations[VSCAnnotationDeletionSecretName] = newSecretName
		newVolumeSnapshotContent.Annotations[VSCAnnotationDeletionSecretNamespace] = ModuleNamespace

		err := UpdateVolumeSnapshotContentIfNeeded(ctx, cl, &vsContent, newVolumeSnapshotContent, logPrefix)
		if err != nil {
			fmt.Printf("[%s]: updateVolumeSnapshotContentIfNeeded error %s\n", logPrefix, err.Error())
			return err
		}
		fmt.Printf("[%s]: VolumeSnapshotContent %s migrated\n", logPrefix, vsContent.Name)
	}

	return nil
}

func UpdateVolumeSnapshotContentIfNeeded(ctx context.Context, cl client.Client, oldVSContent, newVSContent *snapv1.VolumeSnapshotContent, logPrefix string) error {
	if cmp.Equal(oldVSContent, newVSContent) {
		fmt.Printf("[%s]: VolumeSnapshotContent %s doesn't need update\n", logPrefix, oldVSContent.Name)
		return nil
	}

	fmt.Printf("[%s]: Updating VolumeSnapshotContent %s\n", logPrefix, oldVSContent.Name)
	err := cl.Update(ctx, newVSContent)
	if err != nil {
		fmt.Printf("[%s]: VolumeSnapshotContent update error %s\n", logPrefix, err.Error())
		return err
	}

	fmt.Printf("[%s]: VolumeSnapshotContent %s updated\n", logPrefix, oldVSContent.Name)
	return nil
}

func ProcessRemovedPVs(ctx context.Context, cl client.Client, backupSourceName, logPrefix string) error {
	cephMetadataBackupList := &v1alpha1.CephMetadataBackupList{}
	labels := map[string]string{
		BackupSourceLabelKey:           backupSourceName,
		ResourceKindLabelKey:           ResourceKindPersistentVolume,
		PVRecreatedSuccesfullyLabelKey: LabelValueFalse,
	}
	err := cl.List(ctx, cephMetadataBackupList, client.MatchingLabels(labels))
	if err != nil {
		fmt.Printf("[%s]: CephMetadataBackupList get error %s\n", logPrefix, err.Error())
		return err
	}

	if len(cephMetadataBackupList.Items) == 0 {
		fmt.Printf("[%s]: Not found deleted and not migrated PVs\n", logPrefix)
		return nil
	}

	fmt.Printf("[%s]: Found %d backups of PVs that probably were removed and not migrated. Check them\n", logPrefix, len(cephMetadataBackupList.Items))
	for _, backup := range cephMetadataBackupList.Items {
		fmt.Printf("[%s]: Processing backup %s\n", logPrefix, backup.Name)

		obj := &v1.PersistentVolume{}
		data, err := base64.StdEncoding.DecodeString(backup.Spec.Data)
		if err != nil {
			fmt.Printf("[%s]: Decode backup data error %s\n", logPrefix, err.Error())
			return err
		}

		err = json.Unmarshal(data, obj)
		if err != nil {
			fmt.Printf("[%s]: Unmarshal backup data error %s\n", logPrefix, err.Error())
			return err
		}

		err = cl.Create(ctx, obj)
		if err != nil {
			if client.IgnoreAlreadyExists(err) != nil {
				fmt.Printf("[%s]: PV create error %s\n", logPrefix, err.Error())
				return err
			}
			fmt.Printf("[%s]: PV %s already exists\n", logPrefix, obj.Name)
		} else {
			fmt.Printf("[%s]: PV %s created\n", logPrefix, obj.Name)
		}

		fmt.Printf("[%s]: Updating backup %s with %s=%s\n", logPrefix, backup.Name, PVRecreatedSuccesfullyLabelKey, LabelValueTrue)
		backup.Labels[PVRecreatedSuccesfullyLabelKey] = LabelValueTrue
		err = cl.Update(ctx, &backup)
		if err != nil {
			fmt.Printf("[%s]: Update backup %s error %s\n", logPrefix, backup.Name, err.Error())
			return err
		}
	}
	return nil
}

func BackupResource(ctx context.Context, cl client.Client, backupObj runtime.Object, backupTime time.Time, backupSourceName, logPrefix string) (string, error) {
	cleanObj := backupObj.DeepCopyObject()
	err := sanitizeObject(cleanObj)
	if err != nil {
		return "", fmt.Errorf("failed to sanitize object: %w", err)
	}

	datetime := backupTime.Format("20060102-150405")
	metaObj, err := meta.Accessor(cleanObj)
	if err != nil {
		return "", fmt.Errorf("failed to get object metadata: %w", err)
	}

	gvk, err := apiutil.GVKForObject(cleanObj, cl.Scheme())
	if err != nil {
		return "", fmt.Errorf("failed to get GVK for object: %w", err)
	}
	resourceKind := gvk.Kind

	fmt.Printf("[%s]: Backup resource %s %s\n", logPrefix, resourceKind, metaObj.GetName())

	backupName := fmt.Sprintf("%s-%s-%s", datetime, strings.ToLower(resourceKind), metaObj.GetName())
	fmt.Printf("[%s]: Backup name %s\n", logPrefix, backupName)

	cleanObj.GetObjectKind().SetGroupVersionKind(gvk)
	jsonBytes, err := json.Marshal(cleanObj)
	if err != nil {
		return "", fmt.Errorf("failed to marshal object: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(jsonBytes)

	backup := &v1alpha1.CephMetadataBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name: backupName,
			Labels: map[string]string{
				BackupDateLabelKey:   datetime,
				BackupSourceLabelKey: backupSourceName,
				ResourceKindLabelKey: resourceKind,
			},
		},
		Spec: v1alpha1.CephMetadataBackupSpec{
			Data: encoded,
		},
	}

	err = retry.OnError(retry.DefaultBackoff, func(err error) bool {
		return true
	}, func() error {
		err := cl.Create(ctx, backup)
		return err
	})

	if err != nil {
		return "", fmt.Errorf("failed to create backup: %w", err)
	}

	fmt.Printf("[%s]: Resource successfully backed up with name %s\n", logPrefix, backupName)

	return backupName, nil
}

func sanitizeObject(obj runtime.Object) error {
	metaObj, err := meta.Accessor(obj)
	if err != nil {
		return fmt.Errorf("failed to get metadata: %w", err)
	}

	metaObj.SetResourceVersion("")
	metaObj.SetUID("")
	metaObj.SetCreationTimestamp(metav1.Time{})
	metaObj.SetManagedFields(nil)
	metaObj.SetFinalizers(nil)

	return nil
}

func MigratePVsToNewSecret(ctx context.Context, cl client.Client, pvList *v1.PersistentVolumeList, backupTime time.Time, scName, newSecretName, backupSourceName, logPrefix string) error {
	fmt.Printf("[%s]: Started migration PersistentVolumes to new secret %s in namespace %s\n", logPrefix, newSecretName, ModuleNamespace)

	for i := range pvList.Items {
		pv := &pvList.Items[i]
		if pv.Spec.CSI == nil {
			fmt.Printf("[%s]: PV %s doesn't have CSI field. Skipping\n", logPrefix, pv.Name)
			continue
		}

		if !slices.Contains(AllowedProvisioners, pv.Spec.CSI.Driver) {
			fmt.Printf("[%s]: PV %s has not allowed provisioner %s (allowed: %v). Skipping\n", logPrefix, pv.Name, pv.Spec.CSI.Driver, AllowedProvisioners)
			continue
		}

		if pv.Spec.StorageClassName != scName {
			fmt.Printf("[%s]: PV %s uses different storage class %s (expected: %s). Skipping\n", logPrefix, pv.Name, pv.Spec.StorageClassName, scName)
			continue
		}

		needRecreate := false
		if pv.Spec.CSI.ControllerExpandSecretRef != nil {
			if pv.Spec.CSI.ControllerExpandSecretRef.Namespace != ModuleNamespace || pv.Spec.CSI.ControllerExpandSecretRef.Name != newSecretName {
				fmt.Printf("[%s]: PV %s has different ControllerExpandSecretRef %s/%s (expected: %s/%s)\n", logPrefix, pv.Name, pv.Spec.CSI.ControllerExpandSecretRef.Namespace, pv.Spec.CSI.ControllerExpandSecretRef.Name, ModuleNamespace, newSecretName)
				needRecreate = true
			}
		}

		if pv.Spec.CSI.NodeStageSecretRef != nil {
			if pv.Spec.CSI.NodeStageSecretRef.Namespace != ModuleNamespace || pv.Spec.CSI.NodeStageSecretRef.Name != newSecretName {
				fmt.Printf("[%s]: PV %s has different NodeStageSecretRef %s/%s (expected: %s/%s)\n", logPrefix, pv.Name, pv.Spec.CSI.NodeStageSecretRef.Namespace, pv.Spec.CSI.NodeStageSecretRef.Name, ModuleNamespace, newSecretName)
				needRecreate = true
			}
		}

		needUpdate := false
		if pv.Annotations != nil {
			if pv.Annotations[PVAnnotationProvisionerDeletionSecretNamespace] != ModuleNamespace || pv.Annotations[PVAnnotationProvisionerDeletionSecretName] != newSecretName {
				fmt.Printf("[%s]: PV %s has different provision deletion secret %s/%s (expected: %s/%s)\n", logPrefix, pv.Name, pv.Annotations[PVAnnotationProvisionerDeletionSecretNamespace], pv.Annotations[PVAnnotationProvisionerDeletionSecretName], ModuleNamespace, newSecretName)
				needUpdate = true
			}
		} else {
			pv.Annotations = make(map[string]string)
			fmt.Printf("[%s]: PV %s doesn't have annotations\n", logPrefix, pv.Name)
			needUpdate = true
		}

		if needRecreate {
			fmt.Printf("[%s]: Recreating PV %s\n", logPrefix, pv.Name)
			backupName, err := BackupResource(ctx, cl, pv, backupTime, backupSourceName, logPrefix)
			if err != nil {
				fmt.Printf("[%s]: Backup PV %s error %s\n", logPrefix, pv.Name, err)
				return err
			}

			err = setRecreateLabelToBackupResource(ctx, cl, backupName, LabelValueFalse)
			if err != nil {
				fmt.Printf("[%s]: setRecreateLabelToBackupResource error %s\n", logPrefix, err)
				return err
			}

			newPV := pv.DeepCopy()
			newPV.ResourceVersion = ""
			newPV.UID = ""
			newPV.CreationTimestamp = metav1.Time{}
			newPV.Spec.CSI.NodeStageSecretRef.Namespace = ModuleNamespace
			newPV.Spec.CSI.NodeStageSecretRef.Name = newSecretName
			newPV.Spec.CSI.ControllerExpandSecretRef.Namespace = ModuleNamespace
			newPV.Spec.CSI.ControllerExpandSecretRef.Name = newSecretName
			newPV.Annotations[PVAnnotationProvisionerDeletionSecretNamespace] = ModuleNamespace
			newPV.Annotations[PVAnnotationProvisionerDeletionSecretName] = newSecretName

			patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":[]},"spec":{"persistentVolumeReclaimPolicy":"Retain"}}`))
			if err := cl.Patch(ctx, pv, patch); err != nil {
				return fmt.Errorf("failed to remove finalizers: %w", err)
			}

			err = cl.Delete(ctx, pv)
			if err != nil {
				fmt.Printf("[%s]: PV delete error %s\n", logPrefix, err)
				return err
			}

			err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
				fmt.Printf("[%s]: Waiting for PersistentVolume %s to be created\n", logPrefix, newPV.Name)

				newPV.ResourceVersion = ""
				err = cl.Create(ctx, newPV)
				if err != nil {
					if apierrors.IsAlreadyExists(err) {
						fmt.Printf("[%s]: Waiting for PersistentVolume %s to be deleted\n", logPrefix, newPV.Name)
						return false, nil
					}

					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						fmt.Printf("[%s]: Context canceled while creating PersistentVolume\n", logPrefix)
						return false, nil
					}

					return false, err
				}

				fmt.Printf("[%s]: PersistentVolume %s created\n", logPrefix, newPV.Name)
				return true, nil
			})

			if err != nil {
				fmt.Printf("[%s]: PV create error %s\n", logPrefix, err)
				return err
			}
			fmt.Printf("[%s]: PV %s successfully migrated\n", logPrefix, pv.Name)

			err = setRecreateLabelToBackupResource(ctx, cl, backupName, LabelValueTrue)
			if err != nil {
				fmt.Printf("[%s]: setRecreateLabelToBackupResource error %s\n", logPrefix, err)
				return err
			}
		} else if needUpdate {
			fmt.Printf("[%s]: Updating PV %s\n", logPrefix, pv.Name)
			err := cl.Update(ctx, pv)
			if err != nil {
				fmt.Printf("[%s]: PV update error %s\n", logPrefix, err)
				return err
			}
			fmt.Printf("[%s]: PV %s successfully migrated\n", logPrefix, pv.Name)
		} else {
			fmt.Printf("[%s]: PV %s doesn't need migration\n", logPrefix, pv.Name)
		}
	}

	return nil
}

func DisableCSIComponents(ctx context.Context, cl client.Client, logPrefix string) error {

	deploymentList := &appsv1.DeploymentList{}
	err := cl.List(ctx, deploymentList, client.InNamespace(ModuleNamespace), client.MatchingLabels{"app": "csi-controller"})
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			fmt.Printf("[%s]: DeploymentList not found: %s. Skipping disabling CSI components\n", logPrefix, err.Error())
			return nil
		}
		fmt.Printf("[%s]: DeploymentList get error %s\n", logPrefix, err)
		return err
	}

	for _, deployment := range deploymentList.Items {
		fmt.Printf("[%s]: Deleting deployment csi-controller %s in namespace %s\n", logPrefix, deployment.Name, deployment.Namespace)

		deployment.SetFinalizers([]string{})
		err = cl.Update(ctx, &deployment)
		if err != nil {
			fmt.Printf("[%s]: Deployment update error %s\n", logPrefix, err)
			return err
		}

		err = cl.Delete(ctx, &deployment)
		if err != nil {
			fmt.Printf("[%s]: Deployment delete error %s\n", logPrefix, err)
			return err
		}
	}

	daemonSetList := &appsv1.DaemonSetList{}
	err = cl.List(ctx, daemonSetList, client.InNamespace(ModuleNamespace), client.MatchingLabels{"app": "csi-node"})
	if err != nil {
		fmt.Printf("[%s]: DaemonSetList get error %s\n", logPrefix, err)
		return err
	}

	for _, daemonSet := range daemonSetList.Items {
		fmt.Printf("[%s]: Deleting daemonset csi-node %s in namespace %s\n", logPrefix, daemonSet.Name, daemonSet.Namespace)

		daemonSet.SetFinalizers([]string{})
		err = cl.Update(ctx, &daemonSet)
		if err != nil {
			fmt.Printf("[%s]: DaemonSet update error %s\n", logPrefix, err)
			return err
		}

		err = cl.Delete(ctx, &daemonSet)
		if err != nil {
			fmt.Printf("[%s]: DaemonSet delete error %s\n", logPrefix, err)
			return err
		}
	}

	podLabelSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      "app",
				Operator: metav1.LabelSelectorOpIn,
				Values: []string{
					"csi-controller-cephfs",
					"csi-controller-rbd",
					"csi-node-cephfs",
					"csi-node-rbd",
				},
			},
		},
	}

	podSelector, err := metav1.LabelSelectorAsSelector(podLabelSelector)
	if err != nil {
		return err
	}

	err = wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		podList := &v1.PodList{}

		err = cl.List(ctx, podList, client.InNamespace(ModuleNamespace), client.MatchingLabelsSelector{Selector: podSelector})
		if err != nil {
			fmt.Printf("[%s]: PodList get error %s\n", logPrefix, err)
			return false, err
		}

		if len(podList.Items) == 0 {
			return true, nil
		}

		fmt.Printf("[%s]: Waiting for all pods to be deleted\n", logPrefix)
		return false, nil
	})

	if err != nil {
		fmt.Printf("[%s]: Waiting for all pods to be deleted error: %s\n", logPrefix, err)
		return err
	}

	fmt.Printf("[%s]: All pods are deleted\n", logPrefix)

	return nil
}

func setRecreateLabelToBackupResource(ctx context.Context, cl client.Client, backupName, labelValue string) error {
	cephMetadataBackup := &v1alpha1.CephMetadataBackup{}
	err := cl.Get(ctx, types.NamespacedName{Name: backupName}, cephMetadataBackup)
	if err != nil {
		return err
	}

	if cephMetadataBackup.Labels == nil {
		cephMetadataBackup.Labels = make(map[string]string)
	}

	cephMetadataBackup.Labels[PVRecreatedSuccesfullyLabelKey] = labelValue
	err = cl.Update(ctx, cephMetadataBackup)
	if err != nil {
		return err
	}

	return nil
}

func ScaleDeployment(ctx context.Context, cl client.Client, namespace, deploymentName string, replicas *int32) error {
	deployment := &appsv1.Deployment{}
	err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: deploymentName}, deployment)
	if err != nil {
		fmt.Printf("Cannot get prometheus-operator deployment: %s\n", err.Error())
		return err
	}

	fmt.Printf("Scaling prometheus-operator deployment replicas down\n")
	deployment.Spec.Replicas = replicas
	err = cl.Update(ctx, deployment)
	if err != nil {
		fmt.Printf("Cannot update prometheus-operator deployment: %s\n", err.Error())
		return err
	}

	return nil
}
