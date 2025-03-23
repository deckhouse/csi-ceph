package hooks_common

import (
	"context"
	"fmt"

	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"

	funcs "csi-ceph/funcs"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	configMapName = "ceph-csi-config"
	namespace     = "d8-csi-ceph"
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerRemoveFinalizersOnModuleDelete)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnAfterDeleteHelm: &pkg.OrderedConfig{Order: 5},
}

func handlerRemoveFinalizersOnModuleDelete(ctx context.Context, input *pkg.HookInput) error {
	fmt.Printf("[csi-ceph-remove-finalizers-on-module-delete]: removing finalizers\n")

	cephConfigMap := &v1.ConfigMap{}

	cl, err := funcs.NewKubeClient()
	if err != nil {
		fmt.Printf("%s", err.Error())
		return err
	}

	err = cl.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: namespace}, cephConfigMap)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			fmt.Printf("[csi-ceph-remove-finalizers-on-module-delete]: error getting configmap %s: %s\n", configMapName, err)
			return err
		}
		fmt.Printf("[csi-ceph-remove-finalizers-on-module-delete]: configmap %s not found, nothing to clean up\n", configMapName)
		return nil
	}

	if len(cephConfigMap.Finalizers) == 0 {
		fmt.Printf("[csi-ceph-remove-finalizers-on-module-delete]: configmap %s has no finalizers, nothing to clean up\n", configMapName)
		return nil
	}

	fmt.Printf("[csi-ceph-remove-finalizers-on-module-delete]: configmap %s has finalizers %v. Removing them\n", configMapName, cephConfigMap.Finalizers)

	cephConfigMap.Finalizers = nil
	err = cl.Update(ctx, cephConfigMap)
	if err != nil {
		fmt.Printf("[csi-ceph-remove-finalizers-on-module-delete]: configmap %s update error %s\n", configMapName, err)
		return err
	}

	fmt.Printf("[csi-ceph-remove-finalizers-on-module-delete]: finalizers removed\n")

	return nil
}
