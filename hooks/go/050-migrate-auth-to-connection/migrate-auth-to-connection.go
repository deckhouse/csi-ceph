package hooks_common

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"

	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MigratedLabel           = "storage.deckhouse.io/migratedFromCephClusterAuthentication"
	MigratedLabelValueTrue  = "true"
	MigratedLabelValueFalse = "false"

	MigratedWarningLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthenticationWarning"
	MigratedWarningLabelValue = "true"

	AutomaticallyCreatedLabel         = "storage.deckhouse.io/automaticallyCreatedBy"
	AutomaticallyCreatedValue         = "migrate-auth-to-connection"
	ModuleNamespace                   = "d8-csi-ceph"
	StorageManagedLabelKey            = "storage.deckhouse.io/managed-by"
	CephClusterAuthenticationCtrlName = "d8-ceph-cluster-authentication-controller"
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
	if cephStorageClass.Labels == nil {
		cephStorageClass.Labels = make(map[string]string)
	}

	cephStorageClass.Labels[MigratedLabel] = labelValue

	if labelValue == MigratedLabelValueTrue {
		cephStorageClass.Spec.ClusterAuthenticationName = ""
	}
	err := cl.Update(ctx, cephStorageClass)
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

	for _, cephStorageClass := range cephStorageClassList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Migrating %s\n", cephStorageClass.Name)

		if cephStorageClass.Labels[MigratedLabel] == MigratedLabelValueTrue {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: %s already migrated\n", cephStorageClass.Name)
			continue
		}

		if cephStorageClass.Spec.ClusterAuthenticationName == "" {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass %s doesn't have ClusterAuthenticationName field. Marking CephStorageClass as migrated\n", cephStorageClass.Name)
			err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, MigratedLabelValueTrue)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
				return err
			}
			continue
		}

		cephClusterAuthenticationMigrate, cephClusterAuthenticationExists := CephClusterAuthenticationMigrateMap[cephStorageClass.Spec.ClusterAuthenticationName]
		if cephClusterAuthenticationExists {
			cephClusterAuthenticationMigrate.RefCount++
			cephClusterAuthenticationMigrate.Used = true
		}

		cephClusterConnection := &v1alpha1.CephClusterConnection{}
		err = cl.Get(ctx, types.NamespacedName{Name: cephStorageClass.Spec.ClusterConnectionName, Namespace: ""}, cephClusterConnection)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s not found\n", cephStorageClass.Spec.ClusterConnectionName)
				err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, MigratedLabelValueFalse)
				if err != nil {
					fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
					return err
				}
				continue
			} else {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s get error %s\n", err, cephStorageClass.Spec.ClusterConnectionName)
				return err
			}
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: %s CephClusterConnection received\n", cephClusterConnection.Name)

		if !cephClusterAuthenticationExists {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication %s not found\n", cephStorageClass.Spec.ClusterAuthenticationName)
			if cephClusterConnection.Spec.UserID != "" && cephClusterConnection.Spec.UserKey != "" {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s has UserID and UserKey. Marking CephStorageClass %s as migrated\n", cephClusterConnection.Name, cephStorageClass.Name)
				err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, MigratedLabelValueTrue)
				if err != nil {
					fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
					return err
				}
				continue
			}

			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s doesn't have UserID and UserKey. Marking CephStorageClass %s as not migrated\n", cephClusterConnection.Name, cephStorageClass.Name)
			err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, MigratedLabelValueFalse)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
				return err
			}
			continue
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication %s exists\n", cephClusterAuthenticationMigrate.CephClusterAuthentication.Name)

		err = processCephClusterConnection(ctx, cl, &cephStorageClass, cephClusterConnection, cephClusterAuthenticationMigrate.CephClusterAuthentication)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection process error %s\n", err)
			return err
		}

		err = cephStorageClassSetMigrateStatus(ctx, cl, &cephStorageClass, MigratedLabelValueTrue)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
			return err
		}
		cephClusterAuthenticationMigrate.RefCount--
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

		// remove finalizers
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

	err = processSecrets(ctx, cl)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Secrets process error %s\n", err)
		return err
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Finished migration from CephClusterAuthentication\n")

	return nil
}

func processCephClusterConnection(ctx context.Context, cl client.Client, cephStorageClass *v1alpha1.CephStorageClass, cephClusterConnection *v1alpha1.CephClusterConnection, cephClusterAuthentication *v1alpha1.CephClusterAuthentication) error {
	needUpdate := false
	needNewCephClusterConnection := false
	if cephClusterConnection.Spec.UserID != "" && cephClusterConnection.Spec.UserID != cephClusterAuthentication.Spec.UserID {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s UserID %s doesn't match CephClusterAuthentication %s UserID %s and both in use by CephStorageClass %s\n", cephClusterConnection.Name, cephClusterConnection.Spec.UserID, cephClusterAuthentication.Name, cephClusterAuthentication.Spec.UserID, cephStorageClass.Name)
		needNewCephClusterConnection = true
	}
	if cephClusterConnection.Spec.UserKey != "" && cephClusterConnection.Spec.UserKey != cephClusterAuthentication.Spec.UserKey {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s UserKey doesn't match CephClusterAuthentication %s UserKey and both in use by CephStorageClass %s\n", cephClusterConnection.Name, cephClusterAuthentication.Name, cephStorageClass.Name)
		needNewCephClusterConnection = true
	}

	if needNewCephClusterConnection {
		needNewCephClusterConnectionName := cephClusterConnection.Name + "-migrated-for-" + cephStorageClass.Name
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Creating new CephClusterConnection %s for CephStorageClass %s\n", needNewCephClusterConnectionName, cephStorageClass.Name)
		newCephClusterConnection := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: needNewCephClusterConnectionName,
				Labels: map[string]string{
					AutomaticallyCreatedLabel: AutomaticallyCreatedValue,
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
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection create error %s\n", err)
				return err
			}

			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s already exists. Checking if it's the same\n", newCephClusterConnection.Name)
			existingCephClusterConnection := &v1alpha1.CephClusterConnection{}
			err = cl.Get(ctx, types.NamespacedName{Name: newCephClusterConnection.Name, Namespace: ""}, existingCephClusterConnection)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s get error %s\n", newCephClusterConnection.Name, err)
				return err
			}
			if !cmp.Equal(existingCephClusterConnection.Spec, newCephClusterConnection.Spec) {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s already exists but different. Exiting with error\n", newCephClusterConnection.Name)
				return fmt.Errorf("CephClusterConnection %s already exists but different", newCephClusterConnection.Name)
			}
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s already exists and the same.", newCephClusterConnection.Name)
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: New CephClusterConnection %s created or already exists. Recreating CephStorageClass %s with new CephClusterConnection name\n", newCephClusterConnection.Name, cephStorageClass.Name)
		cephStorageClass.SetFinalizers([]string{})

		newCephStorageClass := cephStorageClass.DeepCopy()
		newCephStorageClass.Spec.ClusterConnectionName = newCephClusterConnection.Name
		if newCephStorageClass.Labels == nil {
			newCephStorageClass.Labels = make(map[string]string)
		}
		newCephStorageClass.Labels[MigratedWarningLabel] = MigratedWarningLabelValue
		newCephStorageClass.SetFinalizers([]string{})

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

		err = cl.Create(ctx, newCephStorageClass)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass create error %s\n", err)
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass %+v\n", newCephStorageClass)
			return err
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: New CephStorageClass %s created\n", newCephStorageClass.Name)
		return nil
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
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Updating CephClusterConnection %s\n", cephClusterConnection.Name)
		return cl.Update(ctx, cephClusterConnection)
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s doesn't need update\n", cephClusterConnection.Name)
	return nil
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
