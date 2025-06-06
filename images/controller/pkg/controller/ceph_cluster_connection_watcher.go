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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/config"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/logger"
)

const (
	// This value used as a name for the controller AND the value for managed-by label.
	CephClusterConnectionCtrlName                = "d8-ceph-cluster-connection-controller"
	CephClusterConnectionControllerFinalizerName = "storage.deckhouse.io/ceph-cluster-connection-controller"
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

			shouldRequeue, msg, err := RunCephClusterConnectionEventReconcile(ctx, cl, log, cephClusterConnection, cfg.ControllerNamespace)
			phase := internal.PhaseCreated
			if err != nil {
				log.Error(err, fmt.Sprintf("[CephClusterConnectionReconciler] an error occurred while reconciles the CephClusterConnection, name: %s", cephClusterConnection.Name))
				phase = internal.PhaseFailed
			} else {
				log.Info(fmt.Sprintf("[CephClusterConnectionReconciler] CeohClusterConnection %s has been reconciled with message: %s", cephClusterConnection.Name, msg))
			}

			log.Debug(fmt.Sprintf("[CephClusterConnectionReconciler] update the CephClusterConnection %s with the phase %s and message: %s", cephClusterConnection.Name, phase, msg))
			upErr := updateCephClusterConnectionPhaseIfNeeded(ctx, cl, cephClusterConnection, phase, msg)
			if upErr != nil {
				log.Error(upErr, fmt.Sprintf("[CephClusterConnectionReconciler] unable to update the CephClusterConnection %s: %s", cephClusterConnection.Name, upErr.Error()))
				shouldRequeue = true
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

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.CephClusterConnection{}, handler.TypedFuncs[*v1alpha1.CephClusterConnection, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*v1alpha1.CephClusterConnection], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Info(fmt.Sprintf("[CreateFunc] get event for CephClusterConnection %q. Add to the queue", e.Object.GetName()))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
			q.Add(request)
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*v1alpha1.CephClusterConnection], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Info(fmt.Sprintf("[UpdateFunc] get event for CephClusterConnection %q. Check if it should be reconciled", e.ObjectNew.GetName()))

			oldCephClusterConnection := e.ObjectOld
			newCephClusterConnection := e.ObjectNew

			log.Trace(fmt.Sprintf("[UpdateFunc] oldCephClusterConnection: %+v", oldCephClusterConnection))
			log.Trace(fmt.Sprintf("[UpdateFunc] newCephClusterConnection: %+v", newCephClusterConnection))

			if reflect.DeepEqual(oldCephClusterConnection.Spec, newCephClusterConnection.Spec) && newCephClusterConnection.DeletionTimestamp == nil {
				log.Info(fmt.Sprintf("[UpdateFunc] an update event for the CephClusterConnection %s has no Spec field updates. It will not be reconciled", newCephClusterConnection.Name))
				return
			}

			log.Info(fmt.Sprintf("[UpdateFunc] the CephClusterConnection %q will be reconciled. Add to the queue", newCephClusterConnection.Name))
			request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newCephClusterConnection.Namespace, Name: newCephClusterConnection.Name}}
			q.Add(request)
		},
	},
	),
	)

	if err != nil {
		log.Error(err, "[RunCephClusterConnectionWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}

func RunCephClusterConnectionEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) (shouldRequeue bool, msg string, err error) {
	valid, msg := validateCephClusterConnectionSpec(cephClusterConnection)
	if !valid {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] CephClusterConnection %s has invalid spec: %s", cephClusterConnection.Name, msg)
		return false, msg, err
	}
	log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] CephClusterConnection %s has valid spec", cephClusterConnection.Name))

	added, err := addFinalizerIfNotExists(ctx, cl, cephClusterConnection, CephClusterConnectionControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to add a finalizer %s to the CephClusterConnection %s: %w", CephClusterConnectionControllerFinalizerName, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}
	log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] finalizer %s was added to the CephClusterConnection %s: %t", CephClusterConnectionControllerFinalizerName, cephClusterConnection.Name, added))

	configMapList := &corev1.ConfigMapList{}
	err = cl.List(ctx, configMapList, client.InNamespace(controllerNamespace))
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to list ConfigMaps in namespace %s: %w", controllerNamespace, err)
		return true, err.Error(), err
	}

	shouldRequeue, msg, err = reconcileConfigMap(ctx, cl, log, configMapList, cephClusterConnection, controllerNamespace, internal.CSICephConfigMapName)
	if err != nil || shouldRequeue {
		return shouldRequeue, msg, err
	}

	secretList := &corev1.SecretList{}
	err = cl.List(ctx, secretList, client.InNamespace(controllerNamespace))
	if err != nil {
		err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to list Secrets in namespace %s: %w", controllerNamespace, err)
		return true, err.Error(), err
	}

	shouldRequeue, msg, err = reconcileSecret(ctx, cl, log, secretList, cephClusterConnection, controllerNamespace, internal.CephClusterConnectionSecretPrefix+cephClusterConnection.Name)
	if err != nil || shouldRequeue {
		return shouldRequeue, msg, err
	}

	if cephClusterConnection.DeletionTimestamp != nil {
		err = removeFinalizerIfExists(ctx, cl, cephClusterConnection, CephClusterConnectionControllerFinalizerName)
		if err != nil {
			err = fmt.Errorf("[RunCephClusterConnectionEventReconcile] unable to remove finalizer from the CephClusterConnection %s: %w", cephClusterConnection.Name, err)
			return true, err.Error(), err
		}
	}

	log.Debug(fmt.Sprintf("[RunCephClusterConnectionEventReconcile] finish all reconciliations for CephClusterConnection %q.", cephClusterConnection.Name))
	return false, msg, nil
}
