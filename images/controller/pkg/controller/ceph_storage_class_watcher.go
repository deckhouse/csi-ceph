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
	"fmt"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	v1 "k8s.io/api/storage/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
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
	CephStorageClassCtrlName = "d8-ceph-storage-class-controller"

	StorageClassKind       = "StorageClass"
	StorageClassAPIVersion = "storage.k8s.io/v1"

	CephStorageClassRBDProvisioner    = "rbd.csi.ceph.com"
	CephStorageClassCephFSProvisioner = "cephfs.csi.ceph.com"

	CephStorageClassControllerFinalizerName = "storage.deckhouse.io/ceph-storage-class-controller"
	CephStorageClassManagedLabelKey         = "storage.deckhouse.io/managed-by"
	CephStorageClassManagedLabelValue       = "ceph-storage-class-controller"
)

var (
	allowedProvisioners = []string{CephStorageClassRBDProvisioner, CephStorageClassCephFSProvisioner}
)

func RunCephStorageClassWatcherController(
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
) (controller.Controller, error) {
	cl := mgr.GetClient()
	mapper := mgr.GetRESTMapper()

	reconcileLog := log.Named("reconcile")

	c, err := controller.New(CephStorageClassCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log := reconcileLog

			log.Info(fmt.Sprintf("starts Reconcile for the CephStorageClass %q", request.Name))
			cephSC := &v1alpha1.CephStorageClass{}
			err := cl.Get(ctx, request.NamespacedName, cephSC)
			if err != nil && !k8serr.IsNotFound(err) {
				log.Error(fmt.Sprintf("unable to get CephStorageClass, name: %s", request.Name), logger.Err(err))
				return reconcile.Result{}, err
			}

			if cephSC.Name == "" {
				log.Info(fmt.Sprintf("seems like the CephStorageClass for the request %s was deleted. Reconcile retrying will stop.", request.Name))
				return reconcile.Result{}, nil
			}

			scList := &v1.StorageClassList{}
			err = cl.List(ctx, scList)
			if err != nil {
				log.Error("unable to list Storage Classes", logger.Err(err))
				return reconcile.Result{}, err
			}

			// A nil list tells RunStorageClassEventReconcile that the VolumeSnapshotClass
			// CRD is absent and the whole snapshot leg has to be skipped.
			var vsClassList *snapshotv1.VolumeSnapshotClassList
			if VolumeSnapshotClassCRDExists(mapper, log) {
				vsClassList = &snapshotv1.VolumeSnapshotClassList{}
				err := cl.List(ctx, vsClassList)
				if err != nil {
					log.Error("unable to list VolumeSnapshot Classes", logger.Err(err))
					return reconcile.Result{}, err
				}
			} else {
				log.Info("the VolumeSnapshotClass CRD is not registered in the cluster, skipping VolumeSnapshotClass reconciliation")
			}

			shouldRequeue, msg, err := RunStorageClassEventReconcile(ctx, cl, log, scList, vsClassList, cephSC, cfg.ControllerNamespace, cfg.StorageClassLabelIgnoredPrefixes)
			log.Info(fmt.Sprintf("CephStorageClass %s has been reconciled with message: %s", cephSC.Name, msg))
			if err != nil {
				log.Error(fmt.Sprintf("an error occurred while reconciles the CephStorageClass, name: %s", cephSC.Name), logger.Err(err))
			}

			// The status is written on every pass over a live object, not only
			// when the pass produced a message: a successful reconcile that had
			// nothing to say still has to publish Ready=True, or the condition
			// stays absent and is indistinguishable from never evaluated.
			//
			// A deletion pass that failed still writes: the finalizer is then
			// held, the object is alive, and its status is the only in-cluster
			// signal of what is blocking the teardown. Only the object itself
			// disappearing under the write is tolerated.
			if shouldPublishStatus(cephSC, err, shouldRequeue) {
				log.Debug(fmt.Sprintf("update the CephStorageClass %s status with message: %s", cephSC.Name, msg))
				upErr := updateCephStorageClassStatus(ctx, cl, cephSC, err, msg)
				switch {
				case k8serr.IsNotFound(upErr):
					log.Debug(fmt.Sprintf("the CephStorageClass %s is gone, its status was not written", cephSC.Name))
				case upErr != nil:
					log.Error(fmt.Sprintf("unable to update the CephStorageClass %s", cephSC.Name), logger.Err(upErr))
					shouldRequeue = true
				}
			}

			if shouldRequeue {
				log.Warn(fmt.Sprintf("reconciler will requeue the request, name: %s", request.Name))
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			log.Info(fmt.Sprintf("ends Reconcile for the CephStorageClass %q", request.Name))
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error("unable to create controller", logger.Err(err))
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.CephStorageClass{}, handler.TypedFuncs[*v1alpha1.CephStorageClass, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*v1alpha1.CephStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Named("create").Info(fmt.Sprintf("get event for CephStorageClass %q. Add to the queue", e.Object.GetName()))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
			q.Add(request)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*v1alpha1.CephStorageClass], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log := log.Named("update")

			log.Info(fmt.Sprintf("get event for CephStorageClass %q. Check if it should be reconciled", e.ObjectNew.GetName()))

			oldCephSC := e.ObjectOld
			newCephSC := e.ObjectNew

			if !shouldEnqueueCephStorageClassUpdate(oldCephSC, newCephSC) {
				log.Info(fmt.Sprintf("an update event for the CephStorageClass %s changes nothing the StorageClass is built from. It will not be reconciled", newCephSC.Name))
				return
			}

			log.Info(fmt.Sprintf("the CephStorageClass %q will be reconciled. Add to the queue", newCephSC.Name))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newCephSC.Namespace, Name: newCephSC.Name}}
			q.Add(request)
		},
	},
	),
	)
	if err != nil {
		log.Error("unable to watch the events", logger.Err(err))
		return nil, err
	}

	return c, nil
}

func RunStorageClassEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, vsClassList *snapshotv1.VolumeSnapshotClassList, cephSC *v1alpha1.CephStorageClass, controllerNamespace string, ignoredLabelPrefixes []string) (bool, string, error) {

	log.Debug(fmt.Sprintf("starts reconciliataion of CephStorageClass, name: %s", cephSC.Name))

	if cephSC.DeletionTimestamp != nil {
		log.Debug(fmt.Sprintf("CephStorageClass %s is being deleted", cephSC.Name))
		shouldRequeue, msg, err := reconcileStorageClassDeleteFunc(ctx, cl, log, scList, cephSC)
		log.Debug(fmt.Sprintf("ends reconciliataion of StorageClass, name: %s, shouldRequeue: %t, err: %v", cephSC.Name, shouldRequeue, err))
		if err != nil || shouldRequeue {
			return shouldRequeue, msg, err
		}

		if vsClassList != nil {
			oldVSClass := findVSClass(vsClassList, cephSC.Name)
			shouldRequeue, err = reconcileVolumeSnapshotClassDeleteFunc(ctx, cl, log, oldVSClass, cephSC)
			log.Debug(fmt.Sprintf("ends reconciliataion of VolumeSnapshotClass, name: %s, shouldRequeue: %t, err: %v", cephSC.Name, shouldRequeue, err))
			if err != nil || shouldRequeue {
				return shouldRequeue, "", err
			}
		}

		log.Debug(fmt.Sprintf("removing finalizer %s from the CephStorageClass %s", CephStorageClassControllerFinalizerName, cephSC.Name))
		err = removeFinalizerIfExists(ctx, cl, cephSC, CephStorageClassControllerFinalizerName)
		if err != nil {
			err = fmt.Errorf("[RunStorageClassEventReconcile] unable to remove a finalizer %s from the CephStorageClass %s: %w", CephStorageClassControllerFinalizerName, cephSC.Name, err)
			return true, err.Error(), err
		}

		return shouldRequeue, msg, err
	}

	valid, msg := validateCephStorageClassSpec(cephSC)
	if !valid {
		err := fmt.Errorf("[RunStorageClassEventReconcile] CephStorageClass %s has invalid spec: %s", cephSC.Name, msg)
		return false, msg, err
	}
	log.Debug(fmt.Sprintf("CephStorageClass %s has valid spec", cephSC.Name))

	added, err := addFinalizerIfNotExists(ctx, cl, cephSC, CephStorageClassControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] unable to add a finalizer %s to the CephStorageClass %s: %w", CephStorageClassControllerFinalizerName, cephSC.Name, err)
		return true, err.Error(), err
	}
	log.Debug(fmt.Sprintf("finalizer %s was added to the CephStorageClass %s: %t", CephStorageClassControllerFinalizerName, cephSC.Name, added))

	clusterID, err := getClusterID(ctx, cl, cephSC)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] unable to get clusterID for CephStorageClass %s: %w", cephSC.Name, err)
		return true, err.Error(), err
	}

	reconcileTypeForStorageClass, err := IdentifyReconcileFuncForStorageClass(log, scList, cephSC, controllerNamespace, clusterID, ignoredLabelPrefixes)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] error occurred while identifying the reconcile function for StorageClass %s: %w", cephSC.Name, err)
		return true, err.Error(), err
	}

	shouldRequeue := false
	log.Debug(fmt.Sprintf("successfully identified the reconcile type for StorageClass %s: %s", cephSC.Name, reconcileTypeForStorageClass))
	switch reconcileTypeForStorageClass {
	case internal.CreateReconcile:
		shouldRequeue, msg, err = reconcileStorageClassCreateFunc(ctx, cl, log, scList, cephSC, controllerNamespace, clusterID, ignoredLabelPrefixes)
	case internal.UpdateReconcile:
		shouldRequeue, msg, err = reconcileStorageClassUpdateFunc(ctx, cl, log, scList, cephSC, controllerNamespace, clusterID, ignoredLabelPrefixes)
	default:
		log.Debug(fmt.Sprintf("StorageClass for CephStorageClass %s should not be reconciled", cephSC.Name))
		msg = "Successfully reconciled"
	}
	log.Debug(fmt.Sprintf("ends reconciliataion of StorageClass, name: %s, shouldRequeue: %t, err: %v", cephSC.Name, shouldRequeue, err))

	if err != nil || shouldRequeue {
		return shouldRequeue, msg, err
	}

	if vsClassList == nil {
		log.Debug(fmt.Sprintf("the VolumeSnapshotClass CRD is absent, skipping the VolumeSnapshotClass for the CephStorageClass %q", cephSC.Name))
	} else {
		reconcileTypeForVSClass, oldVSClass, newVSClass := IdentifyReconcileFuncForVSClass(log, vsClassList, cephSC, clusterID, controllerNamespace)

		log.Debug(fmt.Sprintf("reconcile operation for VolumeSnapshotClass %q: %q", cephSC.Name, reconcileTypeForVSClass))
		switch reconcileTypeForVSClass {
		case internal.CreateReconcile:
			shouldRequeue, err = reconcileVolumeSnapshotClassCreateFunc(ctx, cl, log, newVSClass)
		case internal.UpdateReconcile:
			shouldRequeue, err = reconcileVolumeSnapshotClassUpdateFunc(ctx, cl, log, oldVSClass, newVSClass)
		default:
			log.Debug(fmt.Sprintf("VolumeSnapshotClass %q should not be reconciled", cephSC.Name))
		}

		log.Debug(fmt.Sprintf("ends reconciliataion of VolumeSnapshotClass, name: %s, shouldRequeue: %t, err: %v", cephSC.Name, shouldRequeue, err))
		if err != nil || shouldRequeue {
			return shouldRequeue, msg, err
		}
	}

	log.Debug(fmt.Sprintf("finish all reconciliations for CephStorageClass %q.", cephSC.Name))
	return false, msg, nil
}
