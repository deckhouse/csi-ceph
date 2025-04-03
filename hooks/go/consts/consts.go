package consts

const (
	ModuleName      	string = "csiCeph"
	ModuleNamespace 	string = "d8-csi-ceph"
	ModulePluralName	string = "csi-ceph"
	WebhookCertCn   	string = "webhooks"
)

var AllowedProvisioners = []string{
	"rbd.csi.ceph.com",
	"cephfs.csi.ceph.com",
}
