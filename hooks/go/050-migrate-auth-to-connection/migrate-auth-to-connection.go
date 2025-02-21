package hooks_common

import (
	"context"
	"fmt"
	"os"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateAuthToConnection)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func NewKubeClient(kubeconfigPath string) (client.Client, error) {
	var config *rest.Config
	var err error

	if kubeconfigPath == "" {
		kubeconfigPath = os.Getenv("kubeconfig")
	}

	controllerruntime.SetLogger(logr.New(ctrllog.NullLogSink{}))

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

func handlerMigrateAuthToConnection(_ context.Context, input *pkg.HookInput) error {
	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Started migration from CephClusterAuthetication")

	ctx := context.Background()
	cl, err := NewKubeClient("")
	if err != nil {
		klog.Fatal(err.Error())
	}

	cephStorageClassList := &v1alpha1.CephStorageClassList{}

	err = cl.List(ctx, cephStorageClassList)
	if err != nil {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephStorageClassList get error %s", err)
		return err
	}

	for _, item := range cephStorageClassList.Items {
		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: migrating %s", item.Name)

		cephClusterConnection := &v1alpha1.CephClusterConnection{}
		cephClusterAuthentication := &v1alpha1.CephClusterAuthentication{}

		err = cl.Get(ctx, types.NamespacedName{Name: item.Spec.ClusterConnectionName, Namespace: ""}, cephClusterConnection)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterConnection get error %s", err)
			return err
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: got %s CephClusterConnection", cephClusterConnection.Name)

		err = cl.Get(ctx, types.NamespacedName{Name: item.Spec.ClusterAuthenticationName, Namespace: ""}, cephClusterAuthentication)
		if err != nil {
			fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: CephClusterAuthentication get error %s", err)
			return err
		}

		fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: got %s CephClusterAuthentication", cephClusterAuthentication.Name)
	}

	fmt.Printf("[csi-ceph-migration-from-ceph-cluster-authentication]: Finished migration from CephClusterAuthetication")

	return nil
}
