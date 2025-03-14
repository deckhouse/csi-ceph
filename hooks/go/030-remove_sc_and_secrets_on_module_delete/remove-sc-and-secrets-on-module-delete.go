package hooks_common

import (
	"context"
	"fmt"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/hooks/go/consts"
	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"

	snapv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

var (
	AllowedProvisioners = []string{
		"rbd.csi.ceph.com",
		"cephfs.csi.ceph.com",
	}
)

var _ = registry.RegisterFunc(configRemoveScAndSecretsOnModuleDelete, handlerRemoveScAndSecretsOnModuleDelete)

var configRemoveScAndSecretsOnModuleDelete = &pkg.HookConfig{
	OnAfterDeleteHelm: &pkg.OrderedConfig{Order: 10},
}

func handlerRemoveScAndSecretsOnModuleDelete(ctx context.Context, _ *pkg.HookInput) error {
	fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Started removing SC and Secrets on module delete\n")

	cl, err := NewKubeClient()
	if err != nil {
		fmt.Printf("%s", err.Error())
		return err
	}

	secretList := &corev1.SecretList{}
	err = cl.List(ctx, secretList, client.InNamespace(consts.MODULE_NAMESPACE))
	if err != nil {
		fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Failed to list secrets: %v", err)
		return err
	}

	for _, secret := range secretList.Items {
		fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Removing finalizers from %s secret\n", secret.Name)

		patch := client.MergeFrom(secret.DeepCopy())
		secret.ObjectMeta.Finalizers = nil

		err = cl.Patch(ctx, &secret, patch)
		if err != nil {
			fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Failed to patch secret %s: %v", secret.Name, err)
			continue
		}
	}

	scList := &storagev1.StorageClassList{}
	err = cl.List(ctx, scList)
	if err != nil {
		fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Failed to list storage classes: %v", err)
		return err
	}

	for _, sc := range scList.Items {
		for _, provisioner := range AllowedProvisioners {
			if sc.Provisioner == provisioner {
				fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Removing finalizers from %s storage class\n", sc.Name)

				patch := client.MergeFrom(sc.DeepCopy())
				sc.ObjectMeta.Finalizers = nil

				err = cl.Patch(ctx, &sc, patch)
				if err != nil {
					fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Failed to patch storage class %s: %v", sc.Name, err)
					continue
				}

				fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Removing %s storage class\n", sc.Name)
				err = cl.Delete(ctx, &storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: sc.Name,
					},
				})
				if err != nil {
					fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Failed to delete storage class %s: %v", sc.Name, err)
				}
			}
		}
	}

	fmt.Printf("[remove-sc-and-secrets-on-module-delete]: Stoped removing SC and Secrets on module delete\n")

	return nil
}
