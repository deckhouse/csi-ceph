# Patches

The patches are applied, in name order, to the `ceph/ceph-csi` checkout of the
tag recorded in the repository's `oss.yaml` (`id: cephcsi`). Regenerate them
whenever that version changes.

## 001-Remove-go-mod-tidy-from-the-vendored-build.patch

Drop `go mod tidy` from the `cephcsi` make target: the build is offline and
vendored, and tidy would try to reach the module proxy.

## 002-Fix-CVE.patch

Fix CVE: bump the dependencies that carry 2026 advisories and re-vendor —
golang.org/x/crypto to v0.57.0, golang.org/x/net to v0.59.0, golang.org/x/sys
to v0.48.0, golang.org/x/text to v0.42.0, golang.org/x/sync to v0.23.0,
golang.org/x/term to v0.46.0, golang.org/x/oauth2 to v0.37.0,
google.golang.org/grpc to v1.83.2, google.golang.org/protobuf to v1.36.12 and
go.opentelemetry.io/otel (+ metric, trace) to v1.46.0. Pulls
github.com/go-jose/go-jose/v4 v4.1.4, github.com/go-logr/logr v1.4.4,
github.com/stretchr/testify v1.12.1, go.yaml.in/yaml/v3 v3.0.5,
google.golang.org/genproto/googleapis/rpc and raises the `go` directive to
1.26.0.

`go.opentelemetry.io/contrib/.../otelgrpc` deliberately stays at the tag's
v0.60.0: newer releases dropped `otelgrpc.UnaryClientInterceptor`, which the
vendored `kubernetes-csi/csi-lib-utils` still calls.

## 003-Reserve-filesystem-space-for-the-superuser.patch

Honour the `storage.deckhouse.io/ext4-reserved-percent` StorageClass parameter
when formatting an RBD image with ext4: append `-m<percent>` to the arguments
ceph-csi picked itself.

Upstream already offers `mkfsOptions`, but it **replaces** the whole argument
list, including the choice between `assume_storage_prezeroed=1` and
`lazy_itable_init=1,lazy_journal_init=1` that `getMkfsArgs()` makes from what
the node supports — a choice the controller cannot make on the node's behalf.
Appending keeps it, and `mkfs.ext4` honours the last `-m` on the command line.

The annotation key is spelled out here instead of coming from
`sds-common-lib/fsreserve`, because this file is upstream code that cannot
import our modules. Keep the two in sync.
