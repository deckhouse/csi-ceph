package main

import (
	"github.com/deckhouse/module-sdk/pkg/app"

	_ "csi-ceph/050-migrate-auth-to-connection"
)

func main() {
	app.Run()
}
