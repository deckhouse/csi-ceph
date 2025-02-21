package main

import (
	"github.com/deckhouse/module-sdk/pkg/app"
	// подключаем все нужные/созданные хуки (package-ы) здесь

	_ "csi-ceph/050-migrate-auth-to-connection"
)

func main() {
	app.Run()
}
