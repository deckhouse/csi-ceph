---
title: "Модуль csi-ceph: FAQ"
---

## Как получить список томов RBD, разделенный по узлам?

Для мониторинга и диагностики полезно знать, какие RBD-тома подключены к каждому узлу кластера. Следующая команда позволяет получить детальную информацию о маппинге томов:

```shell
d8 k -n d8-csi-ceph get po -l app=csi-node-rbd -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName --no-headers \
  | awk '{print "echo "$2"; kubectl -n d8-csi-ceph exec  "$1" -c node -- rbd showmapped"}' | bash
```

## Какие версии Ceph кластеров поддерживаются

Модуль `csi-ceph` имеет определенные требования к версии Ceph-кластера для обеспечения совместимости и стабильной работы. Официально поддерживаются версии Ceph >= 16.2.0. На практике текущая версия модуля обычно работает и с кластерами >= 14.2.0, но для надёжной эксплуатации рекомендуется обновить Ceph до актуальной поддерживаемой версии.

## Какие режимы работы томов поддерживаются

Различные типы хранилища Ceph поддерживают разные режимы доступа к томам, что важно учитывать при планировании архитектуры приложений.

- **RBD** — поддерживает только ReadWriteOnce (RWO) — доступ к тому только с одного узла кластера.
- **CephFS** — поддерживает ReadWriteOnce (RWO) и ReadWriteMany (RWX) — одновременный доступ к тому с нескольких узлов кластера.

## Разрешения (caps) для пользователей в Ceph

Для обеспечения корректной работы модуля `csi-ceph` пользователи Ceph должны иметь соответствующие разрешения (caps). Необходимые разрешения зависят от используемого типа хранилища. Ниже приведены примеры правильных конфигураций разрешений для различных сценариев.

### RBD

Для одного пула с названием `rbd` требуются следующие разрешения:

```ini
[client.name]
        key = key
        caps mgr = "profile rbd pool=rbd"
        caps mon = "profile rbd"
        caps osd = "profile rbd pool=rbd"
```

### CephFS

Перед настройкой разрешений CephFS убедитесь, что в CephFS создан subvolumegroup `csi` (или другой, указанный в `Custom resources`).

Создать новый subvolumegroup можно командой на узле управления Ceph:

```shell
ceph fs subvolumegroup create <fs_name> <group_name>
```

Например, для создания subvolumegroup `csi` для файловой системы `myfs`:

```shell
ceph fs subvolumegroup create myfs csi
```

Требуемые разрешения для CephFS с названием `myfs`:

```ini
[client.name]
        key = key
        caps mds = "allow rwps fsname=myfs"
        caps mgr = "allow rw"
        caps mon = "allow r fsname=myfs"
        caps osd = "allow rw tag cephfs data=myfs, allow rw tag cephfs metadata=myfs"
```

### CephFS + RBD

Для пользователя, которому необходим доступ к CephFS `myfs` и RBD пулу `rbd`, объедините разрешения следующим образом:

```ini
[client.name]
        key = key
        caps mds = "allow rwps fsname=myfs"
        caps mgr = "allow rw,profile rbd pool=rbd"
        caps mon = "allow r fsname=myfs,profile rbd"
        caps osd = "allow rw tag cephfs metadata=myfs, allow rw tag cephfs data=myfs,profile rbd pool=rbd"
```
