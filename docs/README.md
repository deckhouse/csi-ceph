---
title: "The csi-ceph module"
---

{{< alert level="warning" >}}
When switching to this module from the ceph-csi module, an automatic migration is performed.
If the spec.cephfs.storageClasses.pool field in the CephCSIDriver resources is set to a value other than cephfs_data, the migration will fail with an error.
In this case, it is necessary to contact technical support.
{{< /alert >}}

The module installs and configures the CSI driver for RBD and CephFS.

The module is configured by [Custom Resources](cr.html), which allows you to connect more than one Ceph cluster (UUIDs must be different).
