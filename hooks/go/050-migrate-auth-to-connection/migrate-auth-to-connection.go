package hooks_common

import (
	"context"
	"csi-ceph/funcs"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
	"github.com/google/go-cmp/cmp"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MigratedFromClusterAuthLabelKey = "storage.deckhouse.io/migratedFromCephClusterAuthentication"

	CephClusterAuthenticationNameLabelKey = "storage.deckhouse.io/ceph-cluster-authentication-name"

	MigratedWarningLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthenticationWarning"
	MigratedWarningLabelValue = "true"

	AutomaticallyCreatedLabel            = "storage.deckhouse.io/automaticallyCreatedBy"
	AutomaticallyCreatedClusterAuthValue = "migrate-auth-to-connection"
	StorageManagedLabelKey               = "storage.deckhouse.io/managed-by"
	CephClusterAuthenticationCtrlName    = "d8-ceph-cluster-authentication-controller"

	BackupSourceClusterAuthLabelValue = "migrate-auth-to-connection"
)

var (
	AllowedProvisioners = []string{
		"rbd.csi.ceph.com",
		"cephfs.csi.ceph.com",
	}
	BackupTime = time.Now()
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateAuthToConnection)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

type CephClusterAuthenticationMigrate struct {
	CephClusterAuthentication *v1alpha1.CephClusterAuthentication
	RefCount                  int
	Used                      bool
}

func handlerMigrateAuthToConnection(ctx context.Context, input *pkg.HookInput) error {
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Started migration from CephClusterAuthentication\n")

	cl, err := funcs.NewKubeClient()
	if err != nil {
		fmt.Printf("%s", err.Error())
		return err
	}

	err = funcs.ProcessRemovedPVs(ctx, cl, BackupSourceClusterAuthLabelValue, "csi-ceph-migration-from-ceph-cluster-authentication")
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: processRemovedPVs error %s\n", err)
		return err
	}

	cephClusterAuthenticationList := &v1alpha1.CephClusterAuthenticationList{}
	err = cl.List(ctx, cephClusterAuthenticationList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthenticationList get error %s\n", err)
		return err
	}

	CephClusterAuthenticationMigrateMap := make(map[string]*CephClusterAuthenticationMigrate)
	for _, cephClusterAuthentication := range cephClusterAuthenticationList.Items {
		CephClusterAuthenticationMigrateMap[cephClusterAuthentication.Name] = &CephClusterAuthenticationMigrate{
			CephClusterAuthentication: &cephClusterAuthentication,
			RefCount:                  0,
			Used:                      false,
		}
	}

	cephStorageClassList := &v1alpha1.CephStorageClassList{}
	err = cl.List(ctx, cephStorageClassList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClassList get error %s\n", err)
		return err
	}

	cephClusterConnectionList := &v1alpha1.CephClusterConnectionList{}
	err = cl.List(ctx, cephClusterConnectionList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnectionList get error %s\n", err)
		return err
	}

	cephClusterConnectionMap := make(map[string]v1alpha1.CephClusterConnection, len(cephClusterConnectionList.Items))
	for _, cephClusterConnection := range cephClusterConnectionList.Items {
		cephClusterConnectionMap[cephClusterConnection.Name] = cephClusterConnection
	}

	succefullyMigrated := 0
	cephSCToMigrate := []v1alpha1.CephStorageClass{}

	for _, cephStorageClass := range cephStorageClassList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Check CephStorageClass %s\n", cephStorageClass.Name)

		if cephStorageClass.Labels[MigratedFromClusterAuthLabelKey] == funcs.LabelValueTrue {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: %s already migrated. Skipping\n", cephStorageClass.Name)
			succefullyMigrated++
			continue
		}

		if cephStorageClass.Spec.ClusterAuthenticationName == "" {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass %s doesn't have ClusterAuthenticationName field. Marking CephStorageClass as migrated\n", cephStorageClass.Name)
			err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, funcs.LabelValueTrue)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
				return err
			}
			succefullyMigrated++
			continue
		}

		skipMigration := false
		_, exists := cephClusterConnectionMap[cephStorageClass.Spec.ClusterConnectionName]
		if !exists {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s for CephStorageClass %s not found. Marking CephStorageClass as not migrated\n", cephStorageClass.Spec.ClusterConnectionName, cephStorageClass.Name)
			skipMigration = true
		}

		_, exists = CephClusterAuthenticationMigrateMap[cephStorageClass.Spec.ClusterAuthenticationName]
		if !exists {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication %s for CephStorageClass %s not found. Marking CephStorageClass as not migrated\n", cephStorageClass.Spec.ClusterAuthenticationName, cephStorageClass.Name)
			skipMigration = true
		} else {
			cephClusterAuthenticationMigrate := CephClusterAuthenticationMigrateMap[cephStorageClass.Spec.ClusterAuthenticationName]
			cephClusterAuthenticationMigrate.RefCount++
			cephClusterAuthenticationMigrate.Used = true
		}

		if skipMigration {
			err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, funcs.LabelValueFalse)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
				return err
			}
			continue
		}

		cephSCToMigrate = append(cephSCToMigrate, cephStorageClass)
	}

	pvList := &v1.PersistentVolumeList{}
	if len(cephSCToMigrate) != 0 {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Disable CSI components\n")
		err = funcs.DisableCSIComponents(ctx, cl, "csi-ceph-migration-from-ceph-cluster-authentication")
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Disable CSI components error %s\n", err)
			return err
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Getting PVList\n")
		err = cl.List(ctx, pvList)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PVList get error %s\n", err)
			return err
		}
	}

	for _, cephStorageClass := range cephSCToMigrate {

		cephClusterAuthenticationMigrate, _ := CephClusterAuthenticationMigrateMap[cephStorageClass.Spec.ClusterAuthenticationName]
		cephClusterAuth := cephClusterAuthenticationMigrate.CephClusterAuthentication

		cephClusterConnection, _ := cephClusterConnectionMap[cephStorageClass.Spec.ClusterConnectionName]

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Processing CephStorageClass %s with CephClusterConnection %s and CephClusterAuthentication %s\n", cephStorageClass.Name, cephClusterConnection.Name, cephClusterAuth.Name)

		needNew, err := processCephClusterConnection(ctx, cl, &cephStorageClass, &cephClusterConnection, cephClusterAuth)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection process error %s\n", err)
			return err
		}

		newSecretName := funcs.CSICephSecretPrefix + cephClusterConnection.Name
		if needNew {
			newCephClusterConnectionName := cephClusterConnection.Name + "-migrated-" + cephClusterAuth.Name
			newSecretName = funcs.CSICephSecretPrefix + newCephClusterConnectionName
			err := processNewCephClusterConnection(ctx, cl, &cephStorageClass, newCephClusterConnectionName, &cephClusterConnection, cephClusterAuth)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: processNewCephClusterConnection: error %s\n", err)
				return err
			}
		}

		err = funcs.MigratePVsToNewSecret(ctx, cl, pvList, BackupTime, cephStorageClass.Name, newSecretName, BackupSourceClusterAuthLabelValue, "csi-ceph-migration-from-ceph-cluster-authentication")
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: migratePVsToNewSecret: error %s\n", err)
			return err
		}

		err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, funcs.LabelValueTrue)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
			return err
		}

		cephClusterAuthenticationMigrate.RefCount--
		succefullyMigrated++
	}

	for _, cephClusterAuthenticationMigrate := range CephClusterAuthenticationMigrateMap {
		if cephClusterAuthenticationMigrate.RefCount > 0 {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication %s still in use\n", cephClusterAuthenticationMigrate.CephClusterAuthentication.Name)
			continue
		}
		if !cephClusterAuthenticationMigrate.Used {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication %s not being used by any CephStorageClass. Keeping it\n", cephClusterAuthenticationMigrate.CephClusterAuthentication.Name)
			continue
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Deleting CephClusterAuthentication %s as it's not in use anymore\n", cephClusterAuthenticationMigrate.CephClusterAuthentication.Name)

		cephClusterAuthenticationMigrate.CephClusterAuthentication.SetFinalizers([]string{})
		err = cl.Update(ctx, cephClusterAuthenticationMigrate.CephClusterAuthentication)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication update error %s\n", err)
			return err
		}

		err = cl.Delete(ctx, cephClusterAuthenticationMigrate.CephClusterAuthentication)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication delete error %s\n", err)
			return err
		}
	}

	err = migrateVSClassesAndVSContentsToNewSecretForAuth(ctx, cl)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: migrateVolumeSnapshotClassesToNewSecret error %s\n", err)
		return err
	}

	notMigratedCount := len(cephStorageClassList.Items) - succefullyMigrated
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: %d CephStorageClasses successfully migrated. %d not migrated\n", len(cephSCToMigrate), notMigratedCount)
	if notMigratedCount > 0 {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: There are CephStorageClasses that were not migrated. Please check logs for more information\n")
		return nil
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: All CephStorageClasses successfully migrated. Deleting secrets\n")
	err = processSecrets(ctx, cl)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Secrets process error %s\n", err)
		return err
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Finished migration from CephClusterAuthentication\n")
	return nil
}

func processCephClusterConnection(
	ctx context.Context,
	cl client.Client,
	cephStorageClass *v1alpha1.CephStorageClass,
	cephClusterConnection *v1alpha1.CephClusterConnection,
	cephClusterAuthentication *v1alpha1.CephClusterAuthentication,
) (bool, error) {
	needUpdate := false
	needNewCephClusterConnection := false

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Getting CephClusterConnection %s\n", cephClusterConnection.Name)
	err := cl.Get(ctx, types.NamespacedName{Name: cephClusterConnection.Name, Namespace: ""}, cephClusterConnection)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s get error %s\n", cephClusterConnection.Name, err)
		return false, err
	}

	if cephClusterConnection.Spec.UserID != "" && cephClusterConnection.Spec.UserID != cephClusterAuthentication.Spec.UserID {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s UserID %s doesn't match CephClusterAuthentication %s UserID %s and both in use by CephStorageClass %s\n", cephClusterConnection.Name, cephClusterConnection.Spec.UserID, cephClusterAuthentication.Name, cephClusterAuthentication.Spec.UserID, cephStorageClass.Name)
		needNewCephClusterConnection = true
	}
	if cephClusterConnection.Spec.UserKey != "" && cephClusterConnection.Spec.UserKey != cephClusterAuthentication.Spec.UserKey {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s UserKey doesn't match CephClusterAuthentication %s UserKey and both in use by CephStorageClass %s\n", cephClusterConnection.Name, cephClusterAuthentication.Name, cephStorageClass.Name)
		needNewCephClusterConnection = true
	}

	if needNewCephClusterConnection {
		return true, nil
	}

	if cephClusterConnection.Spec.UserID == "" {
		cephClusterConnection.Spec.UserID = cephClusterAuthentication.Spec.UserID
		needUpdate = true
	}

	if cephClusterConnection.Spec.UserKey == "" {
		cephClusterConnection.Spec.UserKey = cephClusterAuthentication.Spec.UserKey
		needUpdate = true
	}

	if needUpdate {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Updating CephClusterConnection %s and set label %s=%s\n", cephClusterConnection.Name, CephClusterAuthenticationNameLabelKey, cephClusterAuthentication.Name)
		if cephClusterConnection.Labels == nil {
			cephClusterConnection.Labels = make(map[string]string)
		}
		cephClusterConnection.Labels[CephClusterAuthenticationNameLabelKey] = cephClusterAuthentication.Name
		return false, cl.Update(ctx, cephClusterConnection)
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s doesn't need update\n", cephClusterConnection.Name)
	return false, nil
}

func processSecrets(ctx context.Context, cl client.Client) error {
	secretList := &v1.SecretList{}
	err := cl.List(ctx, secretList, client.InNamespace(funcs.ModuleNamespace), client.MatchingLabels{StorageManagedLabelKey: CephClusterAuthenticationCtrlName})
	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: SecretList not found: %s. Skipping secrets deletion\n", err.Error())
			return nil
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: SecretList get error %s\n", err)
		return err
	}

	for _, secret := range secretList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Deleting secret %s in namespace %s\n because it's managed by %s\n", secret.Name, secret.Namespace, CephClusterAuthenticationCtrlName)

		secret.SetFinalizers([]string{})
		err = cl.Update(ctx, &secret)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Secret update error %s\n", err)
			return err
		}

		err = cl.Delete(ctx, &secret)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Secret delete error %s\n", err)
			return err
		}
	}

	return nil
}

func processNewCephClusterConnection(
	ctx context.Context,
	cl client.Client,
	cephStorageClass *v1alpha1.CephStorageClass,
	newCephClusterConnectionName string,
	cephClusterConnection *v1alpha1.CephClusterConnection,
	cephClusterAuthentication *v1alpha1.CephClusterAuthentication,
) error {
	// newCephClusterConnectionName := cephClusterConnection.Name + "-migrated-" + cephClusterAuthentication.Name
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Creating new CephClusterConnection %s for CephStorageClass %s\n", newCephClusterConnectionName, cephStorageClass.Name)

	newCephClusterConnection := &v1alpha1.CephClusterConnection{
		ObjectMeta: metav1.ObjectMeta{
			Name: newCephClusterConnectionName,
			Labels: map[string]string{
				AutomaticallyCreatedLabel:             AutomaticallyCreatedClusterAuthValue,
				CephClusterAuthenticationNameLabelKey: cephClusterAuthentication.Name,
			},
		},
		Spec: v1alpha1.CephClusterConnectionSpec{
			ClusterID: cephClusterConnection.Spec.ClusterID,
			Monitors:  cephClusterConnection.Spec.Monitors,
			UserID:    cephClusterAuthentication.Spec.UserID,
			UserKey:   cephClusterAuthentication.Spec.UserKey,
		},
	}

	err := cl.Create(ctx, newCephClusterConnection)
	if err != nil {
		if client.IgnoreAlreadyExists(err) != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: new CephClusterConnection create error %s\n", err)
			return err
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: new CephClusterConnection %s already exists. Checking if it's the same\n", newCephClusterConnection.Name)
		existingCephClusterConnection := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, types.NamespacedName{Name: newCephClusterConnection.Name, Namespace: ""}, existingCephClusterConnection)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: new CephClusterConnection %s get error %s\n", newCephClusterConnection.Name, err)
			return err
		}
		if !cmp.Equal(existingCephClusterConnection.Spec, newCephClusterConnection.Spec) {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s already exists but different. Exiting with error\n", newCephClusterConnection.Name)
			return fmt.Errorf("CephClusterConnection %s already exists but different", newCephClusterConnection.Name)
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s already exists and the same.\n", newCephClusterConnection.Name)
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: New CephClusterConnection %s created or already exists. Recreating CephStorageClass %s with new CephClusterConnection name\n", newCephClusterConnection.Name, cephStorageClass.Name)

	newCephStorageClass := &v1alpha1.CephStorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: cephStorageClass.Name,
			Labels: map[string]string{
				MigratedWarningLabel: MigratedWarningLabelValue,
			},
		},
		Spec: cephStorageClass.Spec,
	}
	newCephStorageClass.Spec.ClusterConnectionName = newCephClusterConnection.Name

	_, err = funcs.BackupResource(ctx, cl, cephStorageClass, BackupTime, BackupSourceClusterAuthLabelValue, "csi-ceph-migration-from-ceph-cluster-authentication")
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Backup CephStorageClass error %s\n", err)
		return err
	}

	cephStorageClass.SetFinalizers([]string{})
	err = cl.Update(ctx, cephStorageClass)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
		return err
	}
	err = cl.Delete(ctx, cephStorageClass)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass delete error %s\n", err)
		return err
	}

	err = retry.OnError(retry.DefaultBackoff, func(err error) bool {
		return true
	}, func() error {
		err := cl.Create(ctx, newCephStorageClass)
		return err
	})

	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass create error %s\n", err)
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass %+v\n", newCephStorageClass)
		return err
	}
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: New CephStorageClass %s created\n", newCephStorageClass.Name)

	return nil
}

func migrateVSClassesAndVSContentsToNewSecretForAuth(ctx context.Context, cl client.Client) error {
	vsClassList := &snapv1.VolumeSnapshotClassList{}
	err := cl.List(ctx, vsClassList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClassList get error %s\n", err)
		return err
	}

	vsContentList := &snapv1.VolumeSnapshotContentList{}
	err = cl.List(ctx, vsContentList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContentList get error %s\n", err)
		return err
	}

	cephClusterConnectionList := &v1alpha1.CephClusterConnectionList{}
	err = cl.List(ctx, cephClusterConnectionList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnectionList get error %s\n", err)
		return err
	}

	for _, vsClass := range vsClassList.Items {
		if !slices.Contains(AllowedProvisioners, vsClass.Driver) {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClass %s has not allowed driver %s. Skipping\n", vsClass.Name, vsClass.Driver)
			continue
		}

		if vsClass.Labels[MigratedFromClusterAuthLabelKey] == funcs.LabelValueTrue {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClass %s already migrated. Skipping\n", vsClass.Name)
			continue
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Processing VolumeSnapshotClass %s\n", vsClass.Name)

		newSecretName, err := getNewSecretNameFromClusterAuthName(ctx, cl, vsClass, cephClusterConnectionList)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: getNewSecretName error %s\n", err)
			return err
		}

		newVolumeSnapshotClass := vsClass.DeepCopy()
		if newVolumeSnapshotClass.Labels == nil {
			newVolumeSnapshotClass.Labels = make(map[string]string)
		}
		if newVolumeSnapshotClass.Parameters == nil {
			newVolumeSnapshotClass.Parameters = make(map[string]string)
		}

		if newSecretName == "" {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: newSecretName is empty for VolumeSnapshotClass %s. Set as migrated with warning\n", vsClass.Name)
			newVolumeSnapshotClass.Labels[MigratedWarningLabel] = MigratedWarningLabelValue
		} else {
			newVolumeSnapshotClass.Labels[MigratedFromClusterAuthLabelKey] = funcs.LabelValueTrue
			newVolumeSnapshotClass.Parameters[funcs.CSISnapshotterSecretNameKey] = newSecretName
			newVolumeSnapshotClass.Parameters[funcs.CSISnapshotterSecretNamespaceKey] = funcs.ModuleNamespace
			err := funcs.MigrateVSContentsToNewSecret(ctx, cl, vsContentList, vsClass.Name, newSecretName, MigratedFromClusterAuthLabelKey, "csi-ceph-migration-from-ceph-cluster-authentication")
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: migrateVSContentsToNewSecret error %s\n", err)
				return err
			}
		}
		newVolumeSnapshotClass.Labels[MigratedFromClusterAuthLabelKey] = funcs.LabelValueTrue

		err = funcs.UpdateVolumeSnapshotClassIfNeeded(ctx, cl, &vsClass, newVolumeSnapshotClass, "csi-ceph-migration-from-ceph-cluster-authentication")
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: updateVolumeSnapshotClassIfNeeded error %s\n", err)
			return err
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClass %s and VolumeSnapshotContents for it successfully migrated\n", vsClass.Name)
	}

	return nil
}

func getNewSecretNameFromClusterAuthName(ctx context.Context, cl client.Client, volumeSnapshotClass snapv1.VolumeSnapshotClass, cephClusterConnectionList *v1alpha1.CephClusterConnectionList) (string, error) {
	oldSecretName := volumeSnapshotClass.Parameters[funcs.CSISnapshotterSecretNameKey]
	if oldSecretName == "" {
		return "", fmt.Errorf("oldSecretName is empty for VolumeSnapshotClass %+v", volumeSnapshotClass)
	}

	clusterAuthName := strings.TrimPrefix(oldSecretName, funcs.CSICephSecretPrefix)
	if clusterAuthName == oldSecretName {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: oldSecretName %s doesn't have prefix %s. Can't get clusterAuthName\n", oldSecretName, funcs.CSICephSecretPrefix)
		return "", nil
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Found clusterAuthName %s for oldSecretName %s. Trying to get CephClusterConnection with label %s=%s\n", clusterAuthName, oldSecretName, CephClusterAuthenticationNameLabelKey, clusterAuthName)

	labelMap := map[string]string{
		CephClusterAuthenticationNameLabelKey: clusterAuthName,
	}

	cephClusterConnection := getClusterConnectionByLabel(cephClusterConnectionList, labelMap)
	if cephClusterConnection == nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: No CephClusterConnection found with label %s=%s. Trying to get CephClusterConnection by ClusterID\n", CephClusterAuthenticationNameLabelKey, clusterAuthName)
		clusterID, ok := volumeSnapshotClass.Parameters[funcs.VSClassParametersClusterIDKey]
		if !ok || clusterID == "" {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: No clusterID found in VolumeSnapshotClass %s. Can't get CephClusterConnection\n", volumeSnapshotClass.Name)
			return "", nil
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Trying to get CephClusterConnection with clusterID %s\n", clusterID)
		cephClusterConnection = getClusterConnectionByClusterID(ctx, cl, cephClusterConnectionList, clusterID)
		if cephClusterConnection == nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: No CephClusterConnection found with clusterID %s\n", clusterID)
			return "", nil
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Found CephClusterConnection %s with clusterID %s\n", cephClusterConnection.Name, clusterID)
	}

	newSecretName := funcs.CSICephSecretPrefix + cephClusterConnection.Name
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Found CephClusterConnection %s for CephClusterAuthentication %s. newSecretName %s\n", cephClusterConnection.Name, clusterAuthName, newSecretName)

	return newSecretName, nil
}

func getClusterConnectionByClusterID(ctx context.Context, cl client.Client, cephClusterConnectionList *v1alpha1.CephClusterConnectionList, clusterID string) *v1alpha1.CephClusterConnection {
	for _, cephClusterConnection := range cephClusterConnectionList.Items {
		if cephClusterConnection.Spec.ClusterID == clusterID {
			return &cephClusterConnection
		}
	}

	return nil
}

func getClusterConnectionByLabel(cephClusterConnectionList *v1alpha1.CephClusterConnectionList, labelMap map[string]string) *v1alpha1.CephClusterConnection {
	if cephClusterConnectionList == nil {
		return nil
	}

	selector := labels.SelectorFromSet(labelMap)
	for _, cephClusterConnection := range cephClusterConnectionList.Items {
		if selector.Matches(labels.Set(cephClusterConnection.Labels)) {
			return &cephClusterConnection
		}
	}

	return nil
}

func cephStorageClassSetMigrateStatus(ctx context.Context, cl client.Client, cephStorageClass *v1alpha1.CephStorageClass, labelValue string) error {
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		err := cl.Get(ctx, types.NamespacedName{Name: cephStorageClass.Name, Namespace: ""}, cephStorageClass)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass get error %s\n", err)
			return err
		}

		if cephStorageClass.Labels == nil {
			cephStorageClass.Labels = make(map[string]string)
		}

		cephStorageClass.Labels[MigratedFromClusterAuthLabelKey] = labelValue

		if labelValue == funcs.LabelValueTrue {
			cephStorageClass.Spec.ClusterAuthenticationName = ""
		}

		return cl.Update(ctx, cephStorageClass)
	})

	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
	}
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass %s updated with label %s=%s\n", cephStorageClass.Name, MigratedFromClusterAuthLabelKey, labelValue)
	return err
}
