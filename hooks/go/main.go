package main

import (
	"github.com/deckhouse/module-sdk/pkg/app"

	_ "csi-ceph/050-migrate-auth-to-connection"
	_ "csi-ceph/060-migrate-from-ceph-csi-module"
	_ "csi-ceph/070-remove-finalizers-on-module-delete"
)

func main() {
	app.Run()
}
