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

	corev1 "k8s.io/api/core/v1"
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
	CephClusterConnectionCtrlName                = "ceph-cluster-controller"
	CephClusterConnectionControllerFinalizerName = "storage.deckhouse.io/ceph-cluster-controller"
	StorageManagedLabelKey                       = "storage.deckhouse.io/managed-by"

	SecretForCephClusterConnectionPrefix = "csi-ceph-secret-for-"
)

func RunCephClusterConnectionWatcherController(
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
) (controller.Controller, error) {
	cl := mgr.GetClient()

	c, err := controller.New(CephClusterConnectionCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log.Info(fmt.Sprintf("[CephClusterConnectionReconciler] starts Reconcile for the CephClusterConnection %q", request.Name))
			cephClusterConnection := &v1alpha1.CephClusterConnection{}
			err := cl.Get(ctx, request.NamespacedName, cephClusterConnection)
			if err != nil && !k8serr.IsNotFound(err) {
				log.Error(err, fmt.Sprintf("[CephClusterConnectionReconciler] unable to get CephClusterConnection, name: %s", request.Name))
				return reconcile.Result{}, err
			}

			if cephClusterConnection.Name == "" {
				log.Info(fmt.Sprintf("[CephClusterConnectionReconciler] seems like the CephClusterConnection for the request %s was deleted. Reconcile retrying will stop.", request.Name))
				return reconcile.Result{}, nil
			}

			secretList := &corev1.SecretList{}
			err = cl.List(ctx, secretList, client.InNamespace(cfg.ControllerNamespace))
			if err != nil {
				log.Error(err, "[CephClusterConnectionReconciler] unable to list Secrets")
				return reconcile.Result{}, err
			}

			shouldRequeue, err := RunCephClusterConnectionEventReconcile(ctx, cl, log, secretList, cephClusterConnection, cfg.ControllerNamespace)
			if err != nil {
				log.Error(err, fmt.Sprintf("[CephClusterConnectionReconciler] an error occured while reconciles the CephClusterConnection, name: %s", cephClusterConnection.Name))
			}

			if shouldRequeue {
				log.Warning(fmt.Sprintf("[CephClusterConnectionReconciler] Reconciler will requeue the request, name: %s", request.Name))
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			log.Info(fmt.Sprintf("[CephClusterConnectionReconciler] ends Reconcile for the CephClusterConnection %q", request.Name))
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunCephClusterConnectionWatcherController] unable to create controller")
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.CephClusterConnection{}), handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[CreateFunc] get event for CephClusterConnection %q. Add to the queue", e.Object.GetName()))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
			q.Add(request)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			log.Info(fmt.Sprintf("[UpdateFunc] get event for CephClusterConnection %q. Check if it should be reconciled", e.ObjectNew.GetName()))

			oldCephClusterConnection, ok := e.ObjectOld.(*v1alpha1.CephClusterConnection)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[UpdateFunc] an error occurred while handling create event")
				return
			}
			newCephClusterConnection, ok := e.ObjectNew.(*v1alpha1.CephClusterConnection)
			if !ok {
				err = errors.New("unable to cast event object to a given type")
				log.Error(err, "[UpdateFunc] an error occurred while handling create event")
				return
			}

			if reflect.DeepEqual(oldCephClusterConnection.Spec, newCephClusterConnection.Spec) && newCephClusterConnection.DeletionTimestamp == nil {
				log.Info(fmt.Sprintf("[UpdateFunc] an update event for the CephClusterConnection %s has no Spec field updates. It will not be reconciled", newCephClusterConnection.Name))
				return
			}

			log.Info(fmt.Sprintf("[UpdateFunc] the CephClusterConnection %q will be reconciled. Add to the queue", newCephClusterConnection.Name))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newCephClusterConnection.Namespace, Name: newCephClusterConnection.Name}}
			q.Add(request)
		},
	})
	if err != nil {
		log.Error(err, "[RunCephClusterConnectionWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}

func RunCephClusterConnectionEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) (shouldRequeue bool, err error) {
	err = validateCephClusterConnectionSpec(cephClusterConnection)
	if err != nil {
		log.Error(err, fmt.Sprintf("[RunCephClusterConnectionEventReconcile] an error occured while validating the CephClusterConnection %q", cephClusterConnection.Name))
		upError := updateCephClusterConnectionPhase(ctx, cl, cephClusterConnection, v1alpha1.PhaseFailed, err.Error())
		if upError != nil {
			upError = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to update the CephClusterConnection %s: %w", cephClusterConnection.Name, upError)
			err = errors.Join(err, upError)
		}
		return false, err
	}

	added, err := addFinalizerIfNotExists(ctx, cl, cephClusterConnection, CephClusterConnectionControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to add a finalizer %s to the CephClusterConnection %s: %w", CephClusterConnectionControllerFinalizerName, cephClusterConnection.Name, err)
		return true, err
	}
	log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] finalizer %s was added to the CephClusterConnection %s: %t", CephClusterConnectionControllerFinalizerName, cephClusterConnection.Name, added))

	secretName := SecretForCephClusterConnectionPrefix + cephClusterConnection.Name
	reconcileTypeForSecret, err := IdentifyReconcileFuncForSecret(log, secretList, cephClusterConnection, controllerNamespace, secretName)
	if err != nil {
		log.Error(err, fmt.Sprintf("[RunCephClusterConnectionEventReconcile] error occured while identifying the reconcile function for the Secret %q", SecretForCephClusterConnectionPrefix+cephClusterConnection.Name))
		return true, err
	}

	shouldRequeue = false
	log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] reconcile operation of CephClusterConnection %s for Secret %s: %s", cephClusterConnection.Name, secretName, reconcileTypeForSecret))
	switch reconcileTypeForSecret {
	case CreateReconcile:
		shouldRequeue, err = reconcileSecretCreateFunc(ctx, cl, log, cephClusterConnection, controllerNamespace, secretName)
	case UpdateReconcile:
		shouldRequeue, err = reconcileSecretUpdateFunc(ctx, cl, log, secretList, cephClusterConnection, controllerNamespace, secretName)
	case DeleteReconcile:
		log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] DeleteReconcile: starts reconciliataion of CephClusterConnection %s for Secret %s", cephClusterConnection.Name, secretName))
		shouldRequeue, err = reconcileSecretDeleteFunc(ctx, cl, log, secretList, cephClusterConnection, secretName)
	default:
		log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] StorageClass for CephClusterConnection %s should not be reconciled", cephClusterConnection.Name))
	}
	log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] ends reconciliataion of StorageClass, name: %s, shouldRequeue: %t, err: %v", cephClusterConnection.Name, shouldRequeue, err))

	if err != nil || shouldRequeue {
		return shouldRequeue, err
	}

	log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] Finish all reconciliations for CephClusterConnection %q.", cephClusterConnection.Name))
	return false, nil

}
