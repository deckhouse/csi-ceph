package hooks_common

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"

	oldApi "csi-ceph/api/v1alpha1"
	funcs "csi-ceph/funcs"
)

const (
	// CephClusterAuthenticationMigratedLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthentication"
	// CephClusterAuthenticationMigratedLabelValue = "true"
	CephCSIMigratedLabel      = "storage.deckhouse.io/migratedFromCephCSI"
	CephCSIMigratedLabelValue = "true"

	CephCSIMigrateBackupSource = "migrate-from-ceph-csi-module"

	MountOptionsLabelKey         = "storage.deckhouse.io/mount-options"
	AllowVolumeExpansionLabelKey = "storage.deckhouse.io/allow-volume-expansion"

	LogPrefix          = "migrate-from-ceph-csi-module"
	DiscardMountOption = "discard"

	CephFSDefaultPool = "cephfs_data"
)

var (
	BackupTime = time.Now()
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateFromCephCsiModule)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func handlerMigrateFromCephCsiModule(ctx context.Context, input *pkg.HookInput) error {
	fmt.Printf("[%s]: Started migration from Ceph CSI module\n", LogPrefix)

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

	cephCSIDriverList := &oldApi.CephCSIDriverList{}

	err = cl.List(ctx, cephCSIDriverList)
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

	if len(cephCSIDriverList.Items) != 0 {
		fmt.Printf("[%s]: Found %d CephCSIDrivers. Starting migration for them\n", LogPrefix, len(cephCSIDriverList.Items))
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

	for _, cephCSIDriver := range cephCSIDriverList.Items {
		if cephCSIDriver.Labels[CephCSIMigratedLabel] == CephCSIMigratedLabelValue {
			continue
		}

		fmt.Printf("[%s]: Processing CephCSIDriver %s\n", LogPrefix, cephCSIDriver.Name)

		cephClusterConnectionName := cephCSIDriver.Name
		newSecretName := fmt.Sprintf("%s-%s", funcs.CSICephSecretPrefix, cephClusterConnectionName)

		cephClusterConnection := funcs.GetClusterConnectionByName(cephClusterConnectionList, cephClusterConnectionName, LogPrefix)
		if cephClusterConnection == nil {
			fmt.Printf("[%s]: Creating new CephClusterConnection\n", LogPrefix)
			cephClusterConnection = &v1alpha1.CephClusterConnection{}
			cephClusterConnection.Name = cephClusterConnectionName
			cephClusterConnection.Labels = map[string]string{CephCSIMigratedLabel: CephCSIMigratedLabelValue}
			cephClusterConnection.Spec = v1alpha1.CephClusterConnectionSpec{
				Monitors:  cephCSIDriver.Spec.Monitors,
				ClusterID: cephCSIDriver.Spec.ClusterID,
				UserID:    cephCSIDriver.Spec.UserID,
				UserKey:   cephCSIDriver.Spec.UserKey}
			err := cl.Create(ctx, cephClusterConnection)
			if err != nil {
				fmt.Printf("[%s]: cephClusterConnection create error %s\n", LogPrefix, err.Error())
				return err
			}
		}

		if cephCSIDriver.Spec.CephFS != nil {
			for _, cephFSStorageClass := range cephCSIDriver.Spec.CephFS.StorageClasses {
				fmt.Printf("[%s]: Processing CephFS StorageClass %s\n", LogPrefix, cephFSStorageClass.NamePostfix)

				cephStorageClassName := fmt.Sprintf("%s-%s", cephClusterConnectionName, cephFSStorageClass.NamePostfix)
				cephStorageClass := funcs.GetCephStorageClassByName(cephStorageClassList, cephStorageClassName, LogPrefix)
				if cephStorageClass == nil {
					fmt.Printf("[%s]: Creating new CephStorageClass\n", LogPrefix)

					if cephFSStorageClass.Pool != "" && cephFSStorageClass.Pool != CephFSDefaultPool {
						fmt.Printf("[%s]: Pool is not empty and not equal to default pool %s\n", LogPrefix, CephFSDefaultPool)
						return fmt.Errorf("Pool for CephFS StorageClass %s is not empty and not equal to default pool %s", cephFSStorageClass.NamePostfix, CephFSDefaultPool)
					}

					cephStorageClass = &v1alpha1.CephStorageClass{}
					cephStorageClass.Name = cephStorageClassName
					cephStorageClass.Labels = map[string]string{CephCSIMigratedLabel: CephCSIMigratedLabelValue}
					cephStorageClass.Spec = v1alpha1.CephStorageClassSpec{
						ClusterConnectionName: cephClusterConnectionName,
						Type:                  v1alpha1.CephStorageClassTypeCephFS,
						CephFS: &v1alpha1.CephStorageClassCephFS{
							FSName: cephFSStorageClass.FsName,
						},
						ReclaimPolicy: cephFSStorageClass.ReclaimPolicy,
					}

					if len(cephFSStorageClass.MountOptions) > 0 {
						cephStorageClass.Labels[MountOptionsLabelKey] = strings.Join(cephFSStorageClass.MountOptions, ",")
					}

					if !*cephFSStorageClass.AllowVolumeExpansion {
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
				fmt.Printf("[%s]: Processing CephRBD StorageClass %s\n", LogPrefix, cephRbdStorageClass.NamePostfix)

				cephStorageClassName := fmt.Sprintf("%s-%s", cephClusterConnectionName, cephRbdStorageClass.NamePostfix)
				cephStorageClass := funcs.GetCephStorageClassByName(cephStorageClassList, cephStorageClassName, LogPrefix)
				if cephStorageClass == nil {
					fmt.Printf("[%s]: Creating new CephStorageClass\n", LogPrefix)

					cephStorageClass = &v1alpha1.CephStorageClass{}
					cephStorageClass.Name = cephStorageClassName
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
		cephCSIDriver.Labels[CephCSIMigratedLabel] = CephCSIMigratedLabelValue
		err = cl.Update(ctx, &cephCSIDriver)
		if err != nil {
			fmt.Printf("[%s]: CephCSIDriver update error %s\n", LogPrefix, err.Error())
			return err
		}
	}

	fmt.Printf("[%s]: Finished migration for CephCSIDrivers\n", LogPrefix)

	fmt.Printf("[%s]: Started migration for VolumeSnapshotClasses and VolumeSnapshotcontents\n", LogPrefix)
	err = funcs.MigrateVSClassesAndVSContentsToNewSecret(ctx, cl, CephCSIMigratedLabel, LogPrefix)
	if err != nil {
		fmt.Printf("[%s]: MigrateVSClassesAndVSContentsToNewSecret error %s\n", LogPrefix, err.Error())
		return err
	}
	fmt.Printf("[%s]: Finished migration for VolumeSnapshotClasses and VolumeSnapshotcontents\n", LogPrefix)

	fmt.Printf("[%s]: Finished migration from Ceph CSI module\n", LogPrefix)

	return nil
}
