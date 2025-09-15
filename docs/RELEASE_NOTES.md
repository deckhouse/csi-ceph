---
title: "Release Notes"
---

## v0.5.7

* Added release notes

## v0.5.6

* Added additional mount points for containerd v2 support

## v0.5.5

* Added information about the need for snapshot-controller for module operation
* Added readonlyRootFilesystem for enhanced module security

## v0.5.4

* CVE fixes

## v0.5.3

* Added dependency on snapshot-controller
* CVE fixes

## v0.5.2

* Added fixes for containerd v2 support
* Fixes in CephClusterConnection processing

## v0.5.1

* Added support for subvolume group when specifying cluster data (field is also considered during migration from ceph-csi)

## v0.5.0

* Added automatic creation of VolumeSnapshotClassName annotations in StorageClass for proper snapshot-controller operation
* Updated CSI to 3.14.2
* Added setting to specify the number of worker threads for csi provisioner

## v0.4.4

* Module refactoring, documentation and build process fixes
* Added HA mode support for CSI controller

## v0.4.3

* Technical release, module refactoring

## v0.3.2

* Updated lib-helm and CSI code patch fixes

## v0.3.1

* Fixed hook for automatic merging of CephClusterAuthorization and CephClusterConnection resources (now considers VolumeSnapshots, not just PV)
* Cleaned metadata from images (cleaner images to reduce security client questions)

## v0.3.0

* Fixed error in generating internal configmap with ceph settings
* Added hook for automatic merging of CephClusterAuthorization and CephClusterConnection resources (only CephClusterConnection will remain with all necessary fields)

## v0.2.2

* Added labels to our CRDs for proper Deckhouse backup operation
* Fixed CSI controller templates for proper operation

## v0.2.1

* Updated golang to current 1.22.6 and replaced deprecated logging library to close known vulnerabilities

## v0.2.0

* Added liveness and readiness probes
* Updated ceph-csi version to 3.12.1, and updated dependencies for which there are known CVEs in old versions
