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
	CephClusterAuthenticationCtrlName                = "d8-ceph-cluster-authentication-controller"
	CephClusterAuthenticationControllerFinalizerName = "storage.deckhouse.io/ceph-cluster-authentication-controller"
)

func RunCephClusterAuthenticationWatcherController(
	mgr manager.Manager,
	cfg config.Options,
	log logger.Logger,
) (controller.Controller, error) {
	cl := mgr.GetClient()

	c, err := controller.New(CephClusterAuthenticationCtrlName, mgr, controller.Options{
		Reconciler: reconcile.Func(func(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
			log.Info(fmt.Sprintf("[CephClusterAuthenticationReconciler] starts Reconcile for the CephClusterAuthentication %q", request.Name))
			cephClusterAuthentication := &v1alpha1.CephClusterAuthentication{}
			err := cl.Get(ctx, request.NamespacedName, cephClusterAuthentication)
			if err != nil && !k8serr.IsNotFound(err) {
				log.Error(err, fmt.Sprintf("[CephClusterAuthenticationReconciler] unable to get CephClusterAuthentication, name: %s", request.Name))
				return reconcile.Result{}, err
			}

			if cephClusterAuthentication.Name == "" {
				log.Info(fmt.Sprintf("[CephClusterAuthenticationReconciler] seems like the CephClusterAuthentication for the request %s was deleted. Reconcile retrying will stop.", request.Name))
				return reconcile.Result{}, nil
			}

			secretList := &corev1.SecretList{}
			err = cl.List(ctx, secretList, client.InNamespace(cfg.ControllerNamespace))
			if err != nil {
				log.Error(err, "[CephClusterAuthenticationReconciler] unable to list Secrets")
				return reconcile.Result{}, err
			}

			shouldRequeue, msg, err := RunCephClusterAuthenticationEventReconcile(ctx, cl, log, secretList, cephClusterAuthentication, cfg.ControllerNamespace)
			log.Info(fmt.Sprintf("[CephClusterAuthenticationReconciler] CephClusterAuthentication %s has been reconciled with message: %s", cephClusterAuthentication.Name, msg))
			phase := v1alpha1.PhaseCreated
			if err != nil {
				log.Error(err, fmt.Sprintf("[CephClusterAuthenticationReconciler] an error occured while reconciles the CephClusterAuthentication, name: %s", cephClusterAuthentication.Name))
				phase = v1alpha1.PhaseFailed
			}

			if msg != "" {
				log.Debug(fmt.Sprintf("[CephClusterAuthenticationReconciler] update the CephClusterAuthentication %s with the phase %s and message: %s", cephClusterAuthentication.Name, phase, msg))
				upErr := updateCephClusterAuthenticationPhase(ctx, cl, cephClusterAuthentication, phase, msg)
				if upErr != nil {
					log.Error(upErr, fmt.Sprintf("[CephClusterAuthenticationReconciler] unable to update the CephClusterAuthentication %s: %s", cephClusterAuthentication.Name, upErr.Error()))
					shouldRequeue = true
				}
			}

			if shouldRequeue {
				log.Warning(fmt.Sprintf("[CephClusterAuthenticationReconciler] Reconciler will requeue the request, name: %s", request.Name))
				return reconcile.Result{
					RequeueAfter: cfg.RequeueStorageClassInterval * time.Second,
				}, nil
			}

			log.Info(fmt.Sprintf("[CephClusterAuthenticationReconciler] ends Reconcile for the CephClusterAuthentication %q", request.Name))
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunCephClusterAuthenticationWatcherController] unable to create controller")
		return nil, err
	}

	err = c.Watch(
		source.Kind(mgr.GetCache(), &v1alpha1.CephClusterAuthentication{},
			handler.TypedFuncs[*v1alpha1.CephClusterAuthentication]{
				CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[*v1alpha1.CephClusterAuthentication], q workqueue.RateLimitingInterface) {
					log.Info(fmt.Sprintf("[CreateFunc] get event for CephClusterAuthentication %q. Add to the queue", e.Object.GetName()))
					request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}}
					q.Add(request)
				},
				UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[*v1alpha1.CephClusterAuthentication], q workqueue.RateLimitingInterface) {
					log.Info(fmt.Sprintf("[UpdateFunc] get event for CephClusterAuthentication %q. Check if it should be reconciled", e.ObjectNew.GetName()))

					oldCephClusterAuthentication := e.ObjectOld
					newCephClusterAuthentication := e.ObjectNew

					if reflect.DeepEqual(oldCephClusterAuthentication.Spec, newCephClusterAuthentication.Spec) && newCephClusterAuthentication.DeletionTimestamp == nil {
						log.Info(fmt.Sprintf("[UpdateFunc] an update event for the CephClusterAuthentication %s has no Spec field updates. It will not be reconciled", newCephClusterAuthentication.Name))
						return
					}

					log.Info(fmt.Sprintf("[UpdateFunc] the CephClusterAuthentication %q will be reconciled. Add to the queue", newCephClusterAuthentication.Name))
					request := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: newCephClusterAuthentication.Namespace, Name: newCephClusterAuthentication.Name}}
					q.Add(request)
				},
			},
		),
	)

	if err != nil {
		log.Error(err, "[RunCephClusterAuthenticationWatcherController] unable to watch the events")
		return nil, err
	}

	return c, nil
}

func RunCephClusterAuthenticationEventReconcile(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterAuthentication *v1alpha1.CephClusterAuthentication, controllerNamespace string) (shouldRequeue bool, msg string, err error) {
	valid, msg := validateCephClusterAuthenticationSpec(cephClusterAuthentication)
	if !valid {
		err = fmt.Errorf("[RunCephClusterAuthenticationEventReconcile] CephClusterAuthentication %s has invalid spec: %s", cephClusterAuthentication.Name, msg)
		return false, msg, err
	}
	log.Debug(fmt.Sprintf("[RunCephClusterAuthenticationEventReconcile] CephClusterAuthentication %s has valid spec", cephClusterAuthentication.Name))

	added, err := addFinalizerIfNotExists(ctx, cl, cephClusterAuthentication, CephClusterAuthenticationControllerFinalizerName)
	if err != nil {
		err = fmt.Errorf("[RunCephClusterAuthenticationEventReconcile] unable to add a finalizer %s to the CephClusterAuthentication %s: %w", CephClusterAuthenticationControllerFinalizerName, cephClusterAuthentication.Name, err)
		return true, err.Error(), err
	}
	log.Debug(fmt.Sprintf("[RunCephClusterAuthenticationEventReconcile] finalizer %s was added to the CephClusterAuthentication %s: %t", CephClusterAuthenticationControllerFinalizerName, cephClusterAuthentication.Name, added))

	secretName := internal.CephClusterAuthenticationSecretPrefix + cephClusterAuthentication.Name
	reconcileTypeForSecret, err := IdentifyReconcileFuncForSecret(log, secretList, cephClusterAuthentication, controllerNamespace, secretName)
	if err != nil {
		err = fmt.Errorf("[RunCephClusterAuthenticationEventReconcile] error occurred while identifying the reconcile function for CephClusterAuthentication %s on Secret %s: %w", cephClusterAuthentication.Name, secretName, err)
		return true, err.Error(), err
	}

	shouldRequeue = false
	log.Debug(fmt.Sprintf("[RunCephClusterAuthenticationEventReconcile] successfully identified the reconcile type for CephClusterAuthentication %s to be performed on Secret %s: %s", cephClusterAuthentication.Name, secretName, reconcileTypeForSecret))
	switch reconcileTypeForSecret {
	case internal.CreateReconcile:
		shouldRequeue, msg, err = reconcileSecretCreateFunc(ctx, cl, log, cephClusterAuthentication, controllerNamespace, secretName)
	case internal.UpdateReconcile:
		shouldRequeue, msg, err = reconcileSecretUpdateFunc(ctx, cl, log, secretList, cephClusterAuthentication, controllerNamespace, secretName)
	case internal.DeleteReconcile:
		shouldRequeue, msg, err = reconcileSecretDeleteFunc(ctx, cl, log, secretList, cephClusterAuthentication, secretName)
	default:
		log.Debug(fmt.Sprintf("[RunCephClusterAuthenticationEventReconcile] no reconcile action required for CephClusterAuthentication %s on Secret %s. No changes will be made.", cephClusterAuthentication.Name, secretName))
		msg = "Successfully reconciled"
	}
	log.Debug(fmt.Sprintf("[RunCephClusterAuthenticationEventReconcile] completed reconcile operation for CephClusterAuthentication %s on Secret %s.", cephClusterAuthentication.Name, secretName))

	if err != nil || shouldRequeue {
		return shouldRequeue, msg, err
	}

	log.Debug(fmt.Sprintf("[RunCephClusterAuthenticationEventReconcile] finish all reconciliations for CephClusterAuthentication %q.", cephClusterAuthentication.Name))
	return false, msg, nil
}
