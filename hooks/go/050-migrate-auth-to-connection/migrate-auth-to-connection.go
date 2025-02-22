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
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MigratedLabel      = "storage.deckhouse.io/migratedFromCephClusterAuthentication"
	MigratedLabelValue = "true"
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateAuthToConnection)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func cephStorageClassLabelUpdate(cephStorageClass *v1alpha1.CephStorageClass, cl client.Client, ctx context.Context) error {
	if cephStorageClass.Labels == nil {
		cephStorageClass.Labels = make(map[string]string)
	}

	cephStorageClass.Labels[MigratedLabel] = MigratedLabelValue
	err := cl.Update(ctx, cephStorageClass)
	return err
}

func NewKubeClient(kubeconfigPath string) (client.Client, error) {
	var config *rest.Config
	var err error

	config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)

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

	cl, err := NewKubeClient("")
	if err != nil {
		fmt.Printf("%s", err.Error())
	}

	cephStorageClassList := &v1alpha1.CephStorageClassList{}

	err = cl.List(ctx, cephStorageClassList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClassList get error %s\n", err)
		return err
	}

	for _, cephStorageClass := range cephStorageClassList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Migrating %s\n", cephStorageClass.Name)

		if cephStorageClass.Labels[MigratedLabel] == MigratedLabelValue {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: %s already migrated\n", cephStorageClass.Name)
			continue
		}

		cephClusterConnection := &v1alpha1.CephClusterConnection{}
		cephClusterAuthentication := &v1alpha1.CephClusterAuthentication{}

		err = cl.Get(ctx, types.NamespacedName{Name: cephStorageClass.Spec.ClusterConnectionName, Namespace: ""}, cephClusterConnection)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s not found\n", cephStorageClass.Spec.ClusterConnectionName)
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
				err = cephStorageClassLabelUpdate(&cephStorageClass, cl, ctx)
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

		if cephClusterConnection.Spec.UserID != "" || cephClusterConnection.Spec.UserKey != "" {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection %s already has UserID or UserKey\n", cephClusterConnection.Name)
			err = cephStorageClassLabelUpdate(&cephStorageClass, cl, ctx)
			if err != nil {
				fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
				return err
			}
			continue
		}

		cephClusterConnection.Spec.UserID = cephClusterAuthentication.Spec.UserID
		cephClusterConnection.Spec.UserKey = cephClusterAuthentication.Spec.UserKey

		err = cl.Update(ctx, cephClusterConnection)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection update error %s\n", err)
			return err
		}

		err = cephStorageClassLabelUpdate(&cephStorageClass, cl, ctx)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClass update error %s\n", err)
			return err
		}
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Finished migration from CephClusterAuthentication\n")

	return nil
}
