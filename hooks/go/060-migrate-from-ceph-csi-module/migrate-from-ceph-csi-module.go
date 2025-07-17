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

package hooks_common

import (
	"context"
	"fmt"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	oldApi "github.com/deckhouse/csi-ceph/hooks/go/api/v1alpha1"
	funcs "github.com/deckhouse/csi-ceph/hooks/go/funcs"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
)

const (
	// CephClusterAuthenticationMigratedLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthentication"
	// CephClusterAuthenticationMigratedLabelValue = "true"
	CephCSIMigratedLabel          = "storage.deckhouse.io/migratedFromCephCSI"
	CephCSIMigratedLabelValueTrue = "true"

	CephCSIMigrateBackupSource = "migrate-from-ceph-csi-module"

	CephCSISecretPrefix = "csi-"
	CSICephSecretPrefix = "csi-ceph-secret-for-"

	MountOptionsLabelKey         = "storage.deckhouse.io/mount-options"
	AllowVolumeExpansionLabelKey = "storage.deckhouse.io/allow-volume-expansion"
	CephFSPoolLabelKey           = "storage.deckhouse.io/cephfs-pool"

	LogPrefix          = "migrate-from-ceph-csi-module"
	DiscardMountOption = "discard"

	CephFSDefaultPool                = "cephfs_data"
	prometheusOperatorNamespace      = "d8-operator-prometheus"
	prometheusOperatorDeploymentName = "prometheus-operator"
)

var (
	BackupTime = time.Now()
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateFromCephCsiModule)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func handlerMigrateFromCephCsiModule(ctx context.Context, _ *pkg.HookInput) error {
	fmt.Printf("[%s]: Started migration from Ceph CSI module\n", LogPrefix)

	scalePrometheusOperatorUp := false

	cl, err := funcs.NewKubeClient()
	if err != nil {
		fmt.Printf("%s", err.Error())
		return err
	}

	err = funcs.ProcessRemovedPVs(ctx, cl, CephCSIMigrateBackupSource, LogPrefix)
	if err != nil {
		fmt.Printf("[%s]: processRemovedPVs error %s\n", LogPrefix, err.Error())
		return err
	}

	cephCSIDriverListToMigrate := &oldApi.CephCSIDriverList{}
	req, err := labels.NewRequirement(CephCSIMigratedLabel, selection.NotEquals, []string{CephCSIMigratedLabelValueTrue})
	if err != nil {
		return fmt.Errorf("failed to create label requirement: %w", err)
	}
	selector := labels.NewSelector().Add(*req)

	err = cl.List(ctx, cephCSIDriverListToMigrate, &client.ListOptions{LabelSelector: selector})
	if err != nil {
		if meta.IsNoMatchError(err) {
			fmt.Printf("[%s]: CephCSIDriverList not found\n", LogPrefix)
		} else {
			fmt.Printf("[%s]: CephCSIDriverList get error %s\n", LogPrefix, err.Error())
			return err
		}
	}

	pvList := &v1.PersistentVolumeList{}
	cephClusterConnectionList := &v1alpha1.CephClusterConnectionList{}
	cephStorageClassList := &v1alpha1.CephStorageClassList{}

	if len(cephCSIDriverListToMigrate.Items) != 0 {
		fmt.Printf("[%s]: Found %d CephCSIDrivers to migrate. Starting migration for them\n", LogPrefix, len(cephCSIDriverListToMigrate.Items))
		fmt.Printf("[%s]: Disabling csi controllers and csi node for cephfs and rbd in namespace %s\n", LogPrefix, funcs.ModuleNamespace)
		err := funcs.DisableCSIComponents(ctx, cl, LogPrefix)
		if err != nil {
			fmt.Printf("[%s]: DisableCSIComponents error %s\n", LogPrefix, err.Error())
			return err
		}
		err = cl.List(ctx, pvList)
		if err != nil {
			fmt.Printf("[%s]: PVList get error %s\n", LogPrefix, err.Error())
			return err
		}

		err = cl.List(ctx, cephClusterConnectionList)
		if err != nil {
			fmt.Printf("[%s]: CephClusterConnectionList get error %s\n", LogPrefix, err.Error())
			return err
		}

		err = cl.List(ctx, cephStorageClassList)
		if err != nil {
			fmt.Printf("[%s]: CephStorageClassList get error %s\n", LogPrefix, err.Error())
			return err
		}
	} else {
		fmt.Printf("[%s]: No CephCSIDrivers found\n", LogPrefix)
	}

	for _, cephCSIDriver := range cephCSIDriverListToMigrate.Items {
		if cephCSIDriver.Labels[CephCSIMigratedLabel] == CephCSIMigratedLabelValueTrue {
			continue
		}

		if !scalePrometheusOperatorUp {
			err = funcs.ScaleDeployment(ctx, cl, prometheusOperatorNamespace, prometheusOperatorDeploymentName, &[]int32{0}[0])
			if err == nil {
				scalePrometheusOperatorUp = true
			}
		}

		fmt.Printf("[%s]: Processing CephCSIDriver %s\n", LogPrefix, cephCSIDriver.Name)

		cephClusterConnectionName := cephCSIDriver.Name
		newSecretName := CSICephSecretPrefix + cephClusterConnectionName
		fmt.Printf("[%s]: New secret name %s\n", LogPrefix, newSecretName)

		cephClusterConnection := funcs.GetClusterConnectionByName(cephClusterConnectionList, cephClusterConnectionName, LogPrefix)
		if cephClusterConnection == nil {
			fmt.Printf("[%s]: Creating new CephClusterConnection\n", LogPrefix)
			cephClusterConnection = &v1alpha1.CephClusterConnection{}
			cephClusterConnection.Name = cephClusterConnectionName
			cephClusterConnection.Labels = map[string]string{CephCSIMigratedLabel: CephCSIMigratedLabelValueTrue}
			cephClusterConnection.Spec = v1alpha1.CephClusterConnectionSpec{
				Monitors:  cephCSIDriver.Spec.Monitors,
				ClusterID: cephCSIDriver.Spec.ClusterID,
				UserID:    cephCSIDriver.Spec.UserID,
				UserKey:   cephCSIDriver.Spec.UserKey}
			if cephCSIDriver.Spec.CephFS != nil {
				if cephCSIDriver.Spec.CephFS.SubvolumeGroup != "" {
					cephClusterConnection.Spec = v1alpha1.CephClusterConnectionSpec{
						Monitors:       cephCSIDriver.Spec.Monitors,
						ClusterID:      cephCSIDriver.Spec.ClusterID,
						UserID:         cephCSIDriver.Spec.UserID,
						UserKey:        cephCSIDriver.Spec.UserKey,
						SubvolumeGroup: cephCSIDriver.Spec.CephFS.SubvolumeGroup}

				}
			}
			err := cl.Create(ctx, cephClusterConnection)
			if err != nil {
				fmt.Printf("[%s]: cephClusterConnection create error %s\n", LogPrefix, err.Error())
				return err
			}
		}

		if cephCSIDriver.Spec.CephFS != nil {
			for _, cephFSStorageClass := range cephCSIDriver.Spec.CephFS.StorageClasses {
				cephStorageClassName := fmt.Sprintf("%s-%s", cephClusterConnectionName, cephFSStorageClass.NamePostfix)
				fmt.Printf("[%s]: Processing CephFS StorageClass %s\n", LogPrefix, cephStorageClassName)

				cephStorageClass := funcs.GetCephStorageClassByName(cephStorageClassList, cephStorageClassName, LogPrefix)
				if cephStorageClass == nil {
					fmt.Printf("[%s]: Creating new CephStorageClass\n", LogPrefix)

					cephStorageClass = &v1alpha1.CephStorageClass{}
					cephStorageClass.Name = cephStorageClassName
					cephStorageClass.Labels = map[string]string{CephCSIMigratedLabel: CephCSIMigratedLabelValueTrue}
					cephStorageClass.Spec = v1alpha1.CephStorageClassSpec{
						ClusterConnectionName: cephClusterConnectionName,
						Type:                  v1alpha1.CephStorageClassTypeCephFS,
						CephFS: &v1alpha1.CephStorageClassCephFS{
							FSName: cephFSStorageClass.FsName,
						},
						ReclaimPolicy: cephFSStorageClass.ReclaimPolicy,
					}

					if cephFSStorageClass.Pool != "" {
						if cephFSStorageClass.Pool != CephFSDefaultPool {
							fmt.Printf("[%s]: Pool is not empty and is not equal to the default pool (%s). Please contact tech support for migration assistance.\n", LogPrefix, CephFSDefaultPool)
							return fmt.Errorf("pool is not empty and is not equal to the default pool (%s). Please contact tech support for migration assistance", CephFSDefaultPool)
						}
						cephStorageClass.Labels[CephFSPoolLabelKey] = cephFSStorageClass.Pool
					}

					if len(cephFSStorageClass.MountOptions) > 0 {
						cephStorageClass.Labels[MountOptionsLabelKey] = strings.Join(cephFSStorageClass.MountOptions, ",")
					}

					if cephFSStorageClass.AllowVolumeExpansion != nil && !*cephFSStorageClass.AllowVolumeExpansion {
						cephStorageClass.Labels[AllowVolumeExpansionLabelKey] = "false"
					}

					err := cl.Create(ctx, cephStorageClass)
					if err != nil {
						fmt.Printf("[%s]: CephStorageClass create error %s\n", LogPrefix, err.Error())
						return err
					}
				}

				err := funcs.MigratePVsToNewSecret(ctx, cl, pvList, BackupTime, cephStorageClass.Name, newSecretName, CephCSIMigrateBackupSource, LogPrefix)
				if err != nil {
					fmt.Printf("[%s]: MigratePVsToNewSecret error %s\n", LogPrefix, err.Error())
					return err
				}
			}
		}

		if cephCSIDriver.Spec.RBD != nil {
			for _, cephRbdStorageClass := range cephCSIDriver.Spec.RBD.StorageClasses {
				cephStorageClassName := fmt.Sprintf("%s-%s", cephClusterConnectionName, cephRbdStorageClass.NamePostfix)
				fmt.Printf("[%s]: Processing CephRBD StorageClass %s\n", LogPrefix, cephStorageClassName)

				cephStorageClass := funcs.GetCephStorageClassByName(cephStorageClassList, cephStorageClassName, LogPrefix)
				if cephStorageClass == nil {
					fmt.Printf("[%s]: Creating new CephStorageClass\n", LogPrefix)

					cephStorageClass = &v1alpha1.CephStorageClass{}
					cephStorageClass.Name = cephStorageClassName
					cephStorageClass.Labels = map[string]string{CephCSIMigratedLabel: CephCSIMigratedLabelValueTrue}
					cephStorageClass.Spec = v1alpha1.CephStorageClassSpec{
						ClusterConnectionName: cephClusterConnectionName,
						Type:                  v1alpha1.CephStorageClassTypeRBD,
						RBD: &v1alpha1.CephStorageClassRBD{
							Pool:          cephRbdStorageClass.Pool,
							DefaultFSType: cephRbdStorageClass.DefaultFSType,
						},
						ReclaimPolicy: cephRbdStorageClass.ReclaimPolicy,
					}

					if len(cephRbdStorageClass.MountOptions) > 0 && !(len(cephRbdStorageClass.MountOptions) == 1 && cephRbdStorageClass.MountOptions[0] == DiscardMountOption) {
						cephStorageClass.Labels[MountOptionsLabelKey] = strings.Join(cephRbdStorageClass.MountOptions, ",")
					}

					if !*cephRbdStorageClass.AllowVolumeExpansion {
						cephStorageClass.Labels[AllowVolumeExpansionLabelKey] = "false"
					}

					err := cl.Create(ctx, cephStorageClass)
					if err != nil {
						fmt.Printf("[%s]: CephStorageClass create error %s\n", LogPrefix, err.Error())
						return err
					}
				}

				err := funcs.MigratePVsToNewSecret(ctx, cl, pvList, BackupTime, cephStorageClass.Name, newSecretName, CephCSIMigrateBackupSource, LogPrefix)
				if err != nil {
					fmt.Printf("[%s]: MigratePVsToNewSecret error %s\n", LogPrefix, err.Error())
					return err
				}
			}
		}

		if cephCSIDriver.Labels == nil {
			cephCSIDriver.Labels = make(map[string]string)
		}
		cephCSIDriver.Labels[CephCSIMigratedLabel] = CephCSIMigratedLabelValueTrue
		err = cl.Update(ctx, &cephCSIDriver)
		if err != nil {
			fmt.Printf("[%s]: CephCSIDriver update error %s\n", LogPrefix, err.Error())
			return err
		}
	}

	fmt.Printf("[%s]: Finished migration for CephCSIDrivers\n", LogPrefix)

	fmt.Printf("[%s]: Started migration for VolumeSnapshotClasses and VolumeSnapshotcontents\n", LogPrefix)
	err = funcs.MigrateVSClassesAndVSContentsToNewSecret(ctx, cl, CephCSIMigratedLabel, CephCSISecretPrefix, CSICephSecretPrefix, LogPrefix)
	if err != nil {
		fmt.Printf("[%s]: MigrateVSClassesAndVSContentsToNewSecret error %s\n", LogPrefix, err.Error())
		return err
	}
	fmt.Printf("[%s]: Finished migration for VolumeSnapshotClasses and VolumeSnapshotcontents\n", LogPrefix)

	fmt.Printf("[%s]: Finished migration from Ceph CSI module\n", LogPrefix)

	if scalePrometheusOperatorUp {
		_ = funcs.ScaleDeployment(ctx, cl, prometheusOperatorNamespace, prometheusOperatorDeploymentName, &[]int32{1}[0])
	}

	return nil
}
