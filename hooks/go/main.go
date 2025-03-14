package main

import (
	_ "github.com/deckhouse/csi-ceph/hooks/go/020-webhook-certs"
	_ "github.com/deckhouse/csi-ceph/hooks/go/030-remove-sc-and-secrets-on-module-delete"
	_ "github.com/deckhouse/csi-ceph/hooks/go/060-migrate-from-ceph-csi-module"
	"github.com/deckhouse/module-sdk/pkg/app"
)

func main() {
	app.Run()
}
