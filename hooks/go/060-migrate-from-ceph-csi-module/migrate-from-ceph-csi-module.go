package hooks_common

import (
	"context"
	"fmt"

	oldApi "csi-ceph/api/v1alpha1"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"

	funcs "csi-ceph/funcs"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	CephClusterAuthenticationMigratedLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthentication"
	CephClusterAuthenticationMigratedLabelValue = "true"
	CephCSIMigratedLabel                        = "storage.deckhouse.io/migratedFromCephCSI"
	CephCSIMigratedLabelValue                   = "true"
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateFromCephCsiModule)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func handlerMigrateFromCephCsiModule(ctx context.Context, input *pkg.HookInput) error {
	fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: Started migration from Ceph CSI module\n")

	cl, err := funcs.NewKubeClient()
	if err != nil {
		fmt.Printf("%s", err.Error())
		return err
	}

	cephCSIDriverList := &oldApi.CephCSIDriverList{}

	err = cl.List(ctx, cephCSIDriverList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephCSIDriverList get error %s\n", err)
		return err
	}

	for _, cephCSIDriver := range cephCSIDriverList.Items {
		if cephCSIDriver.Labels[CephCSIMigratedLabel] == CephCSIMigratedLabelValue {
			continue
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: Migrating %s\n", cephCSIDriver.Name)

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
					fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: cephClusterConnection create error %s\n", err)
					return err
				}
			} else {
				fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephClusterConnection get error %s\n", err)
				return err
			}
		} else {
			fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephClusterConnection already exists\n")
		}

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
							fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephStorageClass create error %s\n", err)
							return err
						}
					} else {
						fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephStorageClass get error %s\n", err)
						return err
					}
				} else {
					fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephStorageClass already exists\n")
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
							fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephStorageClass create error %s\n", err)
							return err
						}
					} else {
						fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephStorageClass get error %s\n", err)
						return err
					}
				} else {
					fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephStorageClass already exists %s\n", err)
				}
			}
		}

		if cephCSIDriver.Labels == nil {
			cephCSIDriver.Labels = make(map[string]string)
		}
		cephCSIDriver.Labels[CephCSIMigratedLabel] = CephCSIMigratedLabelValue
		err = cl.Update(ctx, &cephCSIDriver)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: CephCSIDriver update error %s\n", err)
			return err
		}
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-csi-module]: Finished migration from Ceph CSI module\n")

	return nil
}
