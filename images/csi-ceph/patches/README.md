# Patches

## 001-Fix-CVE-and-remove-go-mod-tidy.patch

Fix CVE and remove go mod tidy command from build

## 002-Fix-CVE.patch

Fix CVE

## 003-Fix-CVE.patch

Fix CVE

## 004-Fix-CVE.patch

Fix CVE

## 005-Fix-CVE.patch

Fix CVE: bump go.opentelemetry.io/otel to v1.41.0 (CVE-2026-29181)

## 006-Fix-CVE.patch

Fix CVE: bump golang.org/x/crypto to v0.52.0, golang.org/x/net to v0.55.0 and golang.org/x/sys to v0.45.0 (re-vendored)

## 007-Fix-CVE.patch

Fix CVE: bump golang.org/x/net to v0.56.0 (CVE-2026-46600) and golang.org/x/text to v0.39.0 (CVE-2026-56852); pulls golang.org/x/crypto to v0.53.0, golang.org/x/sys to v0.46.0, golang.org/x/sync to v0.21.0 and golang.org/x/term to v0.44.0 (re-vendored)

## 008-Fix-CVE.patch

Fix CVE: bump google.golang.org/grpc to v1.82.1 (GHSA-hrxh-6v49-42gf, xDS RBAC and HTTP/2 vulnerabilities), the version patch 001 had pinned; pulls google.golang.org/protobuf to v1.36.11, go.opentelemetry.io/otel (+ metric, trace) to v1.43.0, golang.org/x/oauth2 to v0.36.0 and google.golang.org/genproto/googleapis/rpc (re-vendored)

## 009-Reserve-filesystem-space-for-the-superuser.patch

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
