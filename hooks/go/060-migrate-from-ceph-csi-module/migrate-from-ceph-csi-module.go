package hooks_common

import (
	"context"
	"fmt"
	"time"

	oldApi "csi-ceph/api/v1alpha1"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
	v1 "k8s.io/api/core/v1"

	funcs "csi-ceph/funcs"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	CephClusterAuthenticationMigratedLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthentication"
	CephClusterAuthenticationMigratedLabelValue = "true"
	CephCSIMigratedLabel                        = "storage.deckhouse.io/migratedFromCephCSI"
	CephCSIMigratedLabelValue                   = "true"

	CephCSIMigrateBackupSource = "migrate-from-ceph-csi-module"

	LogPrefix = "%s"
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
		fmt.Printf("[%s]: CephCSIDriverList get error %s\n", LogPrefix, err.Error())
		return err
	}

	pvList := &v1.PersistentVolumeList{}
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
	} else {
		fmt.Printf("[%s]: No CephCSIDrivers found\n", LogPrefix)
	}

	for _, cephCSIDriver := range cephCSIDriverList.Items {
		if cephCSIDriver.Labels[CephCSIMigratedLabel] == CephCSIMigratedLabelValue {
			continue
		}

		fmt.Printf("[%s]: Migrating %s\n", LogPrefix, cephCSIDriver.Name)

		cephClusterConnectionName := cephCSIDriver.Name

		cephClusterConnection := &v1alpha1.CephClusterConnection{}

		err = cl.Get(ctx, client.ObjectKey{Name: cephClusterConnectionName}, cephClusterConnection)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				cephClusterConnection.Name = cephClusterConnectionName
				cephClusterConnection.ObjectMeta.Labels = map[string]string{CephClusterAuthenticationMigratedLabel: CephClusterAuthenticationMigratedLabelValue}
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
			} else {
				fmt.Printf("[%s]: CephClusterConnection get error %s\n", LogPrefix, err.Error())
				return err
			}
		} else {
			fmt.Printf("[%s]: CephClusterConnection already exists\n", LogPrefix)
		}

		newSecretName := fmt.Sprintf("%s-%s", funcs.CSICephSecretPrefix, cephClusterConnectionName)

		if cephCSIDriver.Spec.CephFS != nil {
			for _, cephFsStorageClass := range cephCSIDriver.Spec.CephFS.StorageClasses {
				cephStorageClassName := fmt.Sprintf("%s-%s", cephClusterConnectionName, cephFsStorageClass.NamePostfix)
				cephStorageClass := &v1alpha1.CephStorageClass{}

				err = cl.Get(ctx, client.ObjectKey{Name: cephStorageClassName}, cephStorageClass)
				if err != nil {
					if client.IgnoreNotFound(err) == nil {
						cephStorageClass.Name = cephStorageClassName
						cephStorageClass.Spec = v1alpha1.CephStorageClassSpec{
							ClusterConnectionName: cephClusterConnectionName,
							Type:                  v1alpha1.CephStorageClassTypeCephFS,
							CephFS:                &v1alpha1.CephStorageClassCephFS{FSName: cephFsStorageClass.FsName, Pool: cephFsStorageClass.Pool},
							ReclaimPolicy:         cephFsStorageClass.ReclaimPolicy}
						err := cl.Create(ctx, cephStorageClass)
						if err != nil {
							fmt.Printf("[%s]: CephStorageClass create error %s\n", LogPrefix, err.Error())
							return err
						}
					} else {
						fmt.Printf("[%s]: CephStorageClass get error %s\n", LogPrefix, err.Error())
						return err
					}
				} else {
					fmt.Printf("[%s]: CephStorageClass already exists\n", LogPrefix)
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
				cephStorageClass := &v1alpha1.CephStorageClass{}

				err = cl.Get(ctx, client.ObjectKey{Name: cephStorageClassName}, cephStorageClass)
				if err != nil {
					if client.IgnoreNotFound(err) == nil {
						cephStorageClass.Name = cephStorageClassName
						cephStorageClass.Spec = v1alpha1.CephStorageClassSpec{
							ClusterConnectionName: cephClusterConnectionName,
							Type:                  v1alpha1.CephStorageClassTypeRBD,
							RBD:                   &v1alpha1.CephStorageClassRBD{Pool: cephRbdStorageClass.Pool, DefaultFSType: cephRbdStorageClass.DefaultFSType},
							ReclaimPolicy:         cephRbdStorageClass.ReclaimPolicy}
						err := cl.Create(ctx, cephStorageClass)
						if err != nil {
							fmt.Printf("[%s]: CephStorageClass create error %s\n", LogPrefix, err.Error())
							return err
						}
					} else {
						fmt.Printf("[%s]: CephStorageClass get error %s\n", LogPrefix, err.Error())
						return err
					}
				} else {
					fmt.Printf("[%s]: CephStorageClass already exists %s\n", LogPrefix, err)
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
