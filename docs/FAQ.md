---
title: "The csi-ceph module: FAQ"
---

## How to get a list of RBD volumes separated by nodes?

```shell
kubectl -n d8-csi-ceph get po -l app=csi-node-rbd -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName --no-headers \
  | awk '{print "echo "$2"; kubectl -n d8-csi-ceph exec  "$1" -c node -- rbd showmapped"}' | bash
```

## Which versions of Ceph clusters are supported

Officially, versions >= 16.2.0 are currently supported. Based on our experience, the current version can work with clusters of versions >= 14.2.0, but we recommend updating the Ceph version.

## Which volume access modes are supported

RBD supports only ReadWriteOnce (RWO, access to the volume within a single node). CephFS supports both ReadWriteOnce and ReadWriteMany (RWX, simultaneous access to the volume from multiple nodes).

## Examples of permissions (caps) for Ceph users

### RBD

For a single pool named `rbd`:

```
[client.name]
        key = key
        caps mgr = "profile rbd pool=rbd"
        caps mon = "profile rbd"
        caps osd = "profile rbd pool=rbd"
```

### CephFS

A subvolumegroup `csi` or another one must be created in CephFS, if configured according to `Custom resources`.

You can create a new subvolumegroup using the following command (on the Ceph management node):
```
ceph fs subvolumegroup create <fs_name> <group_name>
```

For example, if fs `myfs`, subvolumegroup `csi`:
```
ceph fs subvolumegroup create myfs csi
```

Caps for CephFS named `myfs`:

```
[client.name]
        key: key
        caps: [mds] allow rwps fsname=myfs
        caps: [mgr] allow rw
        caps: [mon] allow r fsname=myfs
        caps: [osd] allow rw tag cephfs data=myfs, allow rw tag cephfs metadata=myfs
```

### CephFS + RBD

Example of a user with permissions in CephFS `myfs` and RBD pool `rbd`:

```
[client.name]
        key = key
        caps mds = "allow rwps fsname=myfs"
        caps mgr = "allow rw,profile rbd pool=rbd"
        caps mon = "allow r fsname=myfs,profile rbd"
        caps osd = "allow rw tag cephfs metadata=myfs, allow rw tag cephfs data=myfs,profile rbd pool=rbd"
```
