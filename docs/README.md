---
title: "The csi-ceph module"
---

Ceph is a scalable distributed storage system that ensures high availability and fault tolerance of data.Deckhouse Kubernetes Platform (DKP) provides Ceph cluster integration using the `csi-ceph` module. This enables dynamic storage management and the use of StorageClass based on RADOS Block Device (RBD) or CephFS.

{{< alert level="info" >}}
The [snapshot-controller](/modules/snapshot-controller/) module is required for working with snapshots.
{{< /alert >}}

This page provides instructions on connecting Ceph to Deckhouse, configuring authentication, creating StorageClass objects, and verifying storage functionality.

## Migration from `ceph-csi` module

When switching from the `ceph-csi` module to `csi-ceph`, an automatic migration is performed, but its execution requires preliminary preparation:

1. Set the replica count to zero for all operators (redis, clickhouse, kafka, etc.). Exception: the `prometheus` operator will be disabled automatically.

1. Disable the `ceph-csi` module and [enable](#connecting-to-ceph-cluster) `csi-ceph`.

1. Wait for the operation to complete. The Deckhouse logs should show the message "Finished migration from Ceph CSI module".

1. Verify functionality. Create test pods and PVCs to test CSI.

1. Restore operators to working state.

{{< alert level="warning" >}}
If Ceph StorageClass was created without using the `CephCSIDriver` resource, manual migration will be required. Contact technical support.
{{< /alert >}}

## Connecting to Ceph cluster

To connect to a Ceph cluster, follow the step-by-step instructions below. Execute all commands on a machine with administrative access to the Kubernetes API.

1. Execute the command to activate the `csi-ceph` module:

   ```shell
   d8 s module enable csi-ceph
   ```

1. Wait for the module to transition to `Ready` state:

   ```shell
   d8 k get module csi-ceph -w
   ```

1. Ensure that all pods in the `d8-csi-ceph` namespace are in `Running` or `Completed` state and deployed on all cluster nodes:

   ```shell
   d8 k -n d8-csi-ceph get pod -owide -w
   ```

1. To configure the connection to the Ceph cluster, apply the [CephClusterConnection](/modules/csi-ceph/cr.html#cephclusterconnection) resource.

   Example command:

   ```shell
   d8 k apply -f - <<EOF
   apiVersion: storage.deckhouse.io/v1alpha1
   kind: CephClusterConnection
   metadata:
     name: ceph-cluster-1
   spec:
     # FSID/UUID of the Ceph cluster.
     # Get the FSID/UUID of the Ceph cluster using the command `ceph fsid`.
     clusterID: 2bf085fc-5119-404f-bb19-820ca6a1b07e
     # List of IP addresses of ceph-mon in format 10.0.0.10:6789.
     monitors:
       - 10.0.0.10:6789
     # Username without `client.`.
     # Get the username using the command `ceph auth list`.
     userID: admin
     # Authorization key corresponding to userID.
     # Get the authorization key using the command `ceph auth get-key client.admin`.
     userKey: AQDiVXVmBJVRLxAAg65PhODrtwbwSWrjJwssUg==
   EOF
   ```

1. Verify the connection creation with the command (`Phase` should be in `Created` status):

   ```shell
   d8 k get cephclusterconnection ceph-cluster-1
   ```

1. Create a StorageClass object using the [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass) resource. Manual creation of StorageClass without using [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass) may lead to errors.

   Example of creating StorageClass based on RBD:

   ```shell
   d8 k apply -f - <<EOF
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
   EOF
   ```

   Example of creating StorageClass based on Ceph filesystem:

   ```shell
   d8 k apply -f - <<EOF
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
   EOF
   ```

1. Verify that the created [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass) resources have transitioned to `Created` state:

   ```shell
   d8 k get cephstorageclass
   ```

   This will output information about the created [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass) resources:

   ```console
   NAME          PHASE     AGE
   ceph-rbd-sc   Created   1h
   ceph-fs-sc    Created   1h
   ```

1. Verify the created StorageClass:

   ```shell
   d8 k get sc
   ```

   This will output information about the created StorageClass:

   ```console
   NAME          PROVISIONER        RECLAIMPOLICY   VOLUMEBINDINGMODE      ALLOWVOLUMEEXPANSION   AGE
   ceph-rbd-sc   rbd.csi.ceph.com   Delete          WaitForFirstConsumer   true                   15s
   ceph-fs-sc    rbd.csi.ceph.com   Delete          WaitForFirstConsumer   true                   15s
   ```

Ceph cluster connection setup is complete. You can use the created StorageClass to create PersistentVolumeClaim in your applications.
