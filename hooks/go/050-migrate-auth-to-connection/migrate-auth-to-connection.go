package hooks_common

import (
	"context"
	"fmt"

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
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateAuthToConnection)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func cephStorageClassLabelUpdate(ctx context.Context, cl client.Client, cephStorageClass *v1alpha1.CephStorageClass, labelValue string) error {
	if cephStorageClass.Labels == nil {
		cephStorageClass.Labels = make(map[string]string)
	}

	cephStorageClass.Labels[MigratedLabel] = labelValue
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

		cephClusterConnection := &v1alpha1.CephClusterConnection{}
		cephClusterAuthentication := &v1alpha1.CephClusterAuthentication{}

		err = cl.Get(ctx, types.NamespacedName{Name: cephStorageClass.Spec.ClusterConnectionName, Namespace: ""}, cephClusterConnection)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s not found\n", cephStorageClass.Spec.ClusterConnectionName)
				err = cephStorageClassLabelUpdate(ctx, cl, &cephStorageClass, MigratedLabelValueFalse)
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

		err = cl.Get(ctx, types.NamespacedName{Name: cephStorageClass.Spec.ClusterAuthenticationName, Namespace: ""}, cephClusterAuthentication)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication %s not found\n", cephStorageClass.Spec.ClusterAuthenticationName)

				if cephClusterConnection.Spec.UserID != "" && cephClusterConnection.Spec.UserKey != "" {
					fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s already has UserID and UserKey. Marking CephStorageClass %s as migrated\n", cephClusterConnection.Name, cephStorageClass.Name)
					err = cephStorageClassLabelUpdate(ctx, cl, &cephStorageClass, MigratedLabelValueTrue)
					if err != nil {
						fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
						return err
					}
					continue
				}

				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s doesn't have UserID or UserKey. Skipping\n", cephClusterConnection.Name)
				err = cephStorageClassLabelUpdate(ctx, cl, &cephStorageClass, MigratedLabelValueFalse)
				if err != nil {
					fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
					return err
				}
				continue
			} else {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication %s get error %s\n", err, cephStorageClass.Spec.ClusterAuthenticationName)
				return err
			}
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: %s CephClusterAuthentication received\n", cephClusterAuthentication.Name)

		err = processCephClusterConnection(ctx, cl, &cephStorageClass, cephClusterConnection, cephClusterAuthentication)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection process error %s\n", err)
			return err
		}

		err = cephStorageClassLabelUpdate(ctx, cl, &cephStorageClass, MigratedLabelValueTrue)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
			return err
		}
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
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Creating new CephClusterConnection\n")
		newCephClusterConnection := &v1alpha1.CephClusterConnection{
			ObjectMeta: metav1.ObjectMeta{
				Name: cephClusterConnection.Name + "-migrated-for-" + cephStorageClass.Name,
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
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection create error %s\n", err)
			return err
		}
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: New CephClusterConnection %s created. Updating CephStorageClass with new CephClusterConnection name\n", newCephClusterConnection.Name)
		cephStorageClass.Spec.ClusterConnectionName = newCephClusterConnection.Name
		if cephStorageClass.Labels == nil {
			cephStorageClass.Labels = make(map[string]string)
		}
		cephStorageClass.Labels[MigratedWarningLabel] = MigratedWarningLabelValue
		return cl.Update(ctx, cephStorageClass)
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
