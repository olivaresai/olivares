// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/serverhandover"
	"github.com/olivaresai/olivares/core/webui"
)

// serveOptions are the resolved knobs for running the engine. Both `serve` and
// `quickstart` build one and hand it to runEngine, so the secure boot/serve path
// lives in exactly one place.
type serveOptions struct {
	listen, grpcListen    string
	dataDir, engine, dsn  string
	adminDSN, ownerDSN    string
	region                string
	knownRegions          []string
	tlsCert, tlsKey, lic  string
	grpcClientCA          string
	insecure, seedDemo    bool
	insecureAllowPublic   bool
	allowPrivilegedDBRole bool
	reusePort             bool
	checkpointInterval    time.Duration
}

// newServeCmd runs the engine: the REST/web HTTP server and the gRPC server,
// TLS-on-by-default, with no default credentials (a one-time setup token is
// printed on first boot).
func newServeCmd() *cobra.Command {
	opts := serveOptions{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the engine (REST + gRPC + embedded console), TLS-on-by-default",
		Long: "serve starts the Olivares control plane: REST API, embedded web console, gRPC ingest,\n" +
			"configured modules and source connectors. It uses TLS by default, opens the selected SQLite\n" +
			"or Postgres store, and prints one-time setup guidance on a first boot.",
		Example: `  # Start the control plane on the default address
  olivares serve --data-dir /var/lib/olivares

  # Serve with TLS and Postgres backend
  olivares serve --engine postgres --dsn "file:/etc/olivares/secrets/db.dsn" \
    --tls-cert /etc/olivares/cert.pem --tls-key /etc/olivares/key.pem

  # Development mode (plaintext, loopback only)
  olivares serve --insecure --listen 127.0.0.1:8080`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			announce := func(ctx context.Context, out io.Writer, eng *engine) error {
				if opts.seedDemo {
					if err := announceDemo(ctx, out, eng); err != nil {
						return err
					}
					if err := eng.api.RunStartupBackup(ctx, demoPassword, "demo estate startup backup", "demo-seed"); err != nil {
						eng.log.Warn("demo: startup backup failed; continuing serve", "err", err)
					}
					return nil
				}
				return announceSetup(ctx, out, eng, consoleURL(opts.listen, opts.insecure), opts.insecure)
			}
			return runEngine(cmd.Context(), cmd.OutOrStdout(), opts, announce)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", "127.0.0.1:8443", "HTTP (REST + web) listen address")
	cmd.Flags().StringVar(&opts.grpcListen, "grpc-listen", "127.0.0.1:8444", "gRPC listen address")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().StringVar(&opts.engine, "engine", "sqlite", "store engine: sqlite or postgres")
	_ = cmd.RegisterFlagCompletionFunc("engine", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"sqlite", "postgres"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&opts.dsn, "dsn", "", "store DSN (default a SQLite file in the data dir). May be a file:<path> or env:<VAR> reference resolved at boot, so the password stays out of the env file")
	cmd.Flags().StringVar(&opts.adminDSN, "admin-dsn", "", "Postgres only: DSN of a dedicated NOSUPERUSER BYPASSRLS role used ONLY for cross-tenant System reads (org list, multi-tenant checkpoint coverage). Without it those reads are RLS-limited (see deploy/postgres/01-app-role.sql)")
	cmd.Flags().StringVar(&opts.ownerDSN, "owner-dsn", "", "Postgres only: DSN of the owner role that owns the schema and runs DDL/migrations. Set it to a SEPARATE NOSUPERUSER NOBYPASSRLS role to make --dsn a least-privilege non-owner app role with only DML grants (provision both with `olivares db init`). Empty = the --dsn role owns the schema (single-role). Accepts a file:/env: reference like --dsn")
	cmd.Flags().StringVar(&opts.region, "region", "", "data-residency HOME region of THIS instance (e.g. eu, us). When set, the instance is region-scoped: it serves only tenants pinned to this region and denies cross-region access fail-closed. Empty = single-region mode, no residency enforcement")
	cmd.Flags().StringSliceVar(&opts.knownRegions, "known-regions", nil, "comma-separated region codes valid across the whole deployment (e.g. eu,us); a tenant pin must be one of these. The home --region is always included. Only meaningful with --region set")
	cmd.Flags().StringVar(&opts.tlsCert, "tls-cert", "", "TLS certificate PEM (default a self-signed cert in the data dir)")
	cmd.Flags().StringVar(&opts.tlsKey, "tls-key", "", "TLS private key PEM")
	cmd.Flags().StringVar(&opts.grpcClientCA, "grpc-client-ca", "", "PEM bundle of CAs authorized to issue collector client certs; when set, the gRPC server requires mutual TLS (verified client cert) for collector→core (docs/SECURITY-HARDENING.md §1/§3)")
	cmd.Flags().StringVar(&opts.lic, "license", "", "path to a commercial license file (informational only)")
	cmd.Flags().BoolVar(&opts.insecure, "insecure", false, "serve plaintext HTTP/gRPC (DANGEROUS; localhost dev only). A non-loopback bind is REFUSED unless --insecure-allow-public-bind is also given")
	cmd.Flags().BoolVar(&opts.insecureAllowPublic, "insecure-allow-public-bind", false, "with --insecure, allow binding a non-loopback address (DANGEROUS: the console, bearer tokens and the first-boot setup token cross the network in CLEAR TEXT). Only for a deployment where something in front of the engine terminates TLS. Inert without --insecure")
	cmd.Flags().BoolVar(&opts.allowPrivilegedDBRole, "allow-privileged-db-role", false, "allow connecting Postgres as a superuser/BYPASSRLS role (DANGEROUS: disables the row-level-security tenant backstop; single-tenant/dev only)")
	cmd.Flags().BoolVar(&opts.seedDemo, "seed-demo", false, "load a SYNTHETIC sample estate for demos/E2E (fabricated data; use a throwaway data-dir)")
	cmd.Flags().DurationVar(&opts.checkpointInterval, "checkpoint-interval", time.Hour, "how often to write a signed audit checkpoint over every tenant chain (0 disables; tamper-evidence anchor, docs/SECURITY-HARDENING.md §5)")
	cmd.Flags().BoolVar(&opts.reusePort, "reuse-port", false, "bind listeners with SO_REUSEPORT so a NEW instance can hold the same ports while this one drains — enables a zero-downtime restart/upgrade handover on a single node (Linux/BSD; docs/UPGRADE-AND-ROLLBACK.md)")
	return cmd
}

// runEngine boots the engine and serves it (HTTP/REST/web + gRPC, plus any
// provisioned side servers), TLS-on-by-default. It calls announce once after boot
// — before the listeners start — to print the first-run guidance for the caller's
// mode (serve setup-token, demo, or quickstart). This is the single secure boot/
// serve path shared by `serve` and `quickstart`.
func runEngine(ctx context.Context, out io.Writer, opts serveOptions, announce func(context.Context, io.Writer, *engine) error) error {
	log := slog.Default()

	// Trust-domain collision is a BUILD defect only the artifact can reveal: a direct
	// -ldflags build bypasses scripts/check-release-pubkey.sh, so surface it on every
	// boot of a long-lived server, not just from `version`.
	if w := keyDomainCollisionWarning(); w != "" {
		log.Warn(w)
	}

	// --seed-demo provisions a demo superadmin with a PUBLIC, source-tree password
	// (demo.go). It must never be reachable off-host: refuse to start it on a
	// non-loopback listener (docs/SECURITY-HARDENING.md — no default credentials).
	if opts.seedDemo && (!hostIsLoopback(opts.listen) || !hostIsLoopback(opts.grpcListen)) {
		return fmt.Errorf("--seed-demo mints a demo superadmin with a PUBLIC password and must stay on the local host; refusing a non-loopback bind (listen=%q grpc-listen=%q). Bind 127.0.0.1, or run a real install WITHOUT --seed-demo", opts.listen, opts.grpcListen)
	}

	// --insecure turns TLS off. Off-host, that puts the console, every bearer
	// token and the first-boot setup token on the wire in clear. Refuse before
	// anything binds (docs/SECURITY-HARDENING.md).
	if err := insecureBindGuard(opts.insecure, opts.insecureAllowPublic, opts.listen, opts.grpcListen); err != nil {
		return err
	}

	var tlsLoader *secure.CertificateLoader
	var tlsCertNotAfter func() (time.Time, bool)
	if !opts.insecure {
		// The API is built before TLS material is resolved below. Capture the
		// loader by reference so health-summary reads the live, reload-aware leaf
		// once startup installs it.
		tlsCertNotAfter = func() (time.Time, bool) {
			if tlsLoader == nil {
				return time.Time{}, false
			}
			if err := tlsLoader.Load(); err != nil {
				return time.Time{}, false
			}
			return tlsLoader.NotAfter()
		}
	}

	eng, err := boot(ctx, bootConfig{
		DataDir: opts.dataDir, Engine: opts.engine, DSN: opts.dsn, AdminDSN: opts.adminDSN, OwnerDSN: opts.ownerDSN, LicenseFile: opts.lic,
		Version: version, Logger: log, DemoSeed: opts.seedDemo,
		AllowPrivilegedDBRole: opts.allowPrivilegedDBRole,
		Region:                opts.region, KnownRegions: opts.knownRegions,
		ServeMode:       true, // long-lived server: OK to run the background update-check
		TLSCertNotAfter: tlsCertNotAfter,
	})
	if err != nil {
		return err
	}
	defer func() { _ = eng.Close() }()

	// Schedule signed audit checkpoints (docs/SECURITY-HARDENING.md). Registered AFTER the
	// eng.Close defer so its final shutdown checkpoint runs BEFORE the store
	// closes (defers are LIFO).
	cp := startCheckpointer(eng.signer, eng.store, opts.checkpointInterval, log, eng.metrics)
	defer cp.stop(context.Background())

	if err := announce(ctx, out, eng); err != nil {
		return err
	}

	tlsCert, tlsKey := opts.tlsCert, opts.tlsKey
	if tlsCert == "" {
		tlsCert = filepath.Join(eng.dataDir, "tls.crt")
	}
	if tlsKey == "" {
		tlsKey = filepath.Join(eng.dataDir, "tls.key")
	}

	// Ensure TLS material ONCE, up front, before any listener accepts — so
	// both HTTP and gRPC use the same cert and neither falls back to plaintext.
	if !opts.insecure {
		created, fp, terr := secure.EnsureTLSCert(tlsCert, tlsKey)
		if terr != nil {
			return terr
		}
		if created {
			log.Warn("generated a self-signed TLS certificate; "+pinAdvice, tlsTrustAttrs(tlsCert, fp)...)
		} else {
			log.Info("serving HTTPS; "+pinAdvice, tlsTrustAttrs(tlsCert, fp)...)
		}
		tlsLoader, terr = secure.NewCertificateLoader(tlsCert, tlsKey)
		if terr != nil {
			return terr
		}
		registerTLSCertificateExpiry(eng.metrics, tlsLoader, true)
		warnTLSCertificateExpiry(log, tlsLoader, time.Now())
	}

	httpSrv := eng.api.NewHTTPServer(opts.listen)
	// Serve the embedded web UI on the SAME origin as the API, with SPA
	// fallback to index.html for client-side routes. The static surface is
	// wrapped OUTSIDE the API's auth/setup middleware (see webui.go).: the
	// enterprise build further wraps it with the unauthenticated SP-metadata
	// endpoint (public by design); the default build leaves it unchanged.
	httpSrv.Handler = withEnterpriseHTTP(newSPAHandler(eng.api.Handler(), webui.FS()), eng, log)
	// PIV/CAC: when configured, the HTTP listener REQUESTS (never
	// requires) a client certificate and verifies a presented one against
	// the PIV CA — VerifyClientCertIfGiven keeps every certless browser and
	// SDK client untouched while /v1/auth/piv/* reads the verified peer
	// certificate. The route needs direct TLS at this listener (no XFCC).
	if !opts.insecure && eng.pivConfig != nil && eng.pivConfig.Roots != nil {
		httpSrv.TLSConfig = &tls.Config{
			ClientAuth: tls.VerifyClientCertIfGiven,
			ClientCAs:  eng.pivConfig.Roots,
		}
		log.Info("piv: HTTP listener requests optional client certificates (PIV/CAC route armed)")
	}
	grpcSrv, gerr := newGRPCServer(eng, tlsLoader, opts.grpcClientCA, opts.insecure)
	if gerr != nil {
		return gerr // fail closed: never serve gRPC plaintext unless --insecure
	}
	if !opts.insecure && opts.grpcClientCA != "" {
		log.Info("gRPC mutual TLS enabled: collectors must present a client certificate", "client_ca", opts.grpcClientCA)
	}

	// Mount the inbound HITL round-trip receiver on its OWN socket, if the
	// operator provisioned it (OLIVARES_HITL_CONFIG). It is loopback-default
	// (secure-default); a deployment that must receive Slack/ITSM callbacks
	// fronts it with the operator's ingress and sets a reachable bind. Its security
	// is fail-closed signature verification, not network isolation. nil when unset.
	hitlSrv, err := buildHITLReceiverServer(eng, tlsCert, tlsKey, opts.insecure, log)
	if err != nil {
		return err
	}

	// Mount the inbound OpenAI Realtime SIP webhook receiver on its OWN socket, if
	// the operator provisioned OLIVARES_VOICE_CALL_CONFIG. It mirrors HITL's
	// posture: loopback-default and secured by fail-closed webhook verification.
	voiceWebhookSrv, err := buildVoiceWebhookServer(eng, opts.insecure, log)
	if err != nil {
		return err
	}

	// the inbound agent-protocols gateway (inline MCP Resource-Server PEP
	// + A2A push-notification receiver) on its own socket, if provisioned
	// (OLIVARES_AGENT_GATEWAY_CONFIG). Loopback-default; its security is
	// fail-closed token/JWT verification, not network isolation. nil when unset.
	gatewaySrv, err := buildAgentGatewayServer(eng, log)
	if err != nil {
		return err
	}

	// the GOVERNED Claude Code hooks PEP (PreToolUse/PostToolUse) on its own
	// socket, if provisioned (OLIVARES_HOOK_PEP_CONFIG). It turns "observe" into
	// "govern": a managed hook posts each tool-call here and the engine returns
	// allow/deny/ask deny-closed (PDP + firm identity + HITL + audit).
	// Loopback-default; its security is fail-closed token verification + the
	// governed decision, not network isolation. nil when unset.
	hookPEPSrv, err := buildClaudeHookPEPServer(eng, log)
	if err != nil {
		return err
	}

	// SG-01: the same surface for Codex, on its OWN socket. Separate because the two
	// engines honor different answer shapes per event — one socket serving both would
	// mean a misconfigured hooks.json could be answered in a shape Codex silently ignores,
	// which is the failure this connector exists to prevent. nil when unset.
	codexPEPSrv, err := buildCodexHookPEPServer(eng, eng.sessionsMod, log)
	if err != nil {
		return err
	}

	// AGT-04: la misma superficie para Grok Build, en su PROPIO socket. Separada por la misma
	// razón que la de Codex y una más, medida contra el fuente de xAI: el valor del evento viaja
	// en snake_case y el veto sólo existe en `pre_tool_use`, así que una respuesta en la forma de
	// otro motor se ignoraría en silencio. nil cuando no está configurado.
	grokPEPSrv, err := buildGrokHookPEPServer(eng, eng.sessionsMod, log)
	if err != nil {
		return err
	}

	// the OPTIONAL, OPT-IN inline inference PEP proxy on its own loopback
	// socket, if provisioned (OLIVARES_INFERENCE_PROXY_CONFIG). It fronts
	// api.anthropic.com (the /v1/messages contract) and runs the governed pipeline
	// (residency → model-access → DLP/firewall/ceilings → sizing → budget → record) in-band
	// before forwarding with the OPERATOR's credential — the enforcement Anthropic's
	// managed-settings cannot reach for non-Claude-Code (raw SDK/curl) callers. It
	// DELIBERATELY interposes in the data-path (the inverse of read-first), so it is
	// opt-in and per-tenant fail-closed by default. nil when unset.
	proxySrv, err := buildClaudeMessagesProxyServer(eng, log)
	if err != nil {
		return err
	}

	// Stage-2: in the HA leader-routing layout EVERY replica is Ready, so every
	// one of these auxiliary sockets is dialable — and each of them is application
	// surface with real side effects (a governed hook decision, an A2A push, an
	// inference call). They are separate http.Servers with their own handlers, so
	// core/api's leader gate is not in their chain; wrap each one here. The API
	// listener is already gated inside core/api. Operational paths stay open (see
	// leaderOnlyHandler).
	if eng.haGate {
		for _, srv := range []*http.Server{hitlSrv, voiceWebhookSrv, gatewaySrv, hookPEPSrv, codexPEPSrv, grokPEPSrv, proxySrv} {
			if srv == nil || srv.Handler == nil {
				continue
			}
			srv.Handler = leaderOnlyHandler(srv.Handler, eng.store)
		}
		log.Info("ha: auxiliary listeners refuse application traffic on a standby (leader-routing layout)")
	}

	if !opts.insecure {
		for _, srv := range []*http.Server{httpSrv, hitlSrv, voiceWebhookSrv, gatewaySrv, hookPEPSrv, codexPEPSrv, grokPEPSrv, proxySrv} {
			if srv == nil {
				continue
			}
			if err := configureHTTPServerTLS(srv, tlsLoader); err != nil {
				return fmt.Errorf("configure TLS for HTTP listener %q: %w", srv.Addr, err)
			}
		}
	}

	nServers := 2
	if hitlSrv != nil {
		nServers++
	}
	if voiceWebhookSrv != nil {
		nServers++
	}
	if gatewaySrv != nil {
		nServers++
	}
	if hookPEPSrv != nil {
		nServers++
	}
	if codexPEPSrv != nil {
		nServers++
	}
	if grokPEPSrv != nil {
		nServers++
	}
	if proxySrv != nil {
		nServers++
	}
	errCh := make(chan error, nServers)
	go serveHTTP(httpSrv, opts.listen, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	go serveGRPC(grpcSrv, opts.grpcListen, opts.reusePort, errCh)
	if hitlSrv != nil {
		go serveHTTP(hitlSrv, hitlSrv.Addr, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	}
	if voiceWebhookSrv != nil {
		go serveHTTP(voiceWebhookSrv, voiceWebhookSrv.Addr, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	}
	if gatewaySrv != nil {
		go serveHTTP(gatewaySrv, gatewaySrv.Addr, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	}
	if codexPEPSrv != nil {
		go serveHTTP(codexPEPSrv, codexPEPSrv.Addr, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	}
	if grokPEPSrv != nil {
		go serveHTTP(grokPEPSrv, grokPEPSrv.Addr, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	}
	if hookPEPSrv != nil {
		go serveHTTP(hookPEPSrv, hookPEPSrv.Addr, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	}
	if proxySrv != nil {
		go serveHTTP(proxySrv, proxySrv.Addr, opts.insecure, opts.insecureAllowPublic, opts.reusePort, log, errCh)
	}

	// SIGHUP reconciles the durable source roster into the running engine —
	// the classic ops affordance (`kill -HUP <pid>`) alongside the authenticated
	// POST /v1/console/runtime/reload. Folds the license reconcile into the same
	// SIGHUP (a file-based `license install` + SIGHUP hot-applies, no restart). It
	// runs until the engine context is canceled and is OFF the SIGINT/SIGTERM
	// shutdown path (a reload must never stop serving).
	go watchReloadSignal(ctx, eng, log)
	// re-evaluate the live license on a ticker so the operator gets a WARN at
	// the moment it expires (the degradation half is observability; the seat policy
	// already enforces the expiry per call). OFF the shutdown path.
	go watchLicenseExpiry(ctx, eng, log)

	return waitAndShutdown(ctx, httpSrv, grpcSrv, hitlSrv, voiceWebhookSrv, gatewaySrv, hookPEPSrv, codexPEPSrv, grokPEPSrv, proxySrv, errCh, log)
}

// watchReloadSignal reconciles the source roster on each SIGHUP until ctx is done.
// The actor is a host-local operator (a signal carries no authenticated principal,
// unlike the API trigger), recorded as such in the reconcile log. Each reconcile
// is bounded so a stuck connector quiesce cannot wedge the handler.
func watchReloadSignal(ctx context.Context, eng *engine, log *slog.Logger) {
	if eng.sourceReconciler == nil && eng.licenseService == nil && eng.notifyDispatcher == nil {
		return
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	// The host's service manager, not a person: attributed as a system path so the
	// ledger can tell a SIGHUP reload from an operator's offline mutation, which
	// the previous anonymous "user:" subject could not (B-12).
	hostActor, haerr := auth.NewSystemOperator("sighup/host-operator", "SIGHUP reconfigure from the host service manager")
	if haerr != nil {
		log.Error("reconfigure: cannot attribute the host reload; SIGHUP handling disabled", "err", haerr)
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
			log.Info("reconfigure: SIGHUP received; reconciling the source roster and the license")
			rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			if eng.sourceReconciler != nil {
				report, err := eng.sourceReconciler.ReloadSources(rctx, hostActor)
				if err != nil {
					log.Warn("reconfigure: SIGHUP source reload failed", "err", err)
				} else {
					log.Info("reconfigure: SIGHUP source reload applied",
						"added", len(report.Added), "removed", len(report.Removed),
						"rotated", len(report.Rotated), "unchanged", report.Unchanged, "rejected", len(report.Rejected))
					for _, rej := range report.Rejected {
						log.Warn("reconfigure: source rejected on reload (deny-closed)", "name", rej.Name, "reason", rej.Reason)
					}
				}
			}
			// re-resolve the license by precedence and hot-apply it (the holder
			// logs any status transition). A file-installed license applies here.
			if eng.licenseService != nil {
				eng.licenseService.Reconcile(rctx)
			}
			// hot-reconcile EXTERNAL output destinations — re-read the operator
			// config and atomically add/reload/remove third-party output binaries whose
			// digest/config the operator edited (the source-roster affordance, for the
			// output side). In-process/first-party destinations are unaffected (they
			// apply on restart). The single ConnectorTrust root is re-read too, so a
			// rotated trust anchor takes effect. A config read error skips this pass.
			if eng.notifyDispatcher != nil && eng.secretResolver != nil {
				// Reconcile external output destinations ONLY when BOTH the destination
				// config AND the trust root re-read cleanly. A FAILURE TO READ the trust
				// root (a transient FS hiccup, a config being atomically rewritten mid-edit)
				// is NOT anchor removal: passing a nil root into the revocation pass would
				// tear down every live, already-verified destination. Fail static instead —
				// keep the running destinations and let the next clean SIGHUP apply changes.
				if specs, nerr := loadNotifyDestinations(log); nerr != nil {
					log.Warn("reconfigure: SIGHUP external notify reconcile skipped: cannot re-read OLIVARES_NOTIFY_CONFIG; keeping live destinations", "err", nerr)
				} else if srcCfg, serr := loadSourcesConfig(log); serr != nil {
					log.Warn("reconfigure: SIGHUP external notify reconcile skipped: cannot re-read the OLIVARES_SOURCES_CONFIG trust root (a read failure is not anchor revocation); keeping live destinations", "err", serr)
				} else {
					rep := eng.notifyDispatcher.reconcileExternal(rctx, specs, srcCfg.ConnectorTrust, eng.secretResolver, log)
					log.Info("reconfigure: SIGHUP external notify destinations reconciled",
						"added", rep.Added, "reloaded", rep.Reloaded, "removed", rep.Removed,
						"revoked", rep.Revoked, "unchanged", rep.Unchanged, "refused", rep.Refused)
				}
			}
			cancel()
		}
	}
}

// licenseMonitorInterval is how often serve re-evaluates the live license so the
// operator gets a WARN at (or just after) the moment it expires, rather than only at
// the next reload. The seat policy already enforces the expiry exactly (per call);
// this is the observability half. Cheap: a verify + a clock read, no DB, no I/O.
const licenseMonitorInterval = time.Hour

// watchLicenseExpiry re-evaluates the live license on a ticker until ctx is done,
// logging the valid→expired transition once (the degradation WARN, §3 point 4). It
// is OFF the SIGINT/SIGTERM shutdown path — a license check must never stop serving.
func watchLicenseExpiry(ctx context.Context, eng *engine, log *slog.Logger) {
	if eng.licenseService == nil {
		return
	}
	t := time.NewTicker(licenseMonitorInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			eng.licenseService.reEvaluate()
		}
	}
}

// consoleURL is the browser URL for the embedded console at the given listen
// address (https unless --insecure).
func consoleURL(listen string, insecure bool) string {
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	return scheme + "://" + listen
}

// announceSetup, on a fresh install (no users), mints and prints the one-time
// setup token to STDOUT ONLY (never the logs), pointing the operator at the
// embedded console's setup wizard (the API path is offered as the alternative).
func announceSetup(ctx context.Context, out io.Writer, eng *engine, baseURL string, insecure bool) error {
	has, err := eng.authr.HasAnyUser(ctx)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	token, created, err := eng.setupTok.Ensure()
	if err != nil {
		return err
	}
	// THE TRANSPORT SENTENCE MUST MATCH THE TRANSPORT (2026-08-06). This banner used to
	// state, unconditionally, that the console "serves HTTPS with a self-signed certificate
	// on first boot — your browser will warn once; that is expected". Under --insecure that
	// is false in every clause: TLS is off, no certificate is generated (EnsureTLSCert runs
	// only when NOT insecure), the URL two lines above already says http://, and the browser
	// warning it tells the operator to expect never appears. So the paragraph contradicted
	// the line above it and taught the reader to dismiss a warning they were not going to
	// get. Worse, it is silent about what --insecure actually costs HERE: the single-use
	// setup token printed below travels back to the engine in clear text on the wire.
	//
	// consoleURL already takes the posture; announceSetup did not, which is exactly how the
	// two halves of one screen came to disagree.
	transport := "The console serves HTTPS with a self-signed certificate on first boot — your\n" +
		"browser will warn once; that is expected. "
	if insecure {
		transport = "--insecure is ON: TLS is OFF and the console is served over PLAIN HTTP.\n" +
			"The setup token below travels in the clear — loopback development only.\n"
	}
	if created {
		fmt.Fprintf(out, "\n=== FIRST-BOOT SETUP ===\n"+
			"No accounts exist yet. Open the console and create the first administrator\n"+
			"with this one-time token — setup also creates your first organization and\n"+
			"makes that administrator its owner:\n\n"+
			"  Console:  %s\n"+
			"  Token:    %s\n\n"+
			"%s"+
			"The token is shown ONCE and is\n"+
			"single-use. Prefer the API? POST /v1/setup {\"token\":\"…\",\"email\":\"…\",\n"+
			"\"password\":\"…\"} — add \"organization\":\"…\" to name it (default: \"Default\n"+
			"Organization\"). The reply carries the new organization's tenant_id.\n"+
			"========================\n\n", baseURL, token, transport)
	} else {
		// A restart BEFORE setup was completed. `created` is false and the plaintext
		// is unrecoverable by design (only a hash is stored), so this branch used not
		// to exist at all: the operator got a silent boot with no accounts and no
		// stated way in. The engine does answer honestly over HTTP — setup_required
		// on /status and 409 setup_required elsewhere — but somebody running `serve`
		// in a terminal never sees that, and silence is the one thing an unfinished
		// setup must not be.
		fmt.Fprintf(out, "\n=== SETUP STILL PENDING ===\n"+
			"No accounts exist yet, and the one-time setup token for this data directory was\n"+
			"issued on an earlier boot. It is stored as a hash and CANNOT be shown again.\n\n"+
			"  Console:  %s\n\n"+
			"Complete setup with the token you were given. If it is lost, delete\n"+
			"%s and restart: a fresh token is minted on the next boot,\n"+
			"which is safe while no administrator exists — the token gates only first-boot\n"+
			"setup.\n"+
			"===========================\n\n",
			baseURL, filepath.Join(eng.dataDir, "setup.token"))
	}
	return nil
}

// pinAdvice is the sentence that turns two printed digests into an action, and it is
// shared BY CONSTRUCTION between the two boot paths because the defect it closes was
// having it in only one of them.
//
// THE GAP THIS CLOSES. Put the whole instruction in the `created` branch —
// the branch that runs ONCE in the life of a deployment. Every boot after it took the
// `else` and logged a bare "serving HTTPS" carrying cert_fingerprint_sha256 and
// pin_sha256 side by side with nothing saying which one a flag will take. That is the
// boot an operator actually reads: the first one scrolled past months ago, and the
// restart is where somebody goes looking for the value. Two digests and no verb is the
// same defect fixed, printed on the more common path.
//
// Keep the wording BYTE-IDENTICAL to what the documentation quotes: docs-site quotes
// this line verbatim inside a text fence, and check-engine-output-citations.mjs fails
// when the two drift. That is the point of the gate, not an obstacle to editing — edit
// both, and the gate tells you if you edited only one.
const pinAdvice = "clients must trust it, or pin it with " +
	"--pin-sha256=<pin_sha256> (that value, verbatim)"

// tlsTrustAttrs are the log attributes describing how a client can trust the
// certificate this engine is about to serve.
//
// THE DEFECT THIS CLOSES. The first-boot line said "clients must trust it or
// pin its fingerprint" and printed fingerprint_sha256 — hex(sha256(certificate)).
// The only pin flag the product has, --pin-sha256, decodes base64 and compares
// sha256(leaf SubjectPublicKeyInfo). Two digests, two objects, two encodings: the
// operator who did exactly what the line said was told their value was invalid, and
// nothing in the product said how to get the right one.
//
// So the value carrying the word "pin" IS the one the flag takes. The certificate
// fingerprint stays — it is genuinely useful, it is what a browser shows — under a
// name that no longer invites anyone to paste it into a flag that would reject it.
//
// A certificate we cannot parse must not stop the engine serving: the pin is
// omitted and the reason is logged in its place, which is honest and still lets the
// operator continue with --ca-cert or by trusting the certificate.
func tlsTrustAttrs(certPath, certFingerprint string) []any {
	attrs := []any{"cert", certPath, "cert_fingerprint_sha256", certFingerprint}
	pin, err := secure.SPKIPin(certPath)
	if err != nil {
		return append(attrs, "pin_sha256_error", err.Error())
	}
	return append(attrs, "pin_sha256", pin)
}

// newGRPCServer builds the gRPC server. It FAILS CLOSED: outside --insecure it
// returns an error rather than ever constructing a plaintext gRPC server (which
// would leak bearer tokens on the wire). The TLS cert is ensured up front by the
// caller, so the shared loader is ready here. When clientCA is non-empty the
// server requires and verifies a client certificate (true mTLS, docs/SECURITY-HARDENING.md/§3):
// only collectors with an operator-issued cert can connect, on top of the bearer
// token. Empty clientCA keeps server-only TLS for the localhost single-node case.
func newGRPCServer(eng *engine, loader *secure.CertificateLoader, clientCA string, insecure bool) (*grpc.Server, error) {
	if insecure {
		return eng.api.NewGRPCServer(), nil
	}
	tlsCfg, err := secure.ServerTLSConfigWithLoader(loader, clientCA)
	if err != nil {
		return nil, fmt.Errorf("gRPC TLS credentials: %w", err)
	}
	return eng.api.NewGRPCServer(grpc.Creds(credentials.NewTLS(tlsCfg))), nil
}

func serveHTTP(srv *http.Server, addr string, insecure, allowPublicBind, reusePort bool, log *slog.Logger, errCh chan<- error) {
	// The choke point. insecureBindGuard reads --listen and --grpc-listen, but the
	// SIX auxiliary listeners take their addresses from operator config files
	// (agentGatewayConfig.Listen and friends) and are served with the same global
	// --insecure switch — so loopback primaries let that guard pass while an
	// auxiliary socket served plain HTTP off-host (found by the Codex contrast of
	// 2026-08-06, F-01). Adding a seventh address to the guard's arguments would
	// fix today's six and miss the eighth listener someone adds next year.
	//
	// Every HTTP listener this file starts is created here — the primary server and
	// all six auxiliaries — so this is where the refusal belongs, and it runs
	// BEFORE any bind, because refusing after binding is not refusing.
	//
	// HONEST BOUND, stated precisely because a looser version of this sentence was
	// wrong twice. This is NOT "every plaintext listener in the process":
	//   - serveGRPC creates its own listener and does not pass through here. It is
	//     covered, but by the FLAG-level guard on --grpc-listen, which is the
	//     mechanism the paragraph above argues against relying on alone.
	//   - in-process source connectors open their own servers entirely outside both
	//     guards. THREE of them carry no loopback refusal and no allow_public_bind
	//     opt-in: github and gitlab call ListenAndServe directly
	//     (connectors/{github,gitlab}/gather.go) with WILDCARD defaults :9800/:9801,
	//     and connectors/tak serves plaintext CoT over TCP/UDP with 0.0.0.0 examples
	//     in its own field docs. --insecure governs none of them.
	// Those bypasses predate this guard and are reported separately; do not read
	// the line above as covering them.
	if insecure && !allowPublicBind && !hostIsLoopback(addr) {
		errCh <- fmt.Errorf("refusing to serve PLAINTEXT on %q, which is reachable off-host: with --insecure there is no TLS, so this listener's traffic (bearer tokens, governed decisions, the first-boot setup token) would cross the network in the clear. Bind it to loopback, drop --insecure, or — only if something in front of the engine terminates TLS — declare it with --insecure-allow-public-bind", addr)
		return
	}
	// With --reuse-port, bind through SO_REUSEPORT so a second instance can hold the
	// SAME address for a zero-downtime handover; otherwise use the standard
	// ListenAndServe path. srv.Addr == addr for every caller.
	var lis net.Listener
	if reusePort {
		if !serverhandover.Supported() {
			log.Warn("--reuse-port set but SO_REUSEPORT is unsupported here; using a plain listener (drain+restart, not overlap)", "addr", addr)
		}
		l, err := serverhandover.Listen(context.Background(), "tcp", addr)
		if err != nil {
			errCh <- fmt.Errorf("reuse-port listen %s: %w", addr, err)
			return
		}
		lis = l
	}
	if insecure {
		log.Warn("INSECURE MODE: serving plaintext HTTP — never expose beyond localhost", "addr", addr)
		if lis != nil {
			errCh <- srv.Serve(lis)
		} else {
			errCh <- srv.ListenAndServe()
		}
		return
	}
	// The shared reloadable GetCertificate callback was installed before any
	// listener starts. Empty file arguments make net/http use that callback.
	if lis != nil {
		errCh <- srv.ServeTLS(lis, "", "")
	} else {
		errCh <- srv.ListenAndServeTLS("", "")
	}
}

// hostIsLoopback reports whether addr (host:port, host, or :port) binds only the
// local host. A wildcard bind (empty host / 0.0.0.0 / ::) is NOT loopback.
// insecureBindGuard refuses the one combination that turns a development
// affordance into a production exposure: --insecure (TLS off) on a listener
// reachable from OFF-HOST. In that state the embedded console, every bearer
// token and — on a first boot — the single-use setup token cross the network in
// clear text. Until the only thing between that and the wire was a log line
// advising "never expose beyond localhost": measured on this tree, `serve
// --insecure --listen 0.0.0.0:19443` started, printed the setup token, and
// answered plain HTTP on a routable address.
//
// It does NOT remove --insecure, which is a legitimate development mode:
// loopback binds are untouched. A deployment that terminates TLS in front of the
// engine (ingress, service mesh, sidecar) is still possible — it just has to say
// so a SECOND time and by name. That is the shape the cooperative ingest already
// uses for the same question (connectors/claude `allow_public_bind`), and the
// sibling of the --seed-demo refusal above.
func insecureBindGuard(insecure, allowPublicBind bool, listen, grpcListen string) error {
	if !insecure || allowPublicBind {
		return nil
	}
	for _, l := range []struct{ flag, addr string }{
		{"--listen", listen},
		{"--grpc-listen", grpcListen},
	} {
		if hostIsLoopback(l.addr) {
			continue
		}
		return fmt.Errorf("--insecure serves PLAINTEXT and %s %q is reachable off-host: the console, every bearer token and the single-use first-boot setup token would cross the network in the clear. Bind loopback (127.0.0.1) for development, drop --insecure for a real install (TLS is the default), or — only if something in front of the engine terminates TLS — declare it with --insecure-allow-public-bind", l.flag, l.addr)
	}
	return nil
}

// asciiEqualFold reports whether s and t are equal under ASCII-ONLY case folding.
// It exists because strings.EqualFold folds Unicode, which is wrong for a
// security classifier comparing against a fixed ASCII name: it would accept
// spellings that are not that name (see hostIsLoopback). Comparing byte-wise also
// makes a multi-byte lookalike fail on length before anything else.
func asciiEqualFold(s, t string) bool {
	if len(s) != len(t) {
		return false
	}
	for i := 0; i < len(s); i++ {
		a, b := s[i], t[i]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func hostIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr // no port present
	}
	// ASCII fold, not strings.EqualFold. The name is a DNS label and so
	// case-insensitive — a `LOCALHOST:9000` in an operator config resolves to
	// 127.0.0.1 and must not be refused as public. But EqualFold applies UNICODE
	// simple folding, and U+017F (ſ, LATIN SMALL LETTER LONG S) is in the fold
	// orbit of `s`: EqualFold("localhoſt", "localhost") is TRUE. Measured, and
	// found independently by two reviewers. Since the plaintext refusal moved into
	// serveHTTP this classifier decides whether a listener may serve at all, so a
	// spelling that is not the name it checks for must not pass — Go's own net
	// package folds ASCII-only for exactly this reason
	// ($GOROOT/src/net/parse.go: "stringsEqualFold is strings.EqualFold, ASCII only").
	if asciiEqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func serveGRPC(srv *grpc.Server, addr string, reusePort bool, errCh chan<- error) {
	// With --reuse-port the gRPC listener ALSO binds via SO_REUSEPORT, so a second
	// instance can hold the same gRPC port during a zero-downtime handover — without
	// this the new process would abort with EADDRINUSE on the gRPC socket.
	var (
		lis net.Listener
		err error
	)
	if reusePort {
		lis, err = serverhandover.Listen(context.Background(), "tcp", addr)
	} else {
		lis, err = net.Listen("tcp", addr)
	}
	if err != nil {
		errCh <- err
		return
	}
	errCh <- srv.Serve(lis)
}

// buildHITLReceiverServer constructs the inbound HITL round-trip receiver's HTTP server
// from OLIVARES_HITL_CONFIG, backed by the in-process governed approval API (apiDecider
// over the engine's own handler — the full authenticate→tenant→authorize→audit chain).
// It returns nil when no usable provider is configured (an honest absence, not a
// silently-open surface). The server reuses the hardened timeouts of NewHTTPServer but
// serves ONLY the receiver routes (its own socket), not the API.
func buildHITLReceiverServer(eng *engine, tlsCert, tlsKey string, insecure bool, log *slog.Logger) (*http.Server, error) {
	cfg, err := loadHITLConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load HITL operator config: %w", err)
	}
	if len(cfg.Providers) == 0 {
		return nil, nil
	}
	rcv := newHITLReceiver(cfg, apiDecider{handler: eng.api.Handler()}, log)
	if rcv == nil {
		log.Warn("hitl: OLIVARES_HITL_CONFIG provisioned no usable provider; receiver not mounted")
		return nil, nil
	}
	addr := cfg.Listen
	if addr == "" {
		addr = defaultHITLListen
	}
	if !insecure && !hostIsLoopback(addr) {
		log.Warn("hitl: receiver bound to a NON-loopback address; front it with your ingress — its security is fail-closed signature verification, not network isolation", "addr", addr)
	}
	srv := eng.api.NewHTTPServer(addr)
	srv.Handler = rcv.handler()
	log.Info("hitl: inbound round-trip receiver mounted on its own socket", "addr", addr, "providers", len(cfg.Providers))
	return srv, nil
}

// waitAndShutdown blocks until a signal or a fatal listener error, then drains
// the HTTP server(s), gracefully stops gRPC, and closes the engine (store last).
func waitAndShutdown(ctx context.Context, httpSrv *http.Server, grpcSrv *grpc.Server, hitlSrv, voiceWebhookSrv, gatewaySrv, hookPEPSrv, codexPEPSrv, grokPEPSrv, proxySrv *http.Server, errCh <-chan error, log *slog.Logger) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
		log.Info("shutdown signal received; draining")
	case <-ctx.Done():
		log.Info("context canceled; draining")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
			return fmt.Errorf("listener failed: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	if hitlSrv != nil {
		if err := hitlSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("hitl receiver shutdown", "err", err)
		}
	}
	if voiceWebhookSrv != nil {
		if err := voiceWebhookSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("voice webhook receiver shutdown", "err", err)
		}
	}
	if gatewaySrv != nil {
		if err := gatewaySrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("agent-gateway shutdown", "err", err)
		}
	}
	if codexPEPSrv != nil {
		if err := codexPEPSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("codex hook PEP shutdown", "err", err)
		}
	}
	// ⚠ SÉPTIMO servidor auxiliar con trato IDÉNTICO —comprobar nil, apagar, avisar con su
	// etiqueta— y séptima vez que añadir un motor cuesta seis ediciones repartidas por este
	// fichero. La forma que lo cerraría es un variádico de {servidor, etiqueta}; no lo hago aquí
	// porque cambiar el orden de apagado mientras se añade una función es exactamente cómo se
	// cuela un fallo que nadie atribuye al refactor. Queda dicho para quien lo aborde.
	if grokPEPSrv != nil {
		if err := grokPEPSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("grok hook PEP shutdown", "err", err)
		}
	}
	if hookPEPSrv != nil {
		if err := hookPEPSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("hook-pep shutdown", "err", err)
		}
	}
	if proxySrv != nil {
		if err := proxySrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("inference-proxy shutdown", "err", err)
		}
	}
	grpcSrv.GracefulStop()
	return nil
}
