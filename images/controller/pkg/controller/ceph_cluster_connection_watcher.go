/*
Copyright 2025 Flant JSC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/config"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/logger"
)

const (
	// This value used as a name for the controller AND the value for managed-by label.
	CephClusterConnectionCtrlName                = "d8-ceph-cluster-connection-controller"
	CephClusterConnectionControllerFinalizerName = "storage.deckhouse.io/ceph-cluster-connection-controller"
	CephClusterConnectionSubvolumeGroupConfigKey = "subvolumeGroup"

	provisionerDeletionSecretNameAnnotation      = "volume.kubernetes.io/provisioner-deletion-secret-name"
	provisionerDeletionSecretNamespaceAnnotation = "volume.kubernetes.io/provisioner-deletion-secret-namespace"

	snapshotDeletionSecretNameAnnotation      = "snapshot.storage.kubernetes.io/deletion-secret-name"
	snapshotDeletionSecretNamespaceAnnotation = "snapshot.storage.kubernetes.io/deletion-secret-namespace"

	maxListedNames = 10

	cephCSIDefaultSubvolumeGroup = "csi"
)

func RunCephClusterConnectionWatcherController(
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
) (controller.Controller, error) {
	cl := mgr.GetClient()
	recorder := mgr.GetEventRecorder(CephClusterConnectionCtrlName)

	ctx := context.Background()
	// Use APIReader (direct client) for Get operations since cache may not be started yet
	// Use regular client for Create operations (writes bypass cache)
	apiReader := mgr.GetAPIReader()
	if err := EnsureConfigMapExists(ctx, apiReader, cl, cfg.ControllerNamespace, log); err != nil {
		log.Error("unable to ensure ConfigMap exists", logger.Err(err))
		return nil, err
	}
	log.Info("successfully ensured ConfigMap exists")

	reconcileLog := log.Named("reconcile")

	c, err := controller.New(CephClusterConnectionCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log := reconcileLog

			log.Info(fmt.Sprintf("starts Reconcile for the CephClusterConnection %q", request.Name))
			cephClusterConnection := &v1alpha1.CephClusterConnection{}
			err := cl.Get(ctx, request.NamespacedName, cephClusterConnection)
			if err != nil && !k8serr.IsNotFound(err) {
				log.Error(fmt.Sprintf("unable to get CephClusterConnection, name: %s", request.Name), logger.Err(err))
				return reconcile.Result{}, err
			}

			if cephClusterConnection.Name == "" {
				log.Info(fmt.Sprintf("seems like the CephClusterConnection for the request %s was deleted. Reconcile retrying will stop.", request.Name))
				return reconcile.Result{}, nil
			}

			shouldRequeue, msg, conflicts, err := reconcileCephClusterConnection(ctx, cl, apiReader, log, cephClusterConnection, cfg.ControllerNamespace)
			// Waiting for volumes and snapshots is how every deletion of a
			// connection in use goes, and it is retried every few seconds until
			// they are gone.
			var inUse *secretInUseError
			waitsForVolumes := errors.As(err, &inUse)
			switch {
			case waitsForVolumes:
				log.Info(fmt.Sprintf("the CephClusterConnection %s waits for the objects that need its secret: %s", cephClusterConnection.Name, err))
			case err != nil:
				log.Error(fmt.Sprintf("an error occurred while reconciles the CephClusterConnection, name: %s", cephClusterConnection.Name), logger.Err(err))
			default:
				log.Info(fmt.Sprintf("CephClusterConnection %s has been reconciled with message: %s", cephClusterConnection.Name, msg))
			}

			// A deletion pass that failed still writes: the finalizer is then
			// held, the object is alive, and its status is the only in-cluster
			// signal of whether the secret or the CSI ConfigMap entry is
			// blocking the teardown. Only the object itself disappearing under
			// the write is tolerated.
			if shouldPublishStatus(cephClusterConnection, err, shouldRequeue) {
				log.Debug(fmt.Sprintf("update the CephClusterConnection %s status with message: %s", cephClusterConnection.Name, msg))
				var extra []metav1.Condition
				if message, ok := conflicts[cephClusterConnection.Name]; ok {
					extra = append(extra, clusterConfigConflictCondition(cephClusterConnection.Generation, message))
				}
				upErr := updateCephClusterConnectionStatus(ctx, cl, recorder, cephClusterConnection, err, msg, extra...)
				switch {
				case k8serr.IsNotFound(upErr):
					log.Debug(fmt.Sprintf("the CephClusterConnection %s is gone, its status was not written", cephClusterConnection.Name))
				case upErr != nil:
					log.Error(fmt.Sprintf("unable to update the CephClusterConnection %s", cephClusterConnection.Name), logger.Err(upErr))
					shouldRequeue = true
				}
			}

			// The other connections of the clusterID get their verdict from this
			// pass too, as the entry they share was just decided for all of them.
			for name, message := range conflicts {
				if name == cephClusterConnection.Name {
					continue
				}
				if cErr := publishClusterConfigConflict(ctx, cl, name, message); cErr != nil && !k8serr.IsNotFound(cErr) {
					log.Error(fmt.Sprintf("unable to update the ClusterConfigConflict condition of the CephClusterConnection %s", name), logger.Err(cErr))
					shouldRequeue = true
				}
			}

			if shouldRequeue {
				if waitsForVolumes {
					log.Debug(fmt.Sprintf("reconciler will requeue the request, name: %s", request.Name))
				} else {
					log.Warn(fmt.Sprintf("reconciler will requeue the request, name: %s", request.Name))
				}
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			log.Info(fmt.Sprintf("ends Reconcile for the CephClusterConnection %q", request.Name))
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error("unable to create controller", logger.Err(err))
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.CephClusterConnection{}, handler.TypedFuncs[*v1alpha1.CephClusterConnection, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*v1alpha1.CephClusterConnection], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Named("create").Info(fmt.Sprintf("get event for CephClusterConnection %q. Add to the queue", e.Object.GetName()))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
			q.Add(request)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*v1alpha1.CephClusterConnection], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log := log.Named("update")

			log.Info(fmt.Sprintf("get event for CephClusterConnection %q. Check if it should be reconciled", e.ObjectNew.GetName()))

			oldCephClusterConnection := e.ObjectOld
			newCephClusterConnection := e.ObjectNew

			log.Trace(fmt.Sprintf("oldCephClusterConnection: %+v", oldCephClusterConnection))
			log.Trace(fmt.Sprintf("newCephClusterConnection: %+v", newCephClusterConnection))

			if reflect.DeepEqual(oldCephClusterConnection.Spec, newCephClusterConnection.Spec) && newCephClusterConnection.DeletionTimestamp == nil {
				log.Info(fmt.Sprintf("an update event for the CephClusterConnection %s has no Spec field updates. It will not be reconciled", newCephClusterConnection.Name))
				return
			}

			log.Info(fmt.Sprintf("the CephClusterConnection %q will be reconciled. Add to the queue", newCephClusterConnection.Name))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newCephClusterConnection.Namespace, Name: newCephClusterConnection.Name}}
			q.Add(request)
		},
	},
	),
	)

	if err != nil {
		log.Error("unable to watch the events", logger.Err(err))
		return nil, err
	}

	// The CephStorageClasses decide whose secret the shared config.json entry
	// of a clusterID carries, so their connections are reconciled again when
	// they appear or go away. Their connection and type are immutable.
	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.CephStorageClass{}, cephStorageClassConnectionHandler()))
	if err != nil {
		log.Error("unable to watch the CephStorageClass events", logger.Err(err))
		return nil, err
	}

	// A volume that changes phase or goes away can release a connection
	// being deleted and resolve its ClusterConfigConflict; a successful pass
	// is not requeued, so nothing else would bring its connection back.
	err = c.Watch(source.Kind(mgr.GetCache(), &corev1.PersistentVolume{}, persistentVolumeConnectionHandler(cfg.ControllerNamespace)))
	if err != nil {
		log.Error("unable to watch the PersistentVolume events", logger.Err(err))
		return nil, err
	}

	return c, nil
}

func persistentVolumeConnectionHandler(controllerNamespace string) handler.TypedEventHandler[*corev1.PersistentVolume, reconcile.Request] {
	enqueueConnectionsOf := func(pv *corev1.PersistentVolume, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		for _, name := range connectionsOfPersistentVolume(pv, controllerNamespace) {
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: name}})
		}
	}

	return handler.TypedFuncs[*corev1.PersistentVolume, reconcile.Request]{
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*corev1.PersistentVolume], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.ObjectOld.Status.Phase != e.ObjectNew.Status.Phase {
				enqueueConnectionsOf(e.ObjectNew, q)
			}
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[*corev1.PersistentVolume], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueueConnectionsOf(e.Object, q)
		},
	}
}

// connectionsOfPersistentVolume returns the sorted names of the
// CephClusterConnections whose secret pv names, in a CSI secret reference or
// in the deletion secret annotations of the external-provisioner.
func connectionsOfPersistentVolume(pv *corev1.PersistentVolume, controllerNamespace string) []string {
	refs := []*corev1.SecretReference{{
		Name:      pv.Annotations[provisionerDeletionSecretNameAnnotation],
		Namespace: pv.Annotations[provisionerDeletionSecretNamespaceAnnotation],
	}}
	if csi := pv.Spec.CSI; csi != nil {
		refs = append(refs, csi.ControllerPublishSecretRef, csi.NodeStageSecretRef, csi.NodePublishSecretRef, csi.ControllerExpandSecretRef, csi.NodeExpandSecretRef)
	}

	var names []string
	for _, ref := range refs {
		if ref == nil || ref.Namespace != controllerNamespace {
			continue
		}
		if name, ok := strings.CutPrefix(ref.Name, internal.CephClusterConnectionSecretPrefix); ok && name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func cephStorageClassConnectionHandler() handler.TypedEventHandler[*v1alpha1.CephStorageClass, reconcile.Request] {
	enqueueConnectionOf := func(cephSC *v1alpha1.CephStorageClass, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		if cephSC.Spec.ClusterConnectionName == "" {
			return
		}
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Name: cephSC.Spec.ClusterConnectionName}})
	}

	return handler.TypedFuncs[*v1alpha1.CephStorageClass, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*v1alpha1.CephStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueueConnectionOf(e.Object, q)
		},
		DeleteFunc: func(_ context.Context, e event.TypedDeleteEvent[*v1alpha1.CephStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			enqueueConnectionOf(e.Object, q)
		},
	}
}

func RunCephClusterConnectionEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) (shouldRequeue bool, msg string, err error) {
	shouldRequeue, msg, _, err = reconcileCephClusterConnection(ctx, cl, cl, log, cephClusterConnection, controllerNamespace)
	return shouldRequeue, msg, err
}

// reconcileCephClusterConnection also returns, once the config.json entry of
// the clusterID is in place, the ClusterConfigConflict messages of every
// connection the entry was built from, keyed by name.
//
// When the cache shows no volume or snapshot holding the secret of a
// connection being deleted, it reads the PersistentVolumes, VolumeAttachments
// and VolumeSnapshotContents again through liveReader: the pass may delete
// the secret, and a volume or snapshot the cache has not seen yet would be
// left with a secret that no longer exists.
func reconcileCephClusterConnection(ctx context.Context, cl client.Client, liveReader client.Reader, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) (shouldRequeue bool, msg string, conflicts map[string]string, err error) {
	valid, msg := validateCephClusterConnectionSpec(cephClusterConnection)
	if !valid {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] CephClusterConnection %s has invalid spec: %s", cephClusterConnection.Name, msg)
		return false, msg, conflicts, err
	}
	log.Debug(fmt.Sprintf("CephClusterConnection %s has valid spec", cephClusterConnection.Name))

	added, err := addFinalizerIfNotExists(ctx, cl, cephClusterConnection, CephClusterConnectionControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to add a finalizer %s to the CephClusterConnection %s: %w", CephClusterConnectionControllerFinalizerName, cephClusterConnection.Name, err)
		return true, err.Error(), conflicts, err
	}
	log.Debug(fmt.Sprintf("finalizer %s was added to the CephClusterConnection %s: %t", CephClusterConnectionControllerFinalizerName, cephClusterConnection.Name, added))

	configMapList := &corev1.ConfigMapList{}
	err = cl.List(ctx, configMapList, client.InNamespace(controllerNamespace))
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to list ConfigMaps in namespace %s: %w", controllerNamespace, err)
		return true, err.Error(), conflicts, err
	}

	// One snapshot of the volumes for the whole pass: the config.json entry
	// and the secret of a connection being deleted must agree on whether a
	// volume still needs that secret, or the entry may keep naming a secret
	// this pass has just deleted.
	usage, err := listVolumeUsage(ctx, cl)
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] %w", err)
		return true, err.Error(), conflicts, err
	}
	// Only a pass that would delete the secret needs to see past the cache;
	// one the cache already holds back waits for the volumes anyway.
	secretName := internal.CephClusterConnectionSecretPrefix + cephClusterConnection.Name
	if cephClusterConnection.DeletionTimestamp != nil && !usage.secretNeeded(controllerNamespace, secretName) {
		usage, err = listVolumeUsage(ctx, liveReader)
		if err != nil {
			err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] %w", err)
			return true, err.Error(), conflicts, err
		}
	}

	current := currentClusterConfig(configMapList, internal.CSICephConfigMapName, cephClusterConnection.Spec.ClusterID)
	desiredClusterConfig, entryConflicts, err := getDesiredClusterConfig(ctx, cl, log, cephClusterConnection, current, usage, controllerNamespace)
	if err != nil {
		return true, err.Error(), conflicts, err
	}

	shouldRequeue, msg, err = reconcileConfigMap(ctx, cl, log, configMapList, cephClusterConnection, desiredClusterConfig, controllerNamespace, internal.CSICephConfigMapName)
	if err != nil || shouldRequeue {
		return shouldRequeue, msg, conflicts, err
	}
	conflicts = entryConflicts

	secretList := &corev1.SecretList{}
	err = cl.List(ctx, secretList, client.InNamespace(controllerNamespace))
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to list Secrets in namespace %s: %w", controllerNamespace, err)
		return true, err.Error(), conflicts, err
	}

	shouldRequeue, msg, err = reconcileSecret(ctx, cl, log, secretList, cephClusterConnection, usage, controllerNamespace, secretName)
	if err != nil || shouldRequeue {
		return shouldRequeue, msg, conflicts, err
	}

	if cephClusterConnection.DeletionTimestamp != nil {
		err = removeFinalizerIfExists(ctx, cl, cephClusterConnection, CephClusterConnectionControllerFinalizerName)
		if err != nil {
			err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to remove finalizer from the CephClusterConnection %s: %w", cephClusterConnection.Name, err)
			return true, err.Error(), conflicts, err
		}
	}

	log.Debug(fmt.Sprintf("finish all reconciliations for CephClusterConnection %q.", cephClusterConnection.Name))
	return false, msg, conflicts, nil
}
