---
title: "The csi-ceph module: examples"
---

## CephClusterConnection configuration

The [CephClusterConnection](/modules/csi-ceph/cr.html#cephclusterconnection) resource defines the connection parameters to your Ceph cluster. This resource must be created before creating [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass) objects.

Example configuration:

```yaml
apiVersion: storage.deckhouse.io/v1alpha1
kind: CephClusterConnection
metadata:
  name: ceph-cluster-1
spec:
  clusterID: 0324bfe8-c36a-4829-bacd-9e28b6480de9
  monitors:
  - 172.20.1.28:6789
  - 172.20.1.34:6789
  - 172.20.1.37:6789
  userID: admin
  userKey: AQDiVXVmBJVRLxAAg65PhODrtwbwSWrjJwssUg==
```

To verify the creation of the object, use the following command (Phase should be `Created`):

```shell
d8 k get cephclusterconnection <cephclusterconnection name>
```

## CephStorageClass configuration

The [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass) resource defines storage class parameters for provisioning persistent volumes. You can create different storage classes for RBD and CephFS storage types.

### RBD

Example of a storage class configuration for RBD (RADOS Block Device) volumes:

```yaml
apiVersion: storage.deckhouse.io/v1alpha1
kind: CephStorageClass
metadata:
  name: ceph-rbd-sc
spec:
  clusterConnectionName: ceph-cluster-1
  reclaimPolicy: Delete
  type: RBD
  rbd:
    defaultFSType: ext4
    pool: ceph-rbd-pool
```

### CephFS

Example of a storage class configuration for CephFS (Ceph File System) volumes:

```yaml
apiVersion: storage.deckhouse.io/v1alpha1
kind: CephStorageClass
metadata:
  name: ceph-fs-sc
spec:
  clusterConnectionName: ceph-cluster-1
  reclaimPolicy: Delete
  type: CephFS
  cephFS:
    fsName: cephfs
```

To verify the creation of the object, use the following command (Phase should be `Created`):

```shell
d8 k get cephstorageclass <cephstorageclass name>
```
