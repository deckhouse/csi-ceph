package hooks_common

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
	"github.com/google/go-cmp/cmp"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
)

const (
	MigratedFromClusterAuthLabelKey = "storage.deckhouse.io/migratedFromCephClusterAuthentication"
	LabelValueTrue                  = "true"
	LabelValueFalse                 = "false"

	CephClusterAuthenticationNameLabelKey = "storage.deckhouse.io/ceph-cluster-authentication-name"

	MigratedWarningLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthenticationWarning"
	MigratedWarningLabelValue = "true"

	AutomaticallyCreatedLabel            = "storage.deckhouse.io/automaticallyCreatedBy"
	AutomaticallyCreatedClusterAuthValue = "migrate-auth-to-connection"
	ModuleNamespace                      = "d8-csi-ceph"
	StorageManagedLabelKey               = "storage.deckhouse.io/managed-by"
	CephClusterAuthenticationCtrlName    = "d8-ceph-cluster-authentication-controller"
	CSICephSecretPrefix                  = "csi-ceph-secret-for-"

	PVAnnotationProvisionerDeletionSecretNamespace = "volume.kubernetes.io/provisioner-deletion-secret-namespace"
	PVAnnotationProvisionerDeletionSecretName      = "volume.kubernetes.io/provisioner-deletion-secret-name"

	VSCAnnotationDeletionSecretName      = "snapshot.storage.kubernetes.io/deletion-secret-name"
	VSCAnnotationDeletionSecretNamespace = "snapshot.storage.kubernetes.io/deletion-secret-namespace"

	CSISnapshotterSecretNameKey      = "csi.storage.k8s.io/snapshotter-secret-name"
	CSISnapshotterSecretNamespaceKey = "csi.storage.k8s.io/snapshotter-secret-namespace"

	VSClassParametersClusterIDKey = "clusterID"

	BackupDateLabelKey                = "storage.deckhouse.io/backup-date"
	BackupSourceLabelKey              = "storage.deckhouse.io/backup-source"
	BackupSourceClusterAuthLabelValue = "migrate-auth-to-connection"

	ResourceKindLabelKey         = "storage.deckhouse.io/resource-kind"
	ResourceKindPersistentVolume = "PersistentVolume"

	PVRecreatedSuccesfullyLabelKey = "storage.deckhouse.io/pv-recreated-successfully"
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

		if labelValue == LabelValueTrue {
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

func handlerMigrateAuthToConnection(ctx context.Context, input *pkg.HookInput) error {
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Started migration from CephClusterAuthentication\n")

	cl, err := NewKubeClient()
	if err != nil {
		fmt.Printf("%s", err.Error())
		return err
	}

	err = processRemovedPVs(ctx, cl)
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

		if cephStorageClass.Labels[MigratedFromClusterAuthLabelKey] == LabelValueTrue {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: %s already migrated. Skipping\n", cephStorageClass.Name)
			succefullyMigrated++
			continue
		}

		if cephStorageClass.Spec.ClusterAuthenticationName == "" {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass %s doesn't have ClusterAuthenticationName field. Marking CephStorageClass as migrated\n", cephStorageClass.Name)
			err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, LabelValueTrue)
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
			err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, LabelValueFalse)
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
		err = disableCSIComponents(ctx, cl)
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

		newSecretName := CSICephSecretPrefix + cephClusterConnection.Name
		if needNew {
			newCephClusterConnectionName := cephClusterConnection.Name + "-migrated-" + cephClusterAuth.Name
			newSecretName = CSICephSecretPrefix + newCephClusterConnectionName
			err := processNewCephClusterConnection(ctx, cl, &cephStorageClass, newCephClusterConnectionName, &cephClusterConnection, cephClusterAuth)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: processNewCephClusterConnection: error %s\n", err)
				return err
			}
		}

		err = migratePVsToNewSecret(ctx, cl, pvList, cephStorageClass.Name, ModuleNamespace, newSecretName)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: migratePVsToNewSecret: error %s\n", err)
			return err
		}

		err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, LabelValueTrue)
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

	err = migrateVSClassesAndVSContentsToNewSecret(ctx, cl)
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
	err := cl.List(ctx, secretList, client.InNamespace(ModuleNamespace), client.MatchingLabels{StorageManagedLabelKey: CephClusterAuthenticationCtrlName})
	if err != nil {
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

	_, err = backupResource(ctx, cl, cephStorageClass, BackupTime)
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

func migratePVsToNewSecret(ctx context.Context, cl client.Client, pvList *v1.PersistentVolumeList, scName, newSecretNamespace, newSecretName string) error {
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Started migration PersistentVolumes to new secret %s in namespace %s\n", newSecretName, newSecretNamespace)

	for i := range pvList.Items {
		pv := &pvList.Items[i]
		if pv.Spec.CSI == nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s doesn't have CSI field. Skipping\n", pv.Name)
			continue
		}

		if !slices.Contains(AllowedProvisioners, pv.Spec.CSI.Driver) {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s has not allowed provisioner %s (allowed: %v). Skipping\n", pv.Name, pv.Spec.CSI.Driver, AllowedProvisioners)
			continue
		}

		if pv.Spec.StorageClassName != scName {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s uses different storage class %s (expected: %s). Skipping\n", pv.Name, pv.Spec.StorageClassName, scName)
			continue
		}

		needRecreate := false
		if pv.Spec.CSI.ControllerExpandSecretRef != nil {
			if pv.Spec.CSI.ControllerExpandSecretRef.Namespace != newSecretNamespace || pv.Spec.CSI.ControllerExpandSecretRef.Name != newSecretName {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s has different ControllerExpandSecretRef %s/%s (expected: %s/%s)\n", pv.Name, pv.Spec.CSI.ControllerExpandSecretRef.Namespace, pv.Spec.CSI.ControllerExpandSecretRef.Name, newSecretNamespace, newSecretName)
				needRecreate = true
			}
		}

		if pv.Spec.CSI.NodeStageSecretRef != nil {
			if pv.Spec.CSI.NodeStageSecretRef.Namespace != newSecretNamespace || pv.Spec.CSI.NodeStageSecretRef.Name != newSecretName {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s has different NodeStageSecretRef %s/%s (expected: %s/%s)\n", pv.Name, pv.Spec.CSI.NodeStageSecretRef.Namespace, pv.Spec.CSI.NodeStageSecretRef.Name, newSecretNamespace, newSecretName)
				needRecreate = true
			}
		}

		needUpdate := false
		if pv.Annotations != nil {
			if pv.Annotations[PVAnnotationProvisionerDeletionSecretNamespace] != newSecretNamespace || pv.Annotations[PVAnnotationProvisionerDeletionSecretName] != newSecretName {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s has different provision deletion secret %s/%s (expected: %s/%s)\n", pv.Name, pv.Annotations[PVAnnotationProvisionerDeletionSecretNamespace], pv.Annotations[PVAnnotationProvisionerDeletionSecretName], newSecretNamespace, newSecretName)
				needUpdate = true
			}
		} else {
			pv.Annotations = make(map[string]string)
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s doesn't have annotations\n", pv.Name)
			needUpdate = true
		}

		if needRecreate {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Recreating PV %s\n", pv.Name)
			backupName, err := backupResource(ctx, cl, pv, BackupTime)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Backup PV %s error %s\n", pv.Name, err)
				return err
			}

			err = setRecreateLabelToBackupResource(ctx, cl, backupName, LabelValueFalse)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: setRecreateLabelToBackupResource error %s\n", err)
				return err
			}

			newPV := pv.DeepCopy()
			newPV.ResourceVersion = ""
			newPV.UID = ""
			newPV.CreationTimestamp = metav1.Time{}
			newPV.Spec.CSI.NodeStageSecretRef.Namespace = newSecretNamespace
			newPV.Spec.CSI.NodeStageSecretRef.Name = newSecretName
			newPV.Spec.CSI.ControllerExpandSecretRef.Namespace = newSecretNamespace
			newPV.Spec.CSI.ControllerExpandSecretRef.Name = newSecretName
			newPV.Annotations[PVAnnotationProvisionerDeletionSecretNamespace] = newSecretNamespace
			newPV.Annotations[PVAnnotationProvisionerDeletionSecretName] = newSecretName

			patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":[]},"spec":{"persistentVolumeReclaimPolicy":"Retain"}}`))
			if err := cl.Patch(ctx, pv, patch); err != nil {
				return fmt.Errorf("failed to remove finalizers: %w", err)
			}

			err = cl.Delete(ctx, pv)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV delete error %s\n", err)
				return err
			}

			err = cl.Create(ctx, newPV)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV create error %s\n", err)
				return err
			}
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s successfully migrated\n", pv.Name)

			err = setRecreateLabelToBackupResource(ctx, cl, backupName, LabelValueTrue)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: setRecreateLabelToBackupResource error %s\n", err)
				return err
			}
		} else if needUpdate {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Updating PV %s\n", pv.Name)
			err := cl.Update(ctx, pv)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV update error %s\n", err)
				return err
			}
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s successfully migrated\n", pv.Name)
		} else {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s doesn't need migration\n", pv.Name)
		}
	}

	return nil
}

func backupResource(ctx context.Context, cl client.Client, backupObj runtime.Object, backupTime time.Time) (string, error) {
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

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Backup resource %s %s\n", resourceKind, metaObj.GetName())

	backupName := fmt.Sprintf("%s-%s-%s", datetime, strings.ToLower(resourceKind), metaObj.GetName())
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Backup name %s\n", backupName)

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
				BackupSourceLabelKey: BackupSourceClusterAuthLabelValue,
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

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Resource successfully backed up with name %s\n", backupName)

	return backupName, nil
}

func disableCSIComponents(ctx context.Context, cl client.Client) error {

	deploymentList := &appsv1.DeploymentList{}
	err := cl.List(ctx, deploymentList, client.InNamespace(ModuleNamespace), client.MatchingLabels{"app": "csi-controller"})
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: DeploymentList get error %s\n", err)
		return err
	}

	for _, deployment := range deploymentList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Deleting deployment csi-controller %s in namespace %s\n", deployment.Name, deployment.Namespace)

		deployment.SetFinalizers([]string{})
		err = cl.Update(ctx, &deployment)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Deployment update error %s\n", err)
			return err
		}

		err = cl.Delete(ctx, &deployment)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Deployment delete error %s\n", err)
			return err
		}
	}

	daemonSetList := &appsv1.DaemonSetList{}
	err = cl.List(ctx, daemonSetList, client.InNamespace(ModuleNamespace), client.MatchingLabels{"app": "csi-node"})
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: DaemonSetList get error %s\n", err)
		return err
	}

	for _, daemonSet := range daemonSetList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Deleting daemonset csi-node %s in namespace %s\n", daemonSet.Name, daemonSet.Namespace)

		daemonSet.SetFinalizers([]string{})
		err = cl.Update(ctx, &daemonSet)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: DaemonSet update error %s\n", err)
			return err
		}

		err = cl.Delete(ctx, &daemonSet)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: DaemonSet delete error %s\n", err)
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
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PodList get error %s\n", err)
			return false, err
		}

		if len(podList.Items) == 0 {
			return true, nil
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Waiting for all pods to be deleted\n")
		return false, nil
	})

	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Waiting for all pods to be deleted error: %s\n", err)
		return err
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: All pods are deleted\n")

	return nil
}

func processRemovedPVs(ctx context.Context, cl client.Client) error {
	cephMetadataBackupList := &v1alpha1.CephMetadataBackupList{}
	labels := map[string]string{
		BackupSourceLabelKey:           BackupSourceClusterAuthLabelValue,
		ResourceKindLabelKey:           ResourceKindPersistentVolume,
		PVRecreatedSuccesfullyLabelKey: LabelValueFalse,
	}
	err := cl.List(ctx, cephMetadataBackupList, client.MatchingLabels(labels))
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephMetadataBackupList get error %s\n", err)
		return err
	}

	if len(cephMetadataBackupList.Items) == 0 {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Not found deleted and not migrated PVs\n")
		return nil
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Found %d backups of PVs that probably were removed and not migrated. Check them\n", len(cephMetadataBackupList.Items))
	for _, backup := range cephMetadataBackupList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Processing backup %s\n", backup.Name)

		obj := &v1.PersistentVolume{}
		data, err := base64.StdEncoding.DecodeString(backup.Spec.Data)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Decode backup data error %s\n", err)
			return err
		}

		err = json.Unmarshal(data, obj)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Unmarshal backup data error %s\n", err)
			return err
		}

		err = cl.Create(ctx, obj)
		if err != nil {
			if client.IgnoreAlreadyExists(err) != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV create error %s\n", err)
				return err
			}
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s already exists\n", obj.Name)
		} else {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: PV %s created\n", obj.Name)
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Updating backup %s with %s=%s\n", backup.Name, PVRecreatedSuccesfullyLabelKey, LabelValueTrue)
		backup.Labels[PVRecreatedSuccesfullyLabelKey] = LabelValueTrue
		err = cl.Update(ctx, &backup)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Update backup %s error %s\n", backup.Name, err)
			return err
		}
	}
	return nil
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

func migrateVSClassesAndVSContentsToNewSecret(ctx context.Context, cl client.Client) error {
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

		if vsClass.Labels[MigratedFromClusterAuthLabelKey] == LabelValueTrue {
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
			newVolumeSnapshotClass.Labels[MigratedFromClusterAuthLabelKey] = LabelValueTrue
			newVolumeSnapshotClass.Parameters[CSISnapshotterSecretNameKey] = newSecretName
			newVolumeSnapshotClass.Parameters[CSISnapshotterSecretNamespaceKey] = ModuleNamespace
			err := migrateVSContentsToNewSecret(ctx, cl, vsContentList, vsClass.Name, newSecretName)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: migrateVSContentsToNewSecret error %s\n", err)
				return err
			}
		}
		newVolumeSnapshotClass.Labels[MigratedFromClusterAuthLabelKey] = LabelValueTrue

		err = updateVolumeSnapshotClassIfNeeded(ctx, cl, &vsClass, newVolumeSnapshotClass)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: updateVolumeSnapshotClassIfNeeded error %s\n", err)
			return err
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClass %s migrated\n", vsClass.Name)
	}

	return nil
}

func getNewSecretNameFromClusterAuthName(ctx context.Context, cl client.Client, volumeSnapshotClass snapv1.VolumeSnapshotClass, cephClusterConnectionList *v1alpha1.CephClusterConnectionList) (string, error) {
	oldSecretName := volumeSnapshotClass.Parameters[CSISnapshotterSecretNameKey]
	if oldSecretName == "" {
		return "", fmt.Errorf("oldSecretName is empty for VolumeSnapshotClass %+v", volumeSnapshotClass)
	}

	clusterAuthName := strings.TrimPrefix(oldSecretName, CSICephSecretPrefix)
	if clusterAuthName == oldSecretName {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: oldSecretName %s doesn't have prefix %s. Can't get clusterAuthName\n", oldSecretName, CSICephSecretPrefix)
		return "", nil
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Found clusterAuthName %s for oldSecretName %s. Trying to get CephClusterConnection with label %s=%s\n", clusterAuthName, oldSecretName, CephClusterAuthenticationNameLabelKey, clusterAuthName)

	labelMap := map[string]string{
		CephClusterAuthenticationNameLabelKey: clusterAuthName,
	}

	cephClusterConnection := getClusterConnectionByLabel(cephClusterConnectionList, labelMap)
	if cephClusterConnection == nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: No CephClusterConnection found with label %s=%s. Trying to get CephClusterConnection by ClusterID\n", CephClusterAuthenticationNameLabelKey, clusterAuthName)
		clusterID, ok := volumeSnapshotClass.Parameters[VSClassParametersClusterIDKey]
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

	newSecretName := CSICephSecretPrefix + cephClusterConnection.Name
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Found CephClusterConnection %s for CephClusterAuthentication %s. newSecretName %s\n", cephClusterConnection.Name, clusterAuthName, newSecretName)

	return newSecretName, nil
}

func updateVolumeSnapshotClassIfNeeded(ctx context.Context, cl client.Client, oldVSClass, newVSClass *snapv1.VolumeSnapshotClass) error {
	if cmp.Equal(oldVSClass, newVSClass) {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClass %s doesn't need update\n", oldVSClass.Name)
		return nil
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Updating VolumeSnapshotClass %s\n", oldVSClass.Name)
	err := cl.Update(ctx, newVSClass)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClass update error %s\n", err)
		return err
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotClass %s updated\n", oldVSClass.Name)
	return nil
}

func migrateVSContentsToNewSecret(ctx context.Context, cl client.Client, vsContentList *snapv1.VolumeSnapshotContentList, vsClassName, newSecretName string) error {
	for _, vsContent := range vsContentList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Processing VolumeSnapshotContent %s\n", vsContent.Name)
		if !slices.Contains(AllowedProvisioners, vsContent.Spec.Driver) {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent %s has not allowed driver %s (allowed: %v). Skipping\n", vsContent.Name, vsContent.Spec.Driver, AllowedProvisioners)
			continue
		}

		if vsContent.Spec.VolumeSnapshotClassName == nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent %s doesn't have VolumeSnapshotClassName. Skipping\n", vsContent.Name)
			continue
		}

		if *vsContent.Spec.VolumeSnapshotClassName != vsClassName {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent %s has different VolumeSnapshotClassName %s (expected: %s). Skipping\n", vsContent.Name, *vsContent.Spec.VolumeSnapshotClassName, vsClassName)
			continue
		}

		if vsContent.Labels[MigratedFromClusterAuthLabelKey] == LabelValueTrue {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent %s already migrated. Skipping\n", vsContent.Name)
			continue
		}

		newVolumeSnapshotContent := vsContent.DeepCopy()
		if newVolumeSnapshotContent.Labels == nil {
			newVolumeSnapshotContent.Labels = make(map[string]string)
		}
		newVolumeSnapshotContent.Labels[MigratedFromClusterAuthLabelKey] = LabelValueTrue

		if newVolumeSnapshotContent.Annotations == nil {
			newVolumeSnapshotContent.Annotations = make(map[string]string)
		}
		newVolumeSnapshotContent.Annotations[VSCAnnotationDeletionSecretName] = newSecretName
		newVolumeSnapshotContent.Annotations[VSCAnnotationDeletionSecretNamespace] = ModuleNamespace

		err := updateVolumeSnapshotContentIfNeeded(ctx, cl, &vsContent, newVolumeSnapshotContent)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: updateVolumeSnapshotContentIfNeeded error %s\n", err)
			return err
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent %s migrated\n", vsContent.Name)
	}

	return nil
}

func updateVolumeSnapshotContentIfNeeded(ctx context.Context, cl client.Client, oldVSContent, newVSContent *snapv1.VolumeSnapshotContent) error {
	if cmp.Equal(oldVSContent, newVSContent) {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent %s doesn't need update\n", oldVSContent.Name)
		return nil
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Updating VolumeSnapshotContent %s\n", oldVSContent.Name)
	err := cl.Update(ctx, newVSContent)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent update error %s\n", err)
		return err
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: VolumeSnapshotContent %s updated\n", oldVSContent.Name)
	return nil
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
