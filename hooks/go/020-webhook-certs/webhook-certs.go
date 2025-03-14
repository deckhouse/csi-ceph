package hooks_webhook_certs

import (
	"fmt"

	consts "github.com/deckhouse/csi-ceph/hooks/go/consts"

	tlscertificate "github.com/deckhouse/module-sdk/common-hooks/tls-certificate"
)

var _ = tlscertificate.RegisterInternalTLSHookEM(tlscertificate.GenSelfSignedTLSHookConf{
	CN:            consts.WEBHOOKS_CERT_CN,
	TLSSecretName: fmt.Sprintf("%s-webhook-cert", consts.WEBHOOKS_CERT_CN),
	Namespace:     consts.MODULE_NAMESPACE,
	SANs: tlscertificate.DefaultSANs([]string{
		consts.WEBHOOKS_CERT_CN,
		fmt.Sprintf("%s.%s", consts.WEBHOOKS_CERT_CN, consts.MODULE_NAMESPACE),
		fmt.Sprintf("%s.%s.svc", consts.WEBHOOKS_CERT_CN, consts.MODULE_NAMESPACE),
		// %CLUSTER_DOMAIN%:// is a special value to generate SAN like 'svc_name.svc_namespace.svc.cluster.local'
		fmt.Sprintf("%%CLUSTER_DOMAIN%%://%s.%s.svc", consts.WEBHOOKS_CERT_CN, consts.MODULE_NAMESPACE),
	}),
	FullValuesPathPrefix: fmt.Sprintf("%s.internal.customWebhookCert", consts.MODULE_NAME),
})
