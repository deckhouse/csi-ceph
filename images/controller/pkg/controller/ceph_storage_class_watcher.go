/*
Copyright 2024 Flant JSC

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
	v1alpha1 "d8-controller/api/v1alpha1"
	"d8-controller/pkg/config"
	"d8-controller/pkg/logger"
	"errors"
	"fmt"
	"reflect"
	"time"

	v1 "k8s.io/api/storage/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	CephStorageClassCtrlName = "ceph-storage-class-controller"

	StorageClassKind       = "StorageClass"
	StorageClassAPIVersion = "storage.k8s.io/v1"

	CephStorageClassRBDProvisioner    = "rbd.csi.ceph.com"
	CephStorageClassCephFSProvisioner = "cephfs.csi.ceph.com"

	CephStorageClassControllerFinalizerName = "storage.deckhouse.io/ceph-storage-class-controller"
	CephStorageClassManagedLabelKey         = "storage.deckhouse.io/managed-by"
	CephStorageClassManagedLabelValue       = "ceph-storage-class-controller"

	FailedStatusPhase  = "Failed"
	CreatedStatusPhase = "Created"

	CreateReconcile = "Create"
	UpdateReconcile = "Update"
	DeleteReconcile = "Delete"
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

	c, err := controller.New(CephStorageClassCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log.Info(fmt.Sprintf("[CephStorageClassReconciler] starts Reconcile for the CephStorageClass %q", request.Name))
			cephSC := &v1alpha1.CephStorageClass{}
			err := cl.Get(ctx, request.NamespacedName, cephSC)
			if err != nil && !k8serr.IsNotFound(err) {
				log.Error(err, fmt.Sprintf("[CephStorageClassReconciler] unable to get CephStorageClass, name: %s", request.Name))
				return reconcile.Result{}, err
			}

			if cephSC.Name == "" {
				log.Info(fmt.Sprintf("[CephStorageClassReconciler] seems like the CephStorageClass for the request %s was deleted. Reconcile retrying will stop.", request.Name))
				return reconcile.Result{}, nil
			}

			scList := &v1.StorageClassList{}
			err = cl.List(ctx, scList)
			if err != nil {
				log.Error(err, "[CephStorageClassReconciler] unable to list Storage Classes")
				return reconcile.Result{}, err
			}

			shouldRequeue, err := RunStorageClassEventReconcile(ctx, cl, log, scList, cephSC, cfg.ControllerNamespace)
			if err != nil {
				log.Error(err, fmt.Sprintf("[CephStorageClassReconciler] an error occured while reconciles the CephStorageClass, name: %s", cephSC.Name))
			}

			if shouldRequeue {
				log.Warning(fmt.Sprintf("[CephStorageClassReconciler] Reconciler will requeue the request, name: %s", request.Name))
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			log.Info(fmt.Sprintf("[CephStorageClassReconciler] ends Reconcile for the CephStorageClass %q", request.Name))
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunCephStorageClassWatcherController] unable to create controller")
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.CephStorageClass{}), handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[CreateFunc] get event for CephStorageClass %q. Add to the queue", e.Object.GetName()))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
			q.Add(request)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[UpdateFunc] get event for CephStorageClass %q. Check if it should be reconciled", e.ObjectNew.GetName()))

			oldCephSC, ok := e.ObjectOld.(*v1alpha1.CephStorageClass)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[UpdateFunc] an error occurred while handling create event")
				return
			}
			newCephSC, ok := e.ObjectNew.(*v1alpha1.CephStorageClass)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[UpdateFunc] an error occurred while handling create event")
				return
			}

			if reflect.DeepEqual(oldCephSC.Spec, newCephSC.Spec) && newCephSC.DeletionTimestamp == nil {
				log.Info(fmt.Sprintf("[UpdateFunc] an update event for the CephStorageClass %s has no Spec field updates. It will not be reconciled", newCephSC.Name))
				return
			}

			log.Info(fmt.Sprintf("[UpdateFunc] the CephStorageClass %q will be reconciled. Add to the queue", newCephSC.Name))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newCephSC.Namespace, Name: newCephSC.Name}}
			q.Add(request)
		},
	})
	if err != nil {
		log.Error(err, "[RunCephStorageClassWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}

func RunStorageClassEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, cephSC *v1alpha1.CephStorageClass, controllerNamespace string) (shouldRequeue bool, err error) {
	added, err := addFinalizerIfNotExists(ctx, cl, cephSC, CephStorageClassControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] unable to add a finalizer %s to the CephStorageClass %s: %w", CephStorageClassControllerFinalizerName, cephSC.Name, err)
		return true, err
	}
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] finalizer %s was added to the CephStorageClass %s: %t", CephStorageClassControllerFinalizerName, cephSC.Name, added))

	valid, msg := validateCephStorageClassSpec(cephSC)
	if !valid {
		err = fmt.Errorf("[RunStorageClassEventReconcile] CephStorageClass %s has invalid spec: %s", cephSC.Name, msg)
		upErr := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, msg)
		if upErr != nil {
			upErr = fmt.Errorf("[RunStorageClassEventReconcile] unable to update the CephStorageClass %s: %w", cephSC.Name, upErr)
			err = errors.Join(err, upErr)
		}
		return false, err
	}

	clusterID, err := getClusterID(ctx, cl, cephSC)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] unable to get clusterID for CephStorageClass %s: %w", cephSC.Name, err)
		upErr := updateCephStorageClassPhase(ctx, cl, cephSC, FailedStatusPhase, err.Error())
		if upErr != nil {
			upErr = fmt.Errorf("[RunStorageClassEventReconcile] unable to update the CephStorageClass %s: %w", cephSC.Name, upErr)
			err = errors.Join(err, upErr)
		}
		return true, err
	}

	reconcileTypeForStorageClass, err := IdentifyReconcileFuncForStorageClass(log, scList, cephSC, controllerNamespace, clusterID)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] error occured while identifying the reconcile function for StorageClass %s: %w", cephSC.Name, err)
		return true, err
	}

	shouldRequeue = false
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] reconcile operation for StorageClass %q: %q", cephSC.Name, reconcileTypeForStorageClass))
	switch reconcileTypeForStorageClass {
	case CreateReconcile:
		log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] CreateReconcile starts reconciliataion of StorageClass, name: %s", cephSC.Name))
		shouldRequeue, err = RunStorageClassEventReconcile(ctx, cl, log, scList, cephSC, controllerNamespace)
	case UpdateReconcile:
		log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] UpdateReconcile starts reconciliataion of StorageClass, name: %s", cephSC.Name))
		shouldRequeue, err = reconcileStorageClassUpdateFunc(ctx, cl, log, scList, cephSC, controllerNamespace, clusterID)
	case DeleteReconcile:
		log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] DeleteReconcile starts reconciliataion of StorageClass, name: %s", cephSC.Name))
		shouldRequeue, err = reconcileStorageClassDeleteFunc(ctx, cl, log, scList, cephSC)
	default:
		log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] StorageClass for CephStorageClass %s should not be reconciled", cephSC.Name))
	}
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] ends reconciliataion of StorageClass, name: %s, shouldRequeue: %t, err: %v", cephSC.Name, shouldRequeue, err))

	if err != nil || shouldRequeue {
		return shouldRequeue, err
	}

	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] Finish all reconciliations for CephStorageClass %q.", cephSC.Name))
	return false, nil

}
