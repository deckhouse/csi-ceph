package hooks_common

import (
	"context"
	"fmt"

	consts "csi-ceph/consts"

	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateAuthToConnection)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func handlerMigrateAuthToConnection(_ context.Context, input *pkg.HookInput) error {
	fmt.Printf("Module %s Hook test", consts.MODULE_NAME)

	// Создаем REST-клиент с помощью controller-runtime
	cfg, err := config.GetConfigWithContext("")
	if err != nil {
		fmt.Printf("Ошибка загрузки конфигурации Kubernetes: %v\n", err)
		return err
	}

	// Создаем клиент для работы с Kubernetes API
	cl, err := client.New(cfg, client.Options{})
	if err != nil {
		fmt.Printf("Ошибка создания Kubernetes клиента: %v\n", err)
		return err
	}

	var cephClusterAuthList v1alpha1.CephClusterAuthenticationList
	err = cl.List(context.TODO(), &cephClusterAuthList)
	if err != nil {
		fmt.Printf("Ошибка при запросе CephClusterAuthenticationList: %v\n", err)
		return nil
	}

	return nil
}
