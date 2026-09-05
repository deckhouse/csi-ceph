---
title: "The csi-ceph module: FAQ"
---

## How to get a list of RBD volumes separated by nodes?

For monitoring and diagnostics, it's useful to know which RBD volumes are connected to each cluster node. The following command provides detailed information about volume mapping:

```shell
d8 k -n d8-csi-ceph get po -l app=csi-node-rbd -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName --no-headers \
  | awk '{print "echo "$2"; kubectl -n d8-csi-ceph exec  "$1" -c node -- rbd showmapped"}' | bash
```

## Which versions of Ceph clusters are supported

The `csi-ceph` module has specific requirements for the Ceph cluster version to ensure compatibility and stable operation. Officially supported versions are >= 16.2.0. In practice, the current version works with clusters running versions >= 14.2.0, but it's recommended to update Ceph to the latest version.

## Which volume access modes are supported

Different types of Ceph storage support different volume access modes, which is important to consider when planning application architecture.

- **RBD**: Supports only ReadWriteOnce (RWO) — access to volume from only one cluster node.
- **CephFS**: Supports ReadWriteOnce (RWO) and ReadWriteMany (RWX) — simultaneous access to volume from multiple cluster nodes.

## Examples of permissions (caps) for Ceph users

To ensure proper operation of the `csi-ceph` module, Ceph users must have appropriate permissions (caps) configured. The required permissions depend on the storage type being used (RBD, CephFS, or both). Below are examples of correct permission configurations for different scenarios.

### RBD

For a single pool named `rbd`, the following permissions are required:

```ini
[client.name]
        key = key
        caps mgr = "profile rbd pool=rbd"
        caps mon = "profile rbd"
        caps osd = "profile rbd pool=rbd"
```

### CephFS

Before configuring CephFS permissions, ensure that a subvolumegroup `csi` (or another one specified in `Custom resources`) is created in CephFS.

You can create a new subvolumegroup using the following command on the Ceph management node:

```shell
ceph fs subvolumegroup create <fs_name> <group_name>
```

For example, to create a subvolumegroup `csi` for filesystem `myfs`:

```shell
ceph fs subvolumegroup create myfs csi
```

Required permissions for CephFS named `myfs`:

```ini
[client.name]
        key = key
        caps mds = "allow rwps fsname=myfs"
        caps mgr = "allow rw"
        caps mon = "allow r fsname=myfs"
        caps osd = "allow rw tag cephfs data=myfs, allow rw tag cephfs metadata=myfs"
```

### CephFS + RBD

For a user that needs access to both CephFS `myfs` and RBD pool `rbd`, combine the permissions as follows:

```ini
[client.name]
        key = key
        caps mds = "allow rwps fsname=myfs"
        caps mgr = "allow rw,profile rbd pool=rbd"
        caps mon = "allow r fsname=myfs,profile rbd"
        caps osd = "allow rw tag cephfs metadata=myfs, allow rw tag cephfs data=myfs,profile rbd pool=rbd"
```
## How much of a volume is reserved for the superuser?

Nothing, by default: an RBD image formatted with ext4 gets `-m0`, so the whole volume is available to the workload.

Where the classic 5% ext4 reserve is wanted — for example to keep a filesystem writable for a privileged process after a workload has filled it — annotate the `CephStorageClass` with the percentage to reserve:

```shell
d8 k annotate cephstorageclass <name> storage.deckhouse.io/ext4-reserved-percent=5
```

The value is a whole number of percent between 0 and 50; an invalid one puts the CephStorageClass into the `Failed` phase with the reason in its conditions, instead of breaking volume creation later.

The reserve applies only to volumes created after the annotation was set — filesystems that already exist keep the reserve they were created with. It is meaningful for RBD only: CephFS volumes are subdirectories of an existing filesystem and are never formatted, so the annotation is ignored on a CephFS class. Nor does it apply to a volume whose `defaultFSType` is XFS, which keeps no blocks for the superuser.

Changing the annotation makes the controller recreate the StorageClass, because `parameters` of an existing StorageClass are immutable in Kubernetes. Existing volumes and PVCs are not affected.
