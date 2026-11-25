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

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	goruntime "runtime"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	v1 "k8s.io/api/core/v1"
	sv1 "k8s.io/api/storage/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/cephconn"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/config"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/controller"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/kubutils"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/rbdcapacity"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/rbdwatch"
	"github.com/deckhouse/csi-ceph/images/webhooks/handlers"
	"github.com/deckhouse/sds-common-lib/logger"
	"github.com/deckhouse/sds-common-lib/monitoring"
	"github.com/deckhouse/sds-common-lib/monitoring/metricsauth"
	"github.com/deckhouse/sds-common-lib/webhookserver"
)

var (
	resourcesSchemeFuncs = []func(*apiruntime.Scheme) error{
		v1alpha1.AddToScheme,
		clientgoscheme.AddToScheme,
		extv1.AddToScheme,
		v1.AddToScheme,
		sv1.AddToScheme,
		snapshotv1.AddToScheme,
	}
)

func main() {
	ctx := context.Background()
	cfgParams := config.NewConfig()

	baseLog, err := logger.NewLogger(cfgParams.Loglevel)
	if err != nil {
		fmt.Printf("unable to create NewLogger, err: %v\n", err)
		os.Exit(1)
	}
	ctrl.SetLogger(baseLog.GetLogger())
	log := baseLog.Named("main")

	log.Info("starting csi-ceph-controller",
		"goVersion", goruntime.Version(), "os", goruntime.GOOS, "arch", goruntime.GOARCH)
	log.Info("configuration",
		"logLevel", string(cfgParams.Loglevel),
		"requeueStorageClassInterval", cfgParams.RequeueStorageClassInterval.String(),
		"controllerNamespace", cfgParams.ControllerNamespace,
		"healthProbeBindAddress", cfgParams.HealthProbeBindAddress,
		"metricsBindAddress", cfgParams.MetricsBindAddress)

	kConfig, err := kubutils.KubernetesDefaultConfigCreate()
	if err != nil {
		// Without a config the manager cannot be built at all; continuing only moved
		// the failure a few lines down, to a message that does not name the cause.
		log.Error("unable to KubernetesDefaultConfigCreate", logger.Err(err))
		os.Exit(1)
	}
	log.Info("kubernetes config has been successfully created.")

	scheme := apiruntime.NewScheme()
	for _, f := range resourcesSchemeFuncs {
		err := f(scheme)
		if err != nil {
			log.Error("unable to add scheme to func", logger.Err(err))
			os.Exit(1)
		}
	}
	log.Info("successfully read scheme CR")

	cacheOpt := cache.Options{
		DefaultNamespaces: map[string]cache.Config{
			cfgParams.ControllerNamespace: {},
		},
		DefaultTransform: cache.TransformStripManagedFields(),
	}

	// Configured, not yet serving: the listener is opened by the manager below, and a
	// port already in use is reported from mgr.Start.
	log.Info("metrics endpoint configured", "address", cfgParams.MetricsBindAddress+monitoring.MetricsPath)

	managerOpts := manager.Options{
		Scheme:                  scheme,
		Cache:                   cacheOpt,
		Metrics:                 metricsauth.ApplyFromEnv(metricsserver.Options{BindAddress: cfgParams.MetricsBindAddress}),
		HealthProbeBindAddress:  cfgParams.HealthProbeBindAddress,
		LeaderElection:          true,
		LeaderElectionNamespace: cfgParams.ControllerNamespace,
		LeaderElectionID:        config.ControllerName,
		Logger:                  baseLog.GetLogger(),
	}

	mgr, err := manager.New(kConfig, managerOpts)
	if err != nil {
		log.Error("unable to manager.New", logger.Err(err))
		os.Exit(1)
	}
	log.Info("successfully created kubernetes manager")

	if _, err = controller.RunCephStorageClassWatcherController(mgr, *cfgParams, baseLog.Named(controller.CephStorageClassCtrlName)); err != nil {
		log.Error("unable to run the controller", logger.Err(err), slog.String("controller", controller.CephStorageClassCtrlName))
		os.Exit(1)
	}

	if _, err = controller.RunCephClusterConnectionWatcherController(mgr, *cfgParams, baseLog.Named(controller.CephClusterConnectionCtrlName)); err != nil {
		log.Error("unable to run the controller", logger.Err(err), slog.String("controller", controller.CephClusterConnectionCtrlName))
		os.Exit(1)
	}

	if _, err = controller.RunCephClusterAuthenticationWatcherController(mgr, *cfgParams, baseLog.Named(controller.CephClusterAuthenticationCtrlName)); err != nil {
		log.Error("unable to run the controller", logger.Err(err), slog.String("controller", controller.CephClusterAuthenticationCtrlName))
		os.Exit(1)
	}

	cephConns := cephconn.NewConns(cfgParams.MsCrcData)
	if err = mgr.Add(cephConns); err != nil {
		log.Error("unable to add the Ceph connection pool", logger.Err(err))
		os.Exit(1)
	}

	watchChecker := &rbdwatch.Checker{
		Client:   mgr.GetClient(),
		Counter:  rbdwatch.CephCounter{Conns: cephConns},
		Recorder: mgr.GetEventRecorder(rbdwatch.CheckerName),
		Log:      baseLog.Named(rbdwatch.CheckerName),
		Interval: rbdwatch.DefaultInterval,
	}
	if err = mgr.Add(watchChecker); err != nil {
		log.Error("unable to add the RBD watch checker", logger.Err(err))
		os.Exit(1)
	}
	if err = metrics.Registry.Register(rbdwatch.Collector{Checker: watchChecker}); err != nil {
		log.Error("unable to register the RBD watch metrics", logger.Err(err))
		os.Exit(1)
	}

	capacityPublisher := &rbdcapacity.Publisher{
		Client:   mgr.GetClient(),
		Reader:   rbdcapacity.CephPoolReader{Conns: cephConns},
		Log:      baseLog.Named(rbdcapacity.PublisherName),
		Interval: rbdcapacity.DefaultInterval,
	}
	if err = mgr.Add(capacityPublisher); err != nil {
		log.Error("unable to add the RBD capacity publisher", logger.Err(err))
		os.Exit(1)
	}
	if err = metrics.Registry.Register(rbdcapacity.Collector{Publisher: capacityPublisher}); err != nil {
		log.Error("unable to register the RBD capacity metrics", logger.Err(err))
		os.Exit(1)
	}

	webhookOpts, webhooksEnabled, err := webhookserver.OptionsFromEnv()
	if err != nil {
		log.Error("unable to read the webhook server configuration", logger.Err(err))
		os.Exit(1)
	}
	if webhooksEnabled {
		cs, err := kubernetes.NewForConfig(kConfig)
		if err != nil {
			log.Error("unable to create the Kubernetes client for the webhooks", logger.Err(err))
			os.Exit(1)
		}
		hs, err := handlers.Handlers(baseLog.Named("webhooks"), cs)
		if err != nil {
			log.Error("unable to create the webhook handlers", logger.Err(err))
			os.Exit(1)
		}
		if err = webhookserver.Add(mgr, webhookOpts, hs); err != nil {
			log.Error("unable to add the webhook server", logger.Err(err))
			os.Exit(1)
		}
		log.Info("serving the admission webhooks", "port", webhookOpts.Port, "certDir", webhookOpts.CertDir)
	}

	if err = mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error("unable to mgr.AddHealthzCheck", logger.Err(err))
		os.Exit(1)
	}
	log.Info("successfully AddHealthzCheck")

	if err = mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error("unable to mgr.AddReadyzCheck", logger.Err(err))
		os.Exit(1)
	}
	log.Info("successfully AddReadyzCheck")

	err = mgr.Start(ctx)
	if err != nil {
		log.Error("unable to mgr.Start", logger.Err(err))
		os.Exit(1)
	}
}
