// Code-generation tools for this repository, pinned here rather than in
// api/go.mod: controller-tools drags k8s.io/apiextensions-apiserver and
// k8s.io/code-generator into whatever module requires it, and api/ is imported
// by images/controller, so pinning them there would bump the dependencies the
// controller itself is built against.
//
// The pinned versions are the whole point of this module: the CRDs in crds/ are
// a generated artifact, so the generator is part of their definition and a
// floating version would make the committed manifests depend on when they were
// last rendered. controller-tools v0.18.0 matches the other storage modules that
// already generate their CRDs. crd-enricher is pinned to the deckhouse commit
// that added unset:<key> and the strict flag (deckhouse#22406): no release tag
// carries them yet, so move to the tag once one is cut. Bump with, from this
// directory:
//
//	go get -tool sigs.k8s.io/controller-tools/cmd/controller-gen@vX.Y.Z
//	go get -tool github.com/deckhouse/deckhouse/pkg/crd-enricher/cmd/crd-enricher@vX.Y.Z
//
// and regenerate: a bump that changes the rendered output shows up as a diff in
// crds/ in the same commit, which is exactly where it should be reviewed.
module csi-ceph-codegen-tools

go 1.27.3
tool (
	github.com/deckhouse/deckhouse/pkg/crd-enricher/cmd/crd-enricher
	sigs.k8s.io/controller-tools/cmd/controller-gen
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/deckhouse/deckhouse/pkg/crd-enricher v0.0.3-0.20261101060602-bd2c59c6fec4 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.27.1 // indirect
	github.com/go-openapi/swag/cmdutils v0.27.1 // indirect
	github.com/go-openapi/swag/conv v0.27.1 // indirect
	github.com/go-openapi/swag/fileutils v0.27.1 // indirect
	github.com/go-openapi/swag/jsonutils v0.27.1 // indirect
	github.com/go-openapi/swag/loading v0.27.1 // indirect
	github.com/go-openapi/swag/mangling v0.27.1 // indirect
	github.com/go-openapi/swag/netutils v0.27.1 // indirect
	github.com/go-openapi/swag/pools v0.27.1 // indirect
	github.com/go-openapi/swag/stringutils v0.27.1 // indirect
	github.com/go-openapi/swag/typeutils v0.27.1 // indirect
	github.com/go-openapi/swag/yamlutils v0.27.1 // indirect
	github.com/gobuffalo/flect v1.0.3 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	k8s.io/api v0.37.2 // indirect
	k8s.io/apiextensions-apiserver v0.37.2 // indirect
	k8s.io/apimachinery v0.37.2 // indirect
	k8s.io/code-generator v0.37.2 // indirect
	k8s.io/gengo/v2 v2.0.0-20260408192533-25e2208e0dc3 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	k8s.io/utils v0.0.0-20260626114624-be93311217bd // indirect
	sigs.k8s.io/controller-tools v0.23.0 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)
