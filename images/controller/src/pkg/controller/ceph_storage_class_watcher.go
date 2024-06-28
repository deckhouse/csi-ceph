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
	"d8-controller/pkg/internal"
	"d8-controller/pkg/logger"
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

			shouldRequeue, msg, err := RunStorageClassEventReconcile(ctx, cl, log, scList, cephSC, cfg.ControllerNamespace)
			log.Info(fmt.Sprintf("[CephStorageClassReconciler] CephStorageClass %s has been reconciled with message: %s", cephSC.Name, msg))
			phase := v1alpha1.PhaseCreated
			if err != nil {
				log.Error(err, fmt.Sprintf("[CephStorageClassReconciler] an error occured while reconciles the CephStorageClass, name: %s", cephSC.Name))
				phase = v1alpha1.PhaseFailed
			}

			if msg != "" {
				log.Debug(fmt.Sprintf("[CephStorageClassReconciler] Update the CephStorageClass %s with %s status phase and message: %s", cephSC.Name, phase, msg))
				upErr := updateCephStorageClassPhase(ctx, cl, cephSC, phase, msg)
				if upErr != nil {
					log.Error(upErr, fmt.Sprintf("[CephStorageClassReconciler] unable to update the CephStorageClass %s: %s", cephSC.Name, upErr.Error()))
					shouldRequeue = true
				}
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

	err = c.Watch(
		source.Kind(mgr.GetCache(), &v1alpha1.CephStorageClass{},
			handler.TypedFuncs[*v1alpha1.CephStorageClass]{
				CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[*v1alpha1.CephStorageClass], q workqueue.RateLimitingInterface) {
					log.Info(fmt.Sprintf("[CreateFunc] get event for CephStorageClass %q. Add to the queue", e.Object.GetName()))
					request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
					q.Add(request)
				},
				UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[*v1alpha1.CephStorageClass], q workqueue.RateLimitingInterface) {
					log.Info(fmt.Sprintf("[UpdateFunc] get event for CephStorageClass %q. Check if it should be reconciled", e.ObjectNew.GetName()))

					oldCephSC := e.ObjectOld
					newCephSC := e.ObjectNew

					if reflect.DeepEqual(oldCephSC.Spec, newCephSC.Spec) && newCephSC.DeletionTimestamp == nil {
						log.Info(fmt.Sprintf("[UpdateFunc] an update event for the CephStorageClass %s has no Spec field updates. It will not be reconciled", newCephSC.Name))
						return
					}

					log.Info(fmt.Sprintf("[UpdateFunc] the CephStorageClass %q will be reconciled. Add to the queue", newCephSC.Name))
					request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newCephSC.Namespace, Name: newCephSC.Name}}
					q.Add(request)
				},
			},
		),
	)
	if err != nil {
		log.Error(err, "[RunCephStorageClassWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}

func RunStorageClassEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, scList *v1.StorageClassList, cephSC *v1alpha1.CephStorageClass, controllerNamespace string) (shouldRequeue bool, msg string, err error) {
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] starts reconciliataion of CephStorageClass, name: %s", cephSC.Name))

	if cephSC.DeletionTimestamp != nil {
		log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] CephStorageClass %s is being deleted", cephSC.Name))
		shouldRequeue, msg, err = reconcileStorageClassDeleteFunc(ctx, cl, log, scList, cephSC)
		log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] ends reconciliataion of StorageClass, name: %s, shouldRequeue: %t, err: %v", cephSC.Name, shouldRequeue, err))
		return shouldRequeue, msg, err
	}

	valid, msg := validateCephStorageClassSpec(cephSC)
	if !valid {
		err = fmt.Errorf("[RunStorageClassEventReconcile] CephStorageClass %s has invalid spec: %s", cephSC.Name, msg)
		return false, msg, err
	}
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] CephStorageClass %s has valid spec", cephSC.Name))

	added, err := addFinalizerIfNotExists(ctx, cl, cephSC, CephStorageClassControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] unable to add a finalizer %s to the CephStorageClass %s: %w", CephStorageClassControllerFinalizerName, cephSC.Name, err)
		return true, err.Error(), err
	}
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] finalizer %s was added to the CephStorageClass %s: %t", CephStorageClassControllerFinalizerName, cephSC.Name, added))

	clusterID, err := getClusterID(ctx, cl, cephSC)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] unable to get clusterID for CephStorageClass %s: %w", cephSC.Name, err)
		return true, err.Error(), err
	}

	reconcileTypeForStorageClass, err := IdentifyReconcileFuncForStorageClass(log, scList, cephSC, controllerNamespace, clusterID)
	if err != nil {
		err = fmt.Errorf("[RunStorageClassEventReconcile] error occured while identifying the reconcile function for StorageClass %s: %w", cephSC.Name, err)
		return true, err.Error(), err
	}

	shouldRequeue = false
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] Successfully identified the reconcile type for StorageClass %s: %s", cephSC.Name, reconcileTypeForStorageClass))
	switch reconcileTypeForStorageClass {
	case internal.CreateReconcile:
		shouldRequeue, msg, err = reconcileStorageClassCreateFunc(ctx, cl, log, scList, cephSC, controllerNamespace, clusterID)
	case internal.UpdateReconcile:
		shouldRequeue, msg, err = reconcileStorageClassUpdateFunc(ctx, cl, log, scList, cephSC, controllerNamespace, clusterID)
	default:
		log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] StorageClass for CephStorageClass %s should not be reconciled", cephSC.Name))
		msg = "Successfully reconciled"
	}
	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] ends reconciliataion of StorageClass, name: %s, shouldRequeue: %t, err: %v", cephSC.Name, shouldRequeue, err))

	if err != nil || shouldRequeue {
		return shouldRequeue, msg, err
	}

	log.Debug(fmt.Sprintf("[RunStorageClassEventReconcile] Finish all reconciliations for CephStorageClass %q.", cephSC.Name))
	return false, msg, nil
}
