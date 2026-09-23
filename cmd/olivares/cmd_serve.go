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
	"github.com/olivaresai/olivares/core/webaddr"
	"github.com/olivaresai/olivares/core/webui"
)

// serveOptions are the resolved knobs for running the engine. Both `serve` and
// `quickstart` build one and hand it to runEngine, so the secure boot/serve path
// lives in exactly one place.
type serveOptions struct {
	loginProxies         loginProxyOptions
	listen, grpcListen   string
	dataDir, engine, dsn string
	adminDSN, ownerDSN   string
	region               string
	knownRegions         []string
	tlsCert, tlsKey, lic string
	grpcClientCA         string
	insecure, seedDemo   bool
	insecureAllowPublic  bool
	// publicURL is the operator-declared browser address of the console, and
	// publicURLSet is the FLAG'S PRESENCE — not its emptiness. The two are carried
	// apart so that `--public-url=` clears an OLIVARES_PUBLIC_URL set in the
	// environment file instead of silently falling through to it.
	publicURL    string
	publicURLSet bool
	// publicAddr carries an address a CALLER already resolved, and
	// publicAddrResolved says so. `quickstart governed-rag` resolves before it
	// writes any file, so runEngine must not read the setting a second time —
	// reading it twice is how a refusal came to be discarded on one of the paths.
	publicAddr         webaddr.Address
	publicAddrResolved bool
	// publicAddrSource travels with a pre-resolved address so this command never
	// has to infer the source after a flag already decided it.
	publicAddrSource      publicAddrSource
	allowPrivilegedDBRole bool
	reusePort             bool
	checkpointInterval    time.Duration
	// bindListener acquires each serve-family listener; nil means bindServeListener.
	// Private and test-only: no flag sets it and it selects no alternate transport.
	bindListener serveListenerBind
	// quiet holds the engine's own log back to errors for THIS run (quickstart's
	// --quiet). It changes the level and nothing else: the format is one format
	// (enginelog.go), because a flag that also changed the shape of every line is
	// what made one first hour produce two formats from one binary.
	quiet bool
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
			"or Postgres store, and prints one-time setup guidance on a first boot. It binds every\n" +
			"interface (:8443 and :8444) — this is a server; bind 127.0.0.1 to restrict it to this host.",
		Example: `  # Start the control plane on the default address: every interface, TLS on
  olivares serve --data-dir /var/lib/olivares

  # Restrict the console and the ingest API to this machine
  olivares serve --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444

  # Serve with TLS and Postgres backend
  olivares serve --engine postgres --dsn "file:/etc/olivares/secrets/db.dsn" \
    --tls-cert /etc/olivares/cert.pem --tls-key /etc/olivares/key.pem

  # Development mode (plaintext). Both binds must be loopback: --insecure is refused
  # off-host, and that refusal is what makes the wider default safe to ship.
  olivares serve --insecure --listen 127.0.0.1:8080 --grpc-listen 127.0.0.1:8081

  # Behind a reverse proxy: declare the address a browser uses
  olivares serve --public-url https://olivares.example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.publicURLSet = cmd.Flags().Changed("public-url")
			opts.loginProxies.set = cmd.Flags().Changed("login-trusted-proxies")
			announce := func(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress) error {
				if opts.seedDemo {
					if err := announceDemo(ctx, out, eng); err != nil {
						return err
					}
					if err := eng.api.RunStartupBackup(ctx, demoPassword, "demo estate startup backup", "demo-seed"); err != nil {
						eng.log.Warn("demo: startup backup failed; continuing serve", "err", err)
					}
					return nil
				}
				return announceSetup(ctx, out, eng, addr, opts.insecure)
			}
			return runEngine(cmd.Context(), cmd.OutOrStdout(), opts, announce)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", defaultHTTPListen, "HTTP (REST + web) listen address. The default "+defaultHTTPListen+" is EVERY interface (0.0.0.0 and, where the kernel has IPv6, ::) — this is a server. Bind 127.0.0.1:8443 to restrict it to this host")
	cmd.Flags().StringVar(&opts.publicURL, "public-url", "", publicURLFlagHelp)
	cmd.Flags().StringVar(&opts.loginProxies.value, "login-trusted-proxies", "", loginTrustedProxiesFlagHelp)
	cmd.Flags().StringVar(&opts.grpcListen, "grpc-listen", defaultGRPCListen, "gRPC listen address. The default "+defaultGRPCListen+" is EVERY interface, like --listen; bind 127.0.0.1:8444 to restrict it")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "data directory (default $OLIVARES_DATA_DIR, an existing ./olivares-data, else $XDG_DATA_HOME/olivares or ~/.local/share/olivares)")
	cmd.Flags().StringVar(&opts.engine, "engine", "sqlite", "store engine: sqlite or postgres")
	_ = cmd.RegisterFlagCompletionFunc("engine", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"sqlite", "postgres"}, cobra.ShellCompDirectiveNoFileComp
	})
	cmd.Flags().StringVar(&opts.dsn, "dsn", "", "store DSN (default a SQLite file in the data dir). May be a file:<path> or env:<VAR> reference resolved at boot, so the password stays out of the env file")
	cmd.Flags().StringVar(&opts.adminDSN, "admin-dsn", "", "Postgres: DSN of a dedicated NOSUPERUSER BYPASSRLS read role for first setup and cross-tenant operations (org listing, checkpoints, DR backup). Provision with olivares db init --admin-role; see deploy/postgres/README.md. Keep the app role NOSUPERUSER NOBYPASSRLS; never use a superuser here")
	cmd.Flags().StringVar(&opts.ownerDSN, "owner-dsn", "", "Postgres only: DSN of the owner role that owns the schema and runs DDL/migrations. Set it to a SEPARATE NOSUPERUSER NOBYPASSRLS role to make --dsn a least-privilege non-owner app role with only DML grants (provision both with 'olivares db init'). Empty = the --dsn role owns the schema (single-role). Accepts a file:/env: reference like --dsn")
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
// provisioned side servers), TLS-on-by-default. It prepares every server and
// acquires every present serve listener first, then calls announce once to print
// the first-run guidance for the caller's mode (serve setup-token, demo, or
// quickstart), and only after that announcement was accepted by its writer does it
// start serving. A preparation or bind failure therefore mints no new setup token.
// This is the single secure boot/serve path shared by `serve` and `quickstart`.
func runEngine(ctx context.Context, out io.Writer, opts serveOptions, announce func(context.Context, io.Writer, *engine, consoleAddress) error) error {
	// ONE format, installed before the first line (enginelog.go): logfmt with the
	// timestamp in UTC, at the level the operator asked for. Every engine start goes
	// through here, so `serve` and `quickstart` cannot disagree about the shape of a
	// log line — they did until 2026-09-18, and --quiet was what changed it.
	log := installEngineLogger(os.Stderr, osGetenv, opts.quiet)
	loginProxies, err := opts.loginProxies.resolve(osGetenv)
	if err != nil {
		return err
	}

	// The declared browser address is resolved and validated before the
	// demo guard, before the bind guard, and above all before boot() creates a
	// data directory or mints a key. A refused value is a configuration error the
	// operator fixes in a file, and making them clean up a half-created
	// installation first would be gratuitous. Parsed once here and carried as a
	// typed value: nothing downstream re-parses a string.
	publicAddr, publicSource := opts.publicAddr, opts.publicAddrSource
	if !opts.publicAddrResolved {
		var err error
		publicAddr, publicSource, err = resolvePublicAddr(opts.publicURL, opts.publicURLSet, osGetenv)
		if err != nil {
			return err
		}
	}
	if !publicAddr.IsZero() {
		log.Info("console: public address declared", "source", publicSource.String(), "url", publicAddr.Origin)
	}
	consoleAddr := resolveConsoleAddress(publicAddr, opts.listen, opts.insecure)

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
		ServeMode:           true, // long-lived server: OK to run the background update-check
		TLSCertNotAfter:     tlsCertNotAfter,
		PublicAddr:          publicAddr,
		PublicAddrSource:    publicSource,
		LoginTrustedProxies: loginProxies,
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
	// editionWebFS lets the commercial build serve its own console bundle (the cockpit's
	// terminal) without the public dist moving: the default build returns base unchanged
	// (COCKPIT-07 §4).
	httpSrv.Handler = withEnterpriseHTTP(newSPAHandler(eng.api.Handler(), editionWebFS(webui.FS())), eng, log)
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
	// The HTTP servers in launch order: the primary, then each present auxiliary. The
	// gRPC listener is acquired second, between the primary and the auxiliaries.
	var auxHTTP []*http.Server
	for _, srv := range []*http.Server{hitlSrv, voiceWebhookSrv, gatewaySrv, codexPEPSrv, grokPEPSrv, hookPEPSrv, proxySrv} {
		if srv != nil {
			auxHTTP = append(auxHTTP, srv)
		}
	}

	// Plaintext off-host refusal for every HTTP listener, before any socket exists.
	for _, srv := range append([]*http.Server{httpSrv}, auxHTTP...) {
		if err := plaintextBindRefusal(srv.Addr, opts.insecure, opts.insecureAllowPublic); err != nil {
			return fmt.Errorf("listener failed: %w", err)
		}
	}

	// Acquire every serve listener synchronously; no Serve runs here.
	specs := make([]serveListenerSpec, 0, nServers)
	specs = append(specs,
		serveListenerSpec{addr: httpBindAddr(httpSrv.Addr, opts.insecure, opts.reusePort), http: true},
		serveListenerSpec{addr: opts.grpcListen},
	)
	for _, srv := range auxHTTP {
		specs = append(specs, serveListenerSpec{addr: httpBindAddr(srv.Addr, opts.insecure, opts.reusePort), http: true})
	}
	bind := opts.bindListener
	if bind == nil {
		bind = bindServeListener
	}
	owned, err := acquireServeListeners(ctx, bind, specs, opts.reusePort, log)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return withCloseErrors(fmt.Errorf("serve startup canceled after binding, before the announcement: %w", err), owned.closeAll())
	}

	// The advice is built HERE and not at parse time, because it describes what a
	// passkey prompt will do and that is only known once boot has resolved the
	// relying party. The announcement runs only once every listener is held, so a
	// bind failure can no longer follow a freshly minted setup token. A token that
	// may have been partly delivered is kept: the pending-setup banner is the recovery.
	resolved := consoleAddr.withPlan(eng.webAuthn)
	if err := runAnnouncement(ctx, out, eng, resolved, announce); err != nil {
		return withCloseErrors(err, owned.closeAll())
	}
	// The banner goes to a terminal somebody may not be reading — in Compose it
	// goes to a log that rotates. The same answer is left in the data directory so
	// `olivares first-boot` can repeat it later, from a second process in the same
	// container, with no shell and no credential (consolestate.go). A failure here
	// is logged and never fatal: the engine is up and the banner was delivered.
	if err := writeConsoleState(eng.dataDir, newConsoleState(resolved, opts.listen, opts.grpcListen, opts.insecure)); err != nil {
		log.Warn("could not record the console address for `olivares first-boot`; the engine is unaffected", "err", err)
	}
	// A callback may cancel and still return nil. This is an observation before
	// launch, not an atomic stop/admission barrier.
	if err := ctx.Err(); err != nil {
		return withCloseErrors(fmt.Errorf("serve startup canceled after the announcement, before serving: %w", err), owned.closeAll())
	}

	errCh := make(chan error, nServers)
	go serveHTTP(httpSrv, owned.listeners[0], opts.insecure, log, errCh)
	go serveGRPC(grpcSrv, owned.listeners[1], errCh)
	for i, srv := range auxHTTP {
		go serveHTTP(srv, owned.listeners[2+i], opts.insecure, log, errCh)
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

	// V269 / COCKPIT-03 §3: auxiliary listeners this edition serves. Empty in every
	// build today; the seam and its shutdown path land before the engine so the wiring
	// is already final when it arrives.
	auxServers := editionAgentServers()
	serveEditionAuxServers(ctx, auxServers, log, errCh)
	defer shutdownEditionAuxServers(context.Background(), auxServers, log)

	// Owner fallback after the unchanged DS1 drain: a Serve goroutine that never
	// entered still holds its listener. It runs before the checkpointer and store
	// close. Closed sockets are not a goroutine join.
	shutdownErr := waitAndShutdown(ctx, httpSrv, grpcSrv, hitlSrv, voiceWebhookSrv, gatewaySrv, hookPEPSrv, codexPEPSrv, grokPEPSrv, proxySrv, errCh, log)
	return withCloseErrors(shutdownErr, owned.closeAll())
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

// publicURLFlagHelp is shared by `serve`, `quickstart` and `quickstart
// governed-rag` so the three cannot describe the same field differently. It
// states the precedence and the restart requirement, because both are things an
// operator can only learn from here.
const publicURLFlagHelp = "the address a browser reaches this console at, as scheme://host[:port] " +
	"(e.g. https://olivares.example.com). It is what the startup panel prints and what the WebAuthn " +
	"relying party is derived from, and it is independent of --listen: declare it when the engine sits " +
	"behind a reverse proxy, binds a wildcard, or is reached by a name that is not the bind. " +
	"Defaults to $OLIVARES_PUBLIC_URL; passing the flag wins over the environment, and passing it EMPTY " +
	"clears it. Start-time only: a change takes a restart"

// announceSetup, on a fresh install (no users), mints and prints the one-time
// setup token to STDOUT ONLY (never the logs), pointing the operator at the
// embedded console's setup wizard (the API path is offered as the alternative).
func announceSetup(ctx context.Context, out io.Writer, eng *engine, addr consoleAddress, insecure bool) error {
	baseURL := addr.URL()
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
	// THE JOINER IS NOT PART OF THE SENTENCE, and separating them is not tidiness.
	// These literals used to END in the whitespace that runs them into the token
	// paragraph — a trailing space here, a trailing newline under --insecure — so
	// the registered quotation in check-engine-output-citations.mjs ended in a
	// space too, and every documented fence had to carry an invisible trailing
	// space to stay anchored. The moment the address advice broke the line there,
	// fourteen pages stopped matching a quotation whose last character nobody can
	// see. The sentence ends at its full stop; the joiner is chosen below.
	transport := "The console serves HTTPS with a self-signed certificate on first boot — your\n" +
		"browser will warn once; that is expected."
	join := " "
	if insecure {
		transport = "--insecure is ON: TLS is OFF and the console is served over PLAIN HTTP.\n" +
			"The setup token below travels in the clear — loopback development only."
		join = "\n"
	}
	// The address advice goes HERE, in the transport slot: after the Token line and
	// inside the closing marker. Both halves of that placement are load-bearing.
	// The documented way to read this banner is a RANGE — `sed -n '/FIRST-BOOT
	// SETUP/,/========================/p'` in install-service.sh and in seven
	// tutorial pages — so anything printed after the closing line is outside every
	// capture command the product itself publishes. And sixty documented `grep
	// -A<n>` commands count lines from the header to `Token:`, so the advice must
	// go after Token, not before it.
	transport = withAddressAdvice(transport, join, addr.Advice)
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

// waitAndShutdown blocks until a signal, a canceled context or a fatal listener
// error, then drains the HTTP server(s) it was given and gracefully stops gRPC.
// It owns transports only: closing the engine and its store belongs to its caller.
// A fatal listener error does NOT skip that drain — the wrapped cause is retained,
// the same sequence runs, and that cause is returned once the sequence completes.
func waitAndShutdown(ctx context.Context, httpSrv *http.Server, grpcSrv *grpc.Server, hitlSrv, voiceWebhookSrv, gatewaySrv, hookPEPSrv, codexPEPSrv, grokPEPSrv, proxySrv *http.Server, errCh <-chan error, log *slog.Logger) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// The fatal listener cause, if any. It is retained here and returned at the end:
	// returning it from the select would leave every transport below still admitting.
	var fatalErr error

	select {
	case <-sigCh:
		log.Info("shutdown signal received; draining")
	case <-ctx.Done():
		log.Info("context canceled; draining")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
			fatalErr = fmt.Errorf("listener failed: %w", err)
			// A constant line, like the other two arms: the caller already receives
			// the cause itself, so the log adds no error payload to repeat it.
			log.Error("listener failed; draining")
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
	return fatalErr
}

// withAddressAdvice folds the address paragraphs into a banner's transport slot.
//
// join is the whitespace that runs the transport sentence into the paragraph that
// follows it when there is nothing to say — a space with TLS on, a newline under
// --insecure. With nothing to say the banner is byte-for-byte the one an operator
// saw before this change; with something to say the advice becomes its own
// paragraph and the joiner is not used, because a sentence followed by a blank
// line does not need one.
func withAddressAdvice(transport, join, advice string) string {
	if advice == "" {
		return transport + join
	}
	return transport + "\n\n" + advice + "\n\n"
}
