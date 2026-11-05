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

`profile rbd` in the mon caps lets the user add other Ceph clients to the OSD blocklist. A node needs this to map an RBD volume whose previous node went down or was rebooted: the kernel RBD client blocklists the old owner of the volume's exclusive lock before taking the lock. Without it the Pod does not start on the new node, as described in the question about `Permission denied` below. If the mon caps list individual commands instead of the profile, they must include `allow command "osd blocklist"`; the name `osd blacklist` used before Ceph 16 is not enough.

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

## Why is a CephClusterConnection not deleted?

A `CephClusterConnection` holds the Secret with the user its volumes were created with, and Kubernetes reads that Secret to attach, detach, mount, expand and delete these volumes. Deleting the connection earlier would leave them stuck, for example a volume that can no longer be detached from its node. So the connection and its Secret stay until no PersistentVolume needs them. Its `Ready` condition is `False` with the reason `PersistentVolumesExist`, gives the number of such PersistentVolumes and names up to ten of them:

```shell
d8 k get cephclusterconnection <name> -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'
```

A PersistentVolume holds the connection while it is attached to a node, while it is `Bound` or not yet claimed, and while it is `Released` or `Failed` with the `Delete` reclaim policy, until the volume is deleted. A `Released` or `Failed` PersistentVolume with the `Retain` reclaim policy that is attached nowhere does not hold it: delete such a PersistentVolume by hand once its data is no longer needed, as it can no longer be used without the connection.

Snapshots hold the connection too: the Secret is needed to delete a snapshot, so a `VolumeSnapshotContent` with the `Delete` deletion policy holds the connection until it is deleted. When only snapshots are left, the reason is `VolumeSnapshotContentsExist` and the message names up to ten `VolumeSnapshotContent`s. Delete their `VolumeSnapshot`s for the connection to go.

## What does the ClusterConfigConflict condition of a CephClusterConnection mean?

ceph-csi keeps one entry per Ceph cluster in its configuration, and that entry holds the user of one `CephClusterConnection` for RBD and the subvolume group of one for CephFS. When several connections point at the same cluster, some volumes can need what the entry does not hold: RBD volumes created before csi-ceph started putting the detach secret into the PersistentVolume, if their connection has another user, and CephFS volumes lying in another subvolume group. Such an RBD volume fails to detach from its node; such a CephFS volume can still be mounted but no longer deleted, expanded or snapshotted.

`ClusterConfigConflict=True` on a connection names these volumes and the connection whose settings the entry holds. Find all affected connections with:

```shell
d8 k get cephclusterconnection -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.conditions[?(@.type=="ClusterConfigConflict")].status}{"\t"}{.status.conditions[?(@.type=="ClusterConfigConflict")].message}{"\n"}{end}' | awk -F'\t' '$2 == "True"'
```

The entry keeps the settings of the connection whose volumes depend on it, so the condition stays until the listed volumes are gone. Volumes created from now on do not depend on the entry for RBD. For CephFS, keep one subvolume group per Ceph cluster across its connections.

The secret of an existing PersistentVolume cannot be changed: `spec.csi` of a PersistentVolume is immutable. For the listed RBD volumes to detach, give the Ceph user of the connection the entry holds access to their pools. `ceph auth caps` replaces all the caps of a user, so read the current ones first and repeat them:

```shell
ceph auth get client.<user of the connection the entry holds>
ceph auth caps client.<user> mon '<current mon caps>' osd '<current osd caps>, profile rbd pool=<pool of the listed volumes>'
```

A listed volume whose data is no longer needed can be deleted instead.

## Why does an RBD volume fail to map with `Permission denied` after its Pod moved to another node?

An RBD volume is mapped under an exclusive lock held by one node. When a Pod moves from a node that went down or was rebooted, the lock is still held by the Ceph client of that node, so the kernel RBD client on the new node breaks it: it adds the old client to the OSD blocklist, so that the old client can no longer write, and then takes the lock. If the Ceph user of the `CephClusterConnection` is not allowed to blocklist clients, the volume is not mapped and the Pod stays in `ContainerCreating`. Its events show:

```text
rbd: map failed: (13) Permission denied
```

and the kernel log (`dmesg`) of the new node shows:

```text
breaking header lock owned by client<N>
failed to blocklist client<N>: -13
failed to acquire lock: -13
```

Check the caps of the user:

```shell
ceph auth get client.<user>
```

The mon caps must include `profile rbd` or `allow command "osd blocklist"`. Caps that allow only `osd blacklist`, the name of the command before Ceph 16, do not let the kernel blocklist a client. `ceph auth caps` replaces all the caps of a user, so repeat the current caps of every type the user has (mon, osd, mgr, mds) and add the missing one to mon:

```shell
ceph auth caps client.<user> mon '<current mon caps>, profile rbd' osd '<current osd caps>' mgr '<current mgr caps>'
```

Kubernetes keeps retrying to map the volume, so the Pod starts on its own once the caps are fixed.

If the caps cannot be changed right away, the lock can be removed by hand. Do it only when the old node is powered off or no longer uses the volume: otherwise two nodes write to the same image and corrupt its filesystem. The next move of a Pod from a failed node will fail the same way until the caps are fixed.

```shell
d8 k get pv <PV name> -o jsonpath='{.spec.csi.volumeAttributes.pool}/{.spec.csi.volumeAttributes.imageName}{"\n"}'
rbd lock ls <pool>/<image>
rbd lock rm <pool>/<image> '<ID>' <Locker>
```

Take `<ID>` and `<Locker>` from the `rbd lock ls` output; the ID of an exclusive lock contains a space, so quote it.

## What happens to snapshots and restored volumes when one of them is deleted?

Any order of deletion is allowed: a PVC, a VolumeSnapshot and a volume restored from it can each be deleted while the others exist. The space in Ceph, however, is freed only when nothing depends on it any more.

- **RBD.** A volume restored from a snapshot is a copy-on-write clone: it shares data with the snapshot until ceph-csi flattens it, that is, copies the data so that the clone no longer depends on its parent. ceph-csi flattens clones in the background when a chain of snapshots and restores reaches 4 levels (the hard limit is 8) or an image has more than 250 snapshots; a flattened volume takes its full size in the pool. A deleted image is moved to the RBD trash and stays there while snapshots or restored volumes still depend on it; the Ceph manager removes it afterwards, retrying every few minutes. To see what is still held, run `rbd trash ls <pool>` in the Ceph cluster.
- **CephFS.** A read-write volume restored from a snapshot is a full copy, made in the background: the PVC binds when the copy is done, and until then deleting the snapshot waits for the copy. A deleted volume that has snapshots stays in Ceph with its snapshots and goes away with the last of them. A read-only (`ReadOnlyMany`) volume restored from a snapshot uses the snapshot itself and keeps it in Ceph until the volume is deleted.
- The VolumeSnapshotClass of a CephStorageClass takes the `reclaimPolicy` of the class: with `Retain`, deleting a VolumeSnapshot leaves the snapshot in Ceph.
