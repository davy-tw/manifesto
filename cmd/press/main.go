/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"strings"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	manifestov1alpha1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(manifestov1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var webhookPort int
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Port the webhook server listens on. "+
		"Defaults to 9443. Set -1 to disable the webhook server.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: true,
	}
	var routing controller.Routing
	var heraldService string
	flag.StringVar(&routing.Gateway.Name, "gateway-name", "",
		"Gateway that each Gazette's ListenerSet attaches to. Leave empty to manage routing yourself.")
	flag.StringVar(&routing.Gateway.Namespace, "gateway-namespace", "", "Namespace of --gateway-name.")
	var mode string
	flag.StringVar(&mode, "routing-mode", string(controller.ModeListenerSet),
		"How HTTPRoutes reach the Gateway: \"listenerset\" (a ListenerSet with its own listeners and certificate per "+
			"Gazette) or \"gateway\" (attach to the Gateway's own listeners, e.g. for the Traefik bundled with k3s).")
	flag.StringVar(&routing.GatewaySection, "gateway-section", "",
		"In gateway mode, the Gateway listener HTTPRoutes attach to (sectionName). Empty attaches to all listeners.")
	var httpsPort, httpPort int
	flag.IntVar(&httpsPort, "listener-https-port", 443, "In listenerset mode, the port of each HTTPS listener.")
	flag.IntVar(&httpPort, "listener-http-port", 80, "In listenerset mode, the port of each HTTP listener.")
	flag.StringVar(&heraldService, "herald-service", "manifesto-system/manifesto-herald",
		"herald's Service, as namespace/name, that every HTTPRoute sends traffic to.")
	var heraldPort int
	flag.IntVar(&heraldPort, "herald-port", 80, "Port of --herald-service.")
	flag.StringVar(&routing.ClusterIssuer, "cluster-issuer", "",
		"cert-manager ClusterIssuer for each Gazette's certificate. Leave empty to provide the TLS Secrets yourself.")
	var media controller.Media
	var darkroomSecret string
	flag.StringVar(&media.DarkroomURL, "darkroom-url", "http://manifesto-darkroom.manifesto-system.svc:8081",
		"darkroom's internal address, for pvc Archives. Empty disables the pvc backend.")
	flag.StringVar(&media.DarkroomClaim, "darkroom-claim", "manifesto-media-data",
		"The PersistentVolumeClaim darkroom serves; pvc Archives must name it.")
	flag.StringVar(&darkroomSecret, "darkroom-secret", "manifesto-darkroom-keys",
		"Secret, in press's namespace, holding the key that signs darkroom requests. Created if missing.")
	var editions int
	flag.IntVar(&editions, "editions", controller.DefaultEditionLimit,
		"How many Editions (revisions) to keep per Dispatch; a negative number stops recording them.")
	editionBytes := resource.MustParse("2Mi")
	flag.Func("edition-bytes", "How much content (e.g. 2Mi) to keep in the Editions of one Dispatch, "+
		"so revisions cannot fill etcd; the newest is always kept. 0 means no bound. Default 2Mi.",
		func(v string) (err error) { editionBytes, err = resource.ParseQuantity(v); return err })
	flag.DurationVar(&media.UploadTTL, "upload-ttl", 5*time.Minute, "How long a Print's upload URL stays valid.")
	var deskSecret string
	flag.StringVar(&deskSecret, "desk-session-secret", "manifesto-desk-session",
		"Secret, in press's namespace, with the key desk encrypts session cookies with. Created if missing; empty skips.")
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	if ns, name, ok := strings.Cut(heraldService, "/"); ok {
		routing.Herald.Namespace, routing.Herald.Name = ns, name
	} else {
		setupLog.Error(nil, "--herald-service must be namespace/name", "value", heraldService)
		os.Exit(1)
	}
	routing.HeraldPort = int32(heraldPort) // #nosec G115 -- a port number
	routing.HTTPSPort = int32(httpsPort)   // #nosec G115 -- a port number
	routing.HTTPPort = int32(httpPort)     // #nosec G115 -- a port number
	routing.Mode = controller.RoutingMode(mode)
	if routing.Mode != controller.ModeListenerSet && routing.Mode != controller.ModeGateway {
		setupLog.Error(nil, "--routing-mode must be listenerset or gateway", "value", mode)
		os.Exit(1)
	}
	if routing.Enabled() && routing.Gateway.Namespace == "" {
		setupLog.Error(nil, "--gateway-namespace is required with --gateway-name")
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
		Port:    webhookPort,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	//
	// TODO(user): If you enable certManager, uncomment the following lines:
	// - [METRICS-WITH-CERTS] at config/default/kustomization.yaml to generate and use certificates
	// managed by cert-manager for the metrics server.
	// - [PROMETHEUS-WITH-CERTS] at config/prometheus/kustomization.yaml for TLS certification.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "press.manifesto.davy.tw",
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
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		namespace = "manifesto-system"
	}
	media.Namespace = namespace
	if media.DarkroomURL != "" || deskSecret != "" {
		c, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
		if err == nil && media.DarkroomURL != "" {
			media.SigningKey, err = controller.EnsureSigningKey(context.Background(), c,
				types.NamespacedName{Namespace: namespace, Name: darkroomSecret})
		}
		if err == nil && deskSecret != "" {
			_, err = controller.EnsureKey(context.Background(), c,
				types.NamespacedName{Namespace: namespace, Name: deskSecret}, controller.DeskSessionKey)
		}
		if err != nil {
			setupLog.Error(err, "Failed to set up the darkroom and desk keys")
			os.Exit(1)
		}
	}

	pressOpts := controller.Options{
		Clock: clock.RealClock{}, Routing: routing, Media: media, Editions: editions,
		EditionBytes: editionBytesOption(editionBytes.Value()),
	}
	if err := controller.Setup(mgr, pressOpts); err != nil {
		setupLog.Error(err, "Failed to set up controllers")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

// editionBytesOption maps --edition-bytes to controller.Options: 0 there
// means the default, so "no bound" becomes -1.
func editionBytesOption(n int64) int64 {
	if n <= 0 {
		return -1
	}
	return n
}
