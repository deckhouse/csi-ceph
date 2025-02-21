package hooks_common

import (
	"fmt"

	consts "csi-ceph/consts"
)

var _ = func() {
	fmt.Printf("Module %s Hook test", consts.MODULE_NAME)
}
