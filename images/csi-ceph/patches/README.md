# Patches

The patches are applied, in name order, to the `ceph/ceph-csi` checkout of the
tag recorded in the repository's `oss.yaml` (`id: cephcsi`). Regenerate them
whenever that version changes.

## 001-Remove-go-mod-tidy-from-the-vendored-build.patch

Drop `go mod tidy` from the `cephcsi` make target: the build is offline and
vendored, and tidy would try to reach the module proxy.

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

## 004-Fix-CVE.patch

Fix CVE: bump golang.org/x/net to v0.60.0 (CVE-2026-78669) and re-vendor.
The tag already ships golang.org/x/text v0.42.0.
