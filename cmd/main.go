/*
Copyright 2024.

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
	"crypto/tls"
	"flag"
	"os"
	"time"

	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(seaweedv1.AddToScheme(scheme))
	utilruntime.Must(monitorv1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics server.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set, the metrics endpoint is served securely via HTTPS with authn/authz. "+
			"Use --metrics-secure=false to serve over plain HTTP instead.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	var bucketUsageInterval time.Duration
	flag.DurationVar(&bucketUsageInterval, "bucket-usage-refresh-interval", controller.DefaultUsageRefreshInterval,
		"Cadence for refreshing status.usage on Bucket resources. "+
			"Set to 0 to disable. Defaults to 5m. Each tick issues one collection.list "+
			"call per Seaweed cluster that owns Buckets, then patches per-bucket status.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	// When metrics are served securely, protect the endpoint with authn/authz via
	// controller-runtime's built-in filter, replacing the old kube-rbac-proxy sidecar.
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}
	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "674006ec.seaweedfs.com",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.SeaweedReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controller").WithName("Seaweed"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("seaweed-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Seaweed")
		os.Exit(1)
	}

	if err = (&controller.BucketReconciler{
		Client:               mgr.GetClient(),
		Log:                  ctrl.Log.WithName("controller").WithName("Bucket"),
		Scheme:               mgr.GetScheme(),
		Recorder:             mgr.GetEventRecorderFor("bucket-controller"),
		UsageRefreshInterval: bucketUsageInterval,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Bucket")
		os.Exit(1)
	}

	if err = (&controller.S3IdentityReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controller").WithName("S3Identity"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("s3identity-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "S3Identity")
		os.Exit(1)
	}

	if err = (&controller.S3CredentialsReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controller").WithName("S3Credentials"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("s3credentials-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "S3Credentials")
		os.Exit(1)
	}

	if err = (&controller.S3PolicyReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controller").WithName("S3Policy"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("s3policy-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "S3Policy")
		os.Exit(1)
	}

	if err = (&controller.S3PolicyBindingReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controller").WithName("S3PolicyBinding"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("s3policybinding-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "S3PolicyBinding")
		os.Exit(1)
	}

	if err = (&controller.AdminScriptReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controller").WithName("AdminScript"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("adminscript-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AdminScript")
		os.Exit(1)
	}

	// The S3OIDCProvider transport is not wired yet (the filer IAM gRPC service
	// has no OIDC methods), so registering it would fail-loop on every cluster.
	// Keep it off by default until the server-side RPCs land; opt in with
	// ENABLE_S3_OIDC_PROVIDER=true for development.
	if os.Getenv("ENABLE_S3_OIDC_PROVIDER") == "true" {
		if err = (&controller.S3OIDCProviderReconciler{
			Client:   mgr.GetClient(),
			Log:      ctrl.Log.WithName("controller").WithName("S3OIDCProvider"),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorderFor("s3oidcprovider-controller"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "S3OIDCProvider")
			os.Exit(1)
		}
	} else {
		setupLog.Info("S3OIDCProvider controller disabled (set ENABLE_S3_OIDC_PROVIDER=true to enable; requires filer OIDC gRPC support)")
	}

	// The CSI driver deployment is a node-global concern and ships off by
	// default: enabling it registers a cluster-wide CSIDriver plus privileged
	// node and mount DaemonSets. Opt in with ENABLE_CSI_DRIVER=true.
	if os.Getenv("ENABLE_CSI_DRIVER") == "true" {
		if err = (&controller.SeaweedCSIDriverReconciler{
			Client:   mgr.GetClient(),
			Log:      ctrl.Log.WithName("controller").WithName("SeaweedCSIDriver"),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorderFor("seaweedcsidriver-controller"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "SeaweedCSIDriver")
			os.Exit(1)
		}
	} else {
		setupLog.Info("SeaweedCSIDriver controller disabled (set ENABLE_CSI_DRIVER=true to enable)")
	}

	if os.Getenv("ENABLE_WEBHOOKS") != "false" {
		if err = (&seaweedv1.Seaweed{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Seaweed")
			os.Exit(1)
		}
	}
	// +kubebuilder:scaffold:builder

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
