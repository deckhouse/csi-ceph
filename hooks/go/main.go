package main

import (
	"github.com/deckhouse/module-sdk/pkg/app"

	_ "github.com/deckhouse/csi-ceph/hooks/go/020-webhook-certs"
	_ "github.com/deckhouse/csi-ceph/hooks/go/030-remove-sc-and-secrets-on-module-delete"
	_ "github.com/deckhouse/csi-ceph/hooks/go/050-migrate-auth-to-connection"
)

func main() {
	app.Run()
}
