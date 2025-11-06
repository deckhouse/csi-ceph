---
title: "Модуль csi-ceph: FAQ"
---

## Как получить список томов RBD, разделенный по узлам?

```shell
kubectl -n d8-csi-ceph get po -l app=csi-node-rbd -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName --no-headers \
  | awk '{print "echo "$2"; kubectl -n d8-csi-ceph exec  "$1" -c node -- rbd showmapped"}' | bash
```

## Какие версии Ceph кластеров поддерживаются

Официально сейчас поддерживаются версии >= 16.2.0. Из нашей практики текущая версия способна работать с кластерами версий >=14.2.0, но мы рекомендуем обновить версию Ceph.

## Какие режимы работы томов поддерживаются

RBD поддерживает только ReadWriteOnce (RWO, доступ к тому в рамках одной ноды). CephFS поддерживает как ReadWriteOnce, так и ReadWriteMany (RWX, одновременный доступ к тому с нескольких нод)

## Примеры разрешений (caps) для пользователей в Ceph

### RBD

Для одного пула с названием `rbd`:

```
[client.name]
        key = key
        caps mgr = "profile rbd pool=rbd"
        caps mon = "profile rbd"
        caps osd = "profile rbd pool=rbd"
```

### CephFS

В CephFS должен быть создан subvolumegroup `csi` или другой, если это настроено согласно `Custom resources`.

Создать новый subvolumegroup можно командой (на узле управления Ceph):
```
ceph fs subvolumegroup create <fs_name> <group_name>
```

Например, если fs `myfs`, subvolumegroup `csi`:
```
ceph fs subvolumegroup create myfs csi
```

Caps для CephFS с названием `myfs`:

```
[client.name]
        key: key
        caps: [mds] allow rwps fsname=myfs
        caps: [mgr] allow rw
        caps: [mon] allow r fsname=myfs
        caps: [osd] allow rw tag cephfs data=myfs, allow rw tag cephfs metadata=myfs
```

### CephFS + RBD

Пример пользователя с разрешениями в CephFS `myfs` и RBD пул `rbd`:

```
[client.name]
        key = key
        caps mds = "allow rwps fsname=myfs"
        caps mgr = "allow rw,profile rbd pool=rbd"
        caps mon = "allow r fsname=myfs,profile rbd"
        caps osd = "allow rw tag cephfs metadata=myfs, allow rw tag cephfs data=myfs,profile rbd pool=rbd"
```
