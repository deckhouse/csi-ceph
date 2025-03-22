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
	"fmt"

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
	"github.com/deckhouse/csi-ceph/images/controller/pkg/logger"
)

const (
	// This value used as a name for the controller AND the value for managed-by label.
	CephClusterAuthenticationCtrlName                = "d8-ceph-cluster-authentication-controller"
	CephClusterAuthenticationControllerFinalizerName = "storage.deckhouse.io/ceph-cluster-authentication-controller"
	DeprecatedLabel                                  = "storage.deckhouse.io/deprecated"
	DeprecatedLabelValue                             = "true"
)

func RunCephClusterAuthenticationWatcherController(
	mgr manager.Manager,
	_ config.Options,
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

			err = labelAsDeprecatedIfNeeded(ctx, cl, cephClusterAuthentication)
			if err != nil {
				log.Error(err, fmt.Sprintf("[CephClusterAuthenticationReconciler] unable to label as deprecated CephClusterAuthentication %q", cephClusterAuthentication.Name))
				return reconcile.Result{}, err
			}

			log.Info(fmt.Sprintf("[CephClusterAuthenticationReconciler] ends Reconcile for the CephClusterAuthentication %q", request.Name))
			return reconcile.Result{}, nil
		}),
	})
	if err != nil {
		log.Error(err, "[RunCephClusterAuthenticationWatcherController] unable to create controller")
		return nil, err
	}

	err = c.Watch(source.Kind(mgr.GetCache(), &v1alpha1.CephClusterAuthentication{}, handler.TypedFuncs[*v1alpha1.CephClusterAuthentication, reconcile.Request]{
		CreateFunc: func(_ context.Context, e event.TypedCreateEvent[*v1alpha1.CephClusterAuthentication], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Warning(fmt.Sprintf("[CreateFunc] get event for CephClusterAuthentication %q. Label as deprecated", e.Object.GetName()))
			err := labelAsDeprecatedIfNeeded(context.Background(), cl, e.Object)
			if err != nil {
				log.Error(err, fmt.Sprintf("[CreateFunc] unable to label as deprecated CephClusterAuthentication %q", e.Object.GetName()))
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Object.GetNamespace(), Name: e.Object.GetName()}})
			}

			log.Warning(fmt.Sprintf("[CreateFunc] Successfully labeled as deprecated CephClusterAuthentication %q", e.Object.GetName()))
		},
		UpdateFunc: func(_ context.Context, e event.TypedUpdateEvent[*v1alpha1.CephClusterAuthentication], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Warning(fmt.Sprintf("[UpdateFunc] get event for CephClusterAuthentication %q. Label as deprecated", e.ObjectNew.GetName()))
			err := labelAsDeprecatedIfNeeded(context.Background(), cl, e.ObjectNew)
			if err != nil {
				log.Error(err, fmt.Sprintf("[UpdateFunc] unable to label as deprecated CephClusterAuthentication %q", e.ObjectNew.GetName()))
				q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.ObjectNew.GetNamespace(), Name: e.ObjectNew.GetName()}})
			}

			log.Warning(fmt.Sprintf("[UpdateFunc] Successfully labeled as deprecated CephClusterAuthentication %q", e.ObjectNew.GetName()))
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

func labelAsDeprecatedIfNeeded(ctx context.Context, cl client.Client, cephClusterAuthentication *v1alpha1.CephClusterAuthentication) error {
	if cephClusterAuthentication.DeletionTimestamp != nil {
		if len(cephClusterAuthentication.GetFinalizers()) > 0 {
			cephClusterAuthentication.SetFinalizers([]string{})
			return cl.Update(ctx, cephClusterAuthentication)
		}
		return nil
	}

	if cephClusterAuthentication.Labels == nil {
		cephClusterAuthentication.Labels = map[string]string{}
	}

	if cephClusterAuthentication.Labels[DeprecatedLabel] == DeprecatedLabelValue {
		return nil
	}

	cephClusterAuthentication.Labels[DeprecatedLabel] = DeprecatedLabelValue
	return cl.Update(ctx, cephClusterAuthentication)
}
