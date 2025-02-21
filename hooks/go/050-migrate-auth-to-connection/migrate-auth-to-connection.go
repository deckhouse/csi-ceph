package hooks_common

import (
	"context"
	"fmt"

	consts "csi-ceph/consts"

	"github.com/deckhouse/module-sdk/pkg"
	"github.com/deckhouse/module-sdk/pkg/registry"
)

var _ = registry.RegisterFunc(configMigrateAuthToConnection, handlerMigrateAuthToConnection)

var configMigrateAuthToConnection = &pkg.HookConfig{
	OnBeforeHelm: &pkg.OrderedConfig{Order: 5},
}

func handlerMigrateAuthToConnection(_ context.Context, input *pkg.HookInput) error {
	fmt.Printf("Module %s Hook test", consts.MODULE_NAME)

	return nil
}
