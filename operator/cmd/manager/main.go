// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command manager is the Olivares AI control-plane operator entrypoint. It runs a
// controller-runtime manager hosting the ControlPlane reconciler (the declarative
// lifecycle of the engine: install/upgrade/reconfigure/backup-restore).
//
// It imports neither /core nor /sdk. Like terraform-provider-olivares, the
// operator is a separate module keeping the controller-runtime/client-go tree out
// of the engine's SBOM.
package main

import (
	"crypto/tls"
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	opsv1alpha1 "github.com/olivaresai/olivares/operator/api/v1alpha1"
	"github.com/olivaresai/olivares/operator/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	// Built-in types (apps/v1, core/v1, batch/v1 ...) + our ControlPlane CRD.
	utilRuntimeMust(clientgoscheme.AddToScheme(scheme))
	utilRuntimeMust(opsv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		probeAddr            string
		enableLeaderElection bool
		secureMetrics        bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address the metric endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address the probe endpoint binds to")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"enable leader election for controller manager (ensures a single active manager)")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"serve metrics over HTTPS (self-signed by default; front with a real cert in prod)")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	metricsOpts := metricsserver.Options{BindAddress: metricsAddr}
	if secureMetrics {
		metricsOpts.SecureServing = true
		metricsOpts.TLSOpts = []func(*tls.Config){}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsOpts,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "olivares.ops.olivares.ai",
		Cache:                  cacheOptions(),
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.ControlPlaneReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		// The UNCACHED reader. The cache deliberately strips Secret payloads (see
		// cacheOptions), so the config-hash — which must fold the referenced Secret's
		// CONTENT into the rollout annotation — reads through this instead.
		APIReader: mgr.GetAPIReader(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ControlPlane")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// cacheOptions bounds what the manager's informers hold in memory. The
// ControlPlane reconciler watches Pods (to resolve which one publishes the HA
// leader label) and ConfigMaps/Secrets (so a config edit actually triggers the
// reconfigure rollout it promises) — and an unbounded watch of either would be a
// real operational cost, not a theoretical one:
//
//   - Pods are cached ONLY for workloads this operator renders (the common
//     app.kubernetes.io labels), so a cluster with 50k unrelated pods costs this
//     manager nothing.
//   - Secrets are cached with their PAYLOAD STRIPPED. A cluster-wide Secret informer
//     would otherwise park every credential in the cluster in the operator's heap
//     (and in any heap dump of it). The watch still fires on every change — which is
//     all the reconcile trigger needs — and the one place that genuinely needs the
//     content, the config hash, reads it live through the uncached APIReader.
func cacheOptions() cache.Options {
	workloadPods, err := labels.Parse("app.kubernetes.io/name=olivares,app.kubernetes.io/managed-by=olivares-operator")
	if err != nil {
		// A compile-time-constant selector: unreachable, but never silently widen the
		// cache if it somehow is.
		setupLog.Error(err, "invalid workload pod selector")
		os.Exit(1)
	}
	return cache.Options{
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Label: workloadPods},
			&corev1.Secret{}: {Transform: func(obj any) (any, error) {
				sec, ok := obj.(*corev1.Secret)
				if !ok {
					return obj, nil
				}
				stripped := sec.DeepCopy()
				stripped.Data = nil
				stripped.StringData = nil
				return stripped, nil
			}},
		},
	}
}

// utilRuntimeMust panics on a non-nil scheme registration error at startup.
func utilRuntimeMust(err error) {
	if err != nil {
		setupLog.Error(err, "scheme registration failed")
		os.Exit(1)
	}
}
