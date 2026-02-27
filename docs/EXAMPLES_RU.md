---
title: "Модуль csi-ceph: примеры"
---

## Настройка ресурса CephClusterConnection

Ресурс [CephClusterConnection](/modules/csi-ceph/cr.html#cephclusterconnection) определяет параметры подключения к вашему Ceph-кластеру. Этот ресурс должен быть создан перед созданием объектов [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass).

Пример конфигурации:

```yaml
apiVersion: storage.deckhouse.io/v1alpha1
kind: CephClusterConnection
metadata:
  name: ceph-cluster-1
spec:
  clusterID: 014df517-39d1-4453-b7b3-9930c563627c
  monitors:
  - 172.20.1.28:6789
  - 172.20.1.34:6789
  - 172.20.1.37:6789
  userID: admin
  userKey: <your-ceph-auth-key>
```

Чтобы проверить создание объекта, выполните следующую команду (Phase должен быть `Created`):

```shell
d8 k get cephclusterconnection <имя cephclusterconnection>
```

## Настройка ресурса CephStorageClass

Ресурс [CephStorageClass](/modules/csi-ceph/cr.html#cephstorageclass) определяет параметры класса хранилища для создания постоянных томов. Вы можете создать различные классы хранилища для типов RBD и CephFS.

### RBD

Пример конфигурации класса хранилища для томов RBD (RADOS Block Device):

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

Пример конфигурации класса хранилища для томов CephFS (Ceph File System):

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

Чтобы проверить создание объекта, выполните следующую команду (Phase должен быть `Created`):

```shell
d8 k get cephstorageclass <имя cephstorageclass>
```
