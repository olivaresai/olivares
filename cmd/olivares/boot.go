// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/api/ratelimit/pgstore"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
	"github.com/olivaresai/olivares/core/updatecheck"
	"github.com/olivaresai/olivares/modules/knowledge"
	securitymodule "github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
)

const (
	// logBrokerRingSize bounds the in-memory engine-log history exposed to the console.
	logBrokerRingSize                  = 2000
	envLogLevel                        = "OLIVARES_LOG_LEVEL"
	envCommunicationContentKeyringFile = "OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE"
)

type sessionRuntimeCredentialRecoverer interface {
	CommunicationSessionCredentialsEnabled() bool
	RecoverRuntimeCredentials(context.Context, model.TenantID) error
}

type sessionOrgLister func(context.Context) ([]model.Org, error)

// pdpTenantLister is deliberately narrow so boot can prove its all-estate Cedar
// reload inventory before it starts serving. A failed authoritative enumeration is
// different from a per-tenant reload failure: in the former case there is no way to
// install an unavailable sentinel for every unknown tenant, so boot must stop.
type pdpTenantLister func(context.Context) ([]model.Org, error)

// preparePDPReloadTenants turns the authoritative org inventory into reload work.
// It is deliberately pure: Leader.Run owns closing the store when initial boot fails,
// while an OnPromote failure merely rejects one acquisition and must leave the follower
// alive for the elector to retry.
// orgVisibilityLister is the one method pdpVisibleTenantInventory needs, named
// separately so the tolerance below can be tested without standing up a store.
type orgVisibilityLister interface {
	ListOrgsVisible(ctx context.Context) ([]model.Org, bool, error)
}

// pdpVisibleTenantInventory reads the tenant inventory for the promotion's Cedar
// reactivation, TOLERATING an RLS-limited one and saying so out loud.
//
// ⛔ Y ES `ListOrgsVisible`, NO `ListOrgs`, POR DECISION ESCRITA — que es la unica
// forma en que store.go:328-338 permite tolerar un censo parcial.
//
// Con `ListOrgs` un despliegue de Postgres SIN `--admin-dsn` no llegaba a promover:
// la lectura devuelve ErrEnumerationNotAuthoritative y el error subia hasta abortar
// la eleccion de lider. Medido el 2026-08-24 en `e2e-operator-kind` (job 97315647128,
// 8 de 8 corridas rojas):
//
//	Error: leader election / provision system tenant: leader-election: promotion
//	bootstrap failed (staying follower): pdp: cannot establish a complete tenant
//	inventory for authored Cedar reactivation: ... engine "postgres" holds no
//	BYPASSRLS admin pool ...
//
// Y eso CONTRADICE lo que el despliegue promete: `deploy/postgres/01-app-role.sql:69-72`
// dice que ese rol se puede omitir en un despliegue de un solo tenant y que «the engine
// then LOGS that cross-tenant reads are RLS-limited». Logs, no se niega a arrancar.
//
// La tolerancia es correcta aqui y no es un relajamiento, por tres razones que ya
// estaban en el arbol antes que yo:
//
//  1. El trabajo es POR TENANT y best-effort: reloadPDPForPromotion ya trata el fallo
//     de UN tenant con un Warn, no abortando (boot.go:113-120). Un tenant que este pool
//     no ve tiene exactamente la misma consecuencia que uno cuya recarga fallo.
//  2. Un censo parcial NO abre nada. El tenant que no se reactiva se queda con sus
//     costuras de politica CERRADAS —lo dice el propio Warn de esa funcion—, asi que
//     tolerar el censo falla cerrado, no abierto.
//  3. store.go:329-337 describe este caso con estas palabras: callers «legitimately
//     per-tenant and best-effort … where refusing to boot over it would be the worse
//     failure». Es este.
//
// Lo que NO se tolera sigue sin tolerarse: un error de lectura de verdad sube, y una
// fila de negocio malformada sigue abortando en preparePDPReloadTenants.
func pdpVisibleTenantInventory(ctx context.Context, sys orgVisibilityLister, log *slog.Logger) ([]model.Org, error) {
	orgs, authoritative, err := sys.ListOrgsVisible(ctx)
	if err != nil {
		return nil, err
	}
	if !authoritative && log != nil {
		log.Warn("pdp: the tenant inventory for authored Cedar reactivation is RLS-limited, so this promotion reactivates only the tenants this pool can see; any tenant outside it keeps its policy seams CLOSED until a boot that can enumerate it",
			"tenants_visible", len(orgs),
			"remedy", "provision a NOSUPERUSER BYPASSRLS role (deploy/postgres/01-app-role.sql) and pass --admin-dsn")
	}
	return orgs, nil
}

func preparePDPReloadTenants(ctx context.Context, listOrgs pdpTenantLister) ([]model.TenantID, error) {
	orgs, err := listOrgs(ctx)
	if err != nil {
		return nil, fmt.Errorf("enumerate tenants for authored Cedar reactivation: %w", err)
	}
	tenants := make([]model.TenantID, 0, len(orgs))
	for _, org := range orgs {
		tenant, parseErr := model.ParseTenantID(org.ID.String())
		// A malformed business row makes this inventory incomplete. Skipping it
		// would leave that unknown tenant with no unavailable sentinel.
		if parseErr != nil || tenant.IsZero() {
			return nil, fmt.Errorf("authored Cedar reactivation inventory contains invalid tenant id %q", org.ID)
		}
		if org.TenantID != tenant {
			return nil, fmt.Errorf("authored Cedar reactivation inventory has org id %q with mismatched tenant id %q", org.ID, org.TenantID)
		}
		// The system tenant deliberately has no tenant-scoped Cedar runtime.
		if tenant.IsSystem() {
			continue
		}
		tenants = append(tenants, tenant)
	}
	return tenants, nil
}

// reloadPDPForPromotion establishes each known tenant's Cedar runtime before the
// elector makes this process visible as leader. Inventory is all-or-nothing: without a
// complete list, unknown tenants could retain an old or absent runtime, so the promotion
// must fail. A *known* tenant's ReloadActivePDP failure is different: C3 installs its
// unavailable sentinel, so we log and continue the promotion while that tenant's policy
// seams remain fail-closed. This helper never closes the store; callers use its error to
// distinguish an initial boot (which closes) from a retryable promotion failure.
func reloadPDPForPromotion(
	ctx context.Context,
	listOrgs pdpTenantLister,
	reload func(context.Context, model.TenantID) error,
	log *slog.Logger,
) error {
	tenants, err := preparePDPReloadTenants(ctx, listOrgs)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if err := reload(ctx, tenant); err != nil {
			if log != nil {
				log.Warn("pdp: could not re-activate stored Cedar policy during promotion; tenant runtime is unavailable and policy seams fail closed until a coherent reload",
					"tenant", tenant.String(), "error", err)
			}
		}
	}
	return nil
}

// recoverSessionRuntimeCredentialsForPromotion is the fail-closed promotion
// barrier shared by initial acquisition and every HA failover. K3 OFF returns
// before the authoritative cross-tenant ceremony, which keeps staged rollouts
// boot-compatible. Once enabled, enumeration and every local-tenant recovery
// must complete before the elector may publish leadership.
func recoverSessionRuntimeCredentialsForPromotion(
	ctx context.Context,
	listOrgs sessionOrgLister,
	residencyReg *residency.Registry,
	recoverer sessionRuntimeCredentialRecoverer,
) error {
	if recoverer == nil || !recoverer.CommunicationSessionCredentialsEnabled() {
		return nil
	}
	orgs, err := listOrgs(ctx)
	if err != nil {
		return fmt.Errorf("enumerate tenants for session runtime credential recovery: %w", err)
	}
	type tenantOrg struct {
		tenant model.TenantID
		org    model.Org
	}
	validated := make([]tenantOrg, 0, len(orgs))
	// Validate the complete authoritative inventory before the first recovery
	// side effect. Skipping a malformed business org would publish leadership
	// after only a partial ceremony; recovering earlier rows before discovering
	// it would make even the failed attempt externally partial.
	for _, org := range orgs {
		tenant, parseErr := model.ParseTenantID(org.ID.String())
		if parseErr != nil || tenant.IsZero() {
			return fmt.Errorf(
				"session runtime credential recovery inventory contains an invalid tenant id %q: %w",
				org.ID, errors.Join(parseErr, store.ErrEnumerationNotAuthoritative),
			)
		}
		if tenant.IsSystem() {
			continue
		}
		validated = append(validated, tenantOrg{tenant: tenant, org: org})
	}
	var recoveryErrs []error
	for _, item := range validated {
		if residencyReg != nil && !residencyReg.Serves(item.org.DataRegion) {
			continue
		}
		if err := recoverer.RecoverRuntimeCredentials(ctx, item.tenant); err != nil {
			recoveryErrs = append(recoveryErrs, fmt.Errorf(
				"tenant %s session runtime credential recovery: %w", item.tenant, err,
			))
		}
	}
	return errors.Join(recoveryErrs...)
}

func loadLogCaptureLevel(getenv func(string) string, log *slog.Logger) *slog.LevelVar {
	level := &slog.LevelVar{} // zero value is INFO, the documented default.
	raw := strings.TrimSpace(getenv(envLogLevel))
	switch strings.ToLower(raw) {
	case "", "info":
		level.Set(slog.LevelInfo)
	case "debug":
		level.Set(slog.LevelDebug)
	case "warn":
		level.Set(slog.LevelWarn)
	case "error":
		level.Set(slog.LevelError)
	default:
		if log != nil {
			log.Warn("invalid OLIVARES_LOG_LEVEL; using info", "value", raw)
		}
		level.Set(slog.LevelInfo)
	}
	return level
}

// bootConfig holds the engine boot parameters from flags/env.
type bootConfig struct {
	DataDir string
	Engine  string
	DSN     string
	// AdminDSN (Postgres only) is the dedicated BYPASSRLS role used for
	// cross-tenant System reads; empty means those reads are RLS-limited.
	AdminDSN string
	// OwnerDSN (Postgres only) is the owner-role URL used for DDL/migrations. Empty
	// falls back to DSN (single-role setup). When set to a SEPARATE role, that role
	// owns the schema and runs every migration, so the app role (DSN) can be a
	// non-owner holding only DML grants — the least-privilege split deploy/postgres/
	// 01-app-role.sql documents and `olivares db init` provisions.
	OwnerDSN    string
	LicenseFile string
	Version     string
	Logger      *slog.Logger
	// DemoSeed loads a synthetic sample estate through the real bus (a demo source
	// registered before Start). Off by default; see demo.go.
	DemoSeed bool
	// AllowPrivilegedDBRole opts out of the Postgres RLS-bypass boot guard
	// (docs/SECURITY-HARDENING.md). Off by default: the store refuses to open against a
	// superuser/BYPASSRLS role. Only set on a single-tenant/throwaway deployment.
	AllowPrivilegedDBRole bool
	// Region is this instance's data-residency HOME region. When set, the
	// instance is region-scoped: it serves only tenants pinned to this region and
	// denies cross-region access fail-closed. Empty = single-region (no enforcement).
	Region string
	// KnownRegions is the set of region codes valid across the whole deployment
	// (the home region is added implicitly). A tenant pin must be one of these.
	KnownRegions []string
	// ServeMode is true only for the long-lived `serve`/`quickstart` path. Other
	// commands that reach boot() (audit, dr) get a short-lived engine and must NOT
	// start the background update-check goroutine (no surprise outbound calls, no
	// goroutine that outlives the command)..
	ServeMode bool
	// pdpListOrgs is a test-only seam for the serve-time Cedar reactivation
	// inventory. Production leaves it nil and uses the authoritative System scope.
	pdpListOrgs func(context.Context, store.Store) ([]model.Org, error)
	// pdpPromotionRegistered observes the exact callback registered with the
	// leader elector. It is test-only: the boot focal invokes that callback again
	// to prove later promotions rerun Cedar reactivation and that an inventory
	// failure remains retryable without closing the follower store.
	pdpPromotionRegistered func(func(context.Context) error)
	// pdpReload is a test-only replacement for ReloadActivePDP. It lets the boot
	// focal prove the exact OnPromote callback reaches a G→G+1 reload before
	// visibility, without widening governance's production API.
	pdpReload func(context.Context, model.TenantID) error
	// ReadOnly marks a boot that must not MANUFACTURE the installation it was
	// asked to read. A read-only boot creates no data directory, mints no
	// signing key and creates no store file: if there is nothing at the resolved
	// data dir, it says so (NotFound) instead of building one.
	//
	// Before this, `olivares sources ls` — a listing whose entire output is "no
	// sources in the roster" — left three private keys and a 6 MB SQLite file in
	// ./olivares-data, in whatever directory it was run from, because the default
	// data dir is RELATIVE (defaultDataDir) and boot() creates what it does not
	// find. That is a listing command silently installing a product.
	ReadOnly bool
	// NoImplicitInstall refuses to CREATE an installation at a data-dir path
	// nobody named. It is set by the mutating CLI verbs, and never by
	// `serve`/`quickstart`/`setup`, whose whole job is to initialize one.
	NoImplicitInstall bool
	// NoIngest boots WITHOUT starting the runtime and WITHOUT the initial source
	// reconcile, for a command that only needs to read the roster.
	//
	// ReadOnly does not cover this and never claimed to: it stops the boot
	// MANUFACTURING an installation, and everything after that runs as usual —
	// including rt.Start and the reconcile, which PREPARE, OPEN and WIRE every
	// enabled connector in the roster. So `olivares sources ls` against a
	// twelve-source deployment dialed all twelve to print a table, and the
	// preview verbs, whose entire promise is "this changes nothing", inherited that.
	// Measured by the sol-max contrast: a `sources plan` run logged `rejected=1`
	// from a real apply attempt against a persisted source.
	//
	// It is deliberately NARROW. It does not make the boot read-only — the store
	// still opens and migrates, leadership still bootstraps, and absent sealer keys
	// are still created. Those are named in the session record as a defect
	// this unit did not close, because closing them is a different boot path and
	// not a flag.
	NoIngest bool
	// TLSCertNotAfter reads the certificate currently served by the listener.
	// The serve command supplies the live reload-aware accessor; non-listener
	// commands leave it nil.
	TLSCertNotAfter func() (time.Time, bool)
}

// keyLoadOptions translates the boot's read-only stance into the signing-key
// loaders' vocabulary, so the two cannot drift apart.
func (c bootConfig) keyLoadOptions() []keyLoadOption {
	if c.ReadOnly {
		return []keyLoadOption{withoutMinting()}
	}
	return nil
}

// dirExistsAt reports whether path names an existing directory.
func dirExistsAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// installationExistsAt reports whether dir holds an Olivares installation, which
// is a different question from whether the directory exists: an empty
// ./olivares-data/ is not an installation, and treating it as one let a mutating
// verb mint key material inside it.
//
// The evidence is the store or any signing key — the artifacts `quickstart`
// creates. A directory holding neither has nothing this engine put there.
func installationExistsAt(dir string) bool {
	if !dirExistsAt(dir) {
		return false
	}
	// The evidence set is every artifact THIS ENGINE puts in a data directory, not
	// just the SQLite ones. The narrower list missed a supported, real installation:
	// a Postgres deployment whose three signing keys are under external custody
	// (BYOK/CMEK, auditkey.go) has no olivares.db and no *-signing.key, yet holds
	// tls.key, the sealed secret stores and its license. Counting it as "nothing
	// here" made compatibility branch walk away from those keys and mint a
	// second installation elsewhere — silently unreadable sealed secrets and a new
	// certificate. Found by the sol-max contrast.
	for _, name := range []string{
		"olivares.db", "audit-signing.key", "catalog-signing.key", "policy-signing.key",
		"tls.key", "secret-store.key", "eventing-secret.key", "sso-secret.key",
		"setup.token", licenseFileName, "install-id",
	} {
		if fileExistsAt(filepath.Join(dir, name)) {
			return true
		}
	}
	return false
}

// absOrSame renders path absolute for a message, falling back to the input when
// the working directory cannot be resolved — a diagnostic must never fail.
func absOrSame(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// requireDataDir reports an absent or non-directory data dir as NotFound. It is
// the read-only counterpart of secure.EnsureDir: same question, opposite answer
// when the directory is not there.
func requireDataDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case err != nil:
		abs := dir
		if a, aerr := filepath.Abs(dir); aerr == nil {
			abs = a
		}
		return exitcode.New(exitcode.NotFound, fmt.Errorf(
			"no data directory at %s: a read-only command never creates one — run "+
				"`olivares quickstart` to initialize it, or point --data-dir (or "+
				"OLIVARES_DATA_DIR) at an existing installation", abs))
	case !info.IsDir():
		return exitcode.New(exitcode.NotFound, fmt.Errorf("data directory %s is not a directory", dir))
	}
	return nil
}

// newConnectorScratchDir creates the private directory that holds first-party
// connector executables for the lifetime of one engine boot.
//
// TMPDIR is an explicit operator choice and wins when it is non-empty. Without
// that choice, extracted executables belong beside the installation under
// <data-dir>/tmp, rather than on the host's default /tmp mount (commonly noexec
// on hardened hosts). The system temporary directory is only the last resort
// when the data directory cannot provide writable scratch space.
func newConnectorScratchDir(dataDir string) (string, error) {
	if tmpDir := os.Getenv("TMPDIR"); tmpDir != "" {
		return makePrivateConnectorScratch(tmpDir, "olivares-connectors-")
	}

	dataTmp := filepath.Join(dataDir, "tmp")
	info, dataErr := os.Stat(dataDir)
	if dataErr == nil && !info.IsDir() {
		dataErr = fmt.Errorf("data directory %q is not a directory", dataDir)
	}
	if dataErr == nil {
		dataErr = os.MkdirAll(dataTmp, 0o700)
	}
	if dataErr == nil {
		// MkdirAll preserves the mode of an existing directory. Reassert the
		// private mode instead of depending on its history or the process umask.
		dataErr = os.Chmod(dataTmp, 0o700)
	}
	if dataErr == nil {
		var dir string
		dir, dataErr = makePrivateConnectorScratch(dataTmp, "connectors-")
		if dataErr == nil {
			return dir, nil
		}
	}

	// Reaching here means an actual create/chmod/write attempt below data-dir
	// failed. Only then retain the historical os.MkdirTemp("", ...) fallback.
	dir, fallbackErr := makePrivateConnectorScratch("", "olivares-connectors-")
	if fallbackErr != nil {
		return "", fmt.Errorf(
			"connector scratch under data directory %q: %v; system temporary fallback: %w",
			dataTmp, dataErr, fallbackErr,
		)
	}
	return dir, nil
}

func makePrivateConnectorScratch(root, pattern string) (string, error) {
	dir, err := os.MkdirTemp(root, pattern)
	if err != nil {
		return "", err
	}
	// MkdirTemp currently creates 0700, but this is a security invariant, not an
	// implementation detail to inherit. firstparty.Extract may later widen it to
	// 0711 when a root engine must launch the binary under plugjail's other uid.
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// engine bundles the wired subsystems and tears them down in order on Close.
type engine struct {
	store    store.Store
	rt       *runtime.Runtime
	signer   *audit.Signer
	authr    *auth.Authenticator
	authz    *auth.Authorizer
	setupTok *secure.SetupToken
	api      *api.Server
	tracer   *obstrace.Provider
	dataDir  string
	log      *slog.Logger
	// secretStore is the runtime secret store, exposed so the `secrets` CLI
	// can do sealed CRUD over the same store (and sealer) the engine resolves
	// `store:<name>` references through.
	secretStore *auth.SecretStore
	// sourceStore is the durable source roster (store CRUD), exposed so the
	// `sources` CLI authors the same rows the live reconciler reconciles.
	sourceStore *auth.SourceStore
	// sourceReconciler is the live reconfiguration engine: the SIGHUP handler
	// and the console/CLI drive it to add/remove/rotate connectors without a
	// restart. It holds the runtime, so it lives on the engine (not reachable from
	// a module).
	sourceReconciler *sourceReconciler
	// licenseService is the live edition/license surface: the SIGHUP handler +
	// the runtime/reload endpoint reconcile it (hot-apply a file-installed license)
	// and the serve-time expiry monitor ticks it. It holds the live licenseHolder
	// the seat policy reads, so it rides the engine like sourceReconciler.
	licenseService *licenseService
	// notifyDispatcher is the XV output adapter, exposed so the SIGHUP
	// handler can hot-reconcile EXTERNAL output destinations (re-read the operator
	// config, atomically reload changed third-party output binaries) — it holds the
	// runtime loader, so it rides the engine like sourceReconciler. secretResolver is
	// the same resolver the reconcile needs to re-resolve a destination's config
	// references. Both nil only when notify was never wired.
	notifyDispatcher *connectorDispatcher
	secretResolver   *secret.Resolver
	// logBroker is the console log surface's ring/pub-sub handler, held here for
	// one reason: the redaction wiring is otherwise UNOBSERVABLE. The
	// canonical credential catalog is injected into the broker at construction,
	// and without a handle there is no way for a test to show that removing the
	// injection changes anything — a wiring nothing can see fail is a wiring
	// nothing protects.
	logBroker *api.LogBroker
	// demoTenant is the seeded demo tenant id when boot ran with DemoSeed; else zero.
	demoTenant model.TenantID
	// auditPriors is the per-event signing key's rotation history (prior public
	// keys from the CMEK envelope) — what `audit verify` pins alongside the
	// current key so a rotated chain verifies end-to-end (audit.VerifyEventsWith).
	auditPriors []ed25519.PublicKey
	// connectorDir is the private scratch dir holding extracted first-party plugin
	// binaries (CB-1 transport B); removed on Close.
	connectorDir string
	// vectorIndex is the optional ANN backend adapter (nil unless OLIVARES_VECTOR_BACKEND
	// is configured); its connection pool is closed on Close.
	vectorIndex *vectorIndexAdapter
	// the knowledge module, exposed so the MCP retrieval upstream can invoke its
	// programmatic Query/FetchDocument/ListKBs API in-process.
	knowledgeMod *knowledge.Module
	// sessionsMod is module II, kept for the same reason as knowledgeMod: a surface built
	// AFTER boot needs it. Here it is the Codex hook PEP (SG-01), whose decider resolves a
	// Codex session id to the canonical sid through this module's identity plane — the one
	// step that cannot live in the Apache connector, because modules/sessions imports /core.
	sessionsMod *sessions.Module
	// protocolBindingReconciler is the late-bound REST multiplexer. A2A is
	// installed during module composition; the MCP adapter is added only after
	// the configured durable store and real upstream have both been constructed.
	protocolBindingReconciler *protocolBindingReconcileMux
	// approvalBridge is the OUTBOUND ApprovalGate bridge, exposed here so the
	// Agent-protocols gateway (MCP tools/call HITL) and the Claude Code hooks
	// PEP can reuse the SAME governed approval path instead of opening a second one. nil
	// when no bridge is configured.
	approvalBridge *approvalBridge
	// policyEval is the composed live PDP (governance native ABAC + external/authored
	// Cedar overlay), the SAME evaluator the Authorizer ANDs into every request. The
	// hooks PEP consults it as a deny-overlay for a tool-call. Restrict-only.
	policyEval auth.PolicyEvaluator
	// scopedGrants is the scoped grant/forbid engine (gov.ScopedGrants) — the SAME
	// engine the Authorizer and the source-scope resolver consult. F-03: the hooks PEP
	// consults its FORBID contribution so a central scoped forbid (e.g. forbid an mcp_server
	// for a principal) is applied at the hook too, keeping the authorization algebra
	// consistent across surfaces. Restrict-only at the hook (never widens a disposition).
	scopedGrants auth.ScopedAuthorizer
	// nhiEnforcer is the NHI-lifecycle enforcement query (the governance module):
	// the hooks PEP consults it, when a tenant opts in, to deny a tool-call by an agent
	// whose bound NHI is blocked (stale-escalated / offboarded — the offboarding cascade).
	nhiEnforcer nhiEnforcer
	// killSwitch is the estate kill-switch live-state consult (the governance
	// module): the hooks PEP and the MCP gateway deny every governed call while a
	// stop scopes it, fail-closed on a read error. The module-seam stop gates are
	// wired in buildModules; these two surfaces are built post-boot, so the handle
	// rides the engine like nhiEnforcer.
	killSwitch killSwitchGuard
	// circuitBreaker is the OPTIONAL enterprise runtime circuit-breaker, wired in
	//. nil in the default AGPL build ⇒ the inference gate skips it, behavior
	// UNCHANGED. It is held here because TWO consumers need the same instance: the
	// inference proxy consults State(), and the finding rail drives OnFinding().
	circuitBreaker circuitBreakerEngine
	// pinVerifier is the tool-pin verifier (nil in the community build),
	// constructed once in buildModules and shared: the MCP gateway PEP consults it
	// at tools/call and the enterprise reporting tool-pin source reads its drift
	// summary — the same store, so the report never disagrees with enforcement.
	pinVerifier mcpc.ToolPinVerifier
	// stopDeny is the throttled tamper-evident recorder for kill-switch denials at
	// the cmd-side surfaces (hooks PEP / MCP gateway) — the evidence pack's "PEP
	// decisions" leg (module seams record their own denials in their own ledgers).
	stopDeny *stopDenyRecorder
	// the inline inference PEP composes these post-boot (its own loopback socket,
	// opt-in via OLIVARES_INFERENCE_PROXY_CONFIG). models backs the model-access
	// gate; finops the in-band budget admission; inferenceProxy the per-tenant config +
	// DLP policy; residencyReg the data-residency check. nil-safe: the proxy mounts
	// nothing when unconfigured.
	models         modelAccessGate
	finops         budgetChecker
	inferenceProxy proxyPolicySource
	residencyReg   *residency.Registry
	// pivConfig is the PIV/CAC route config (nil = unconfigured). The serve
	// command arms the HTTP listener with it (optional client-cert request) so
	// the handlers can read the verified peer certificate.
	pivConfig *auth.PIVConfig
	// fedSvc is the managed SSO config service, held so the serve command's
	// enterprise SP-metadata endpoint can resolve the SAME live SAML provider
	// login uses and publish its metadata. nil only in embedders that skip it.
	fedSvc *auth.FederationService
	// bus is the event bus boot constructed and OWNS: the in-proc default
	// or the NATS-bridged hybrid. The runtime never closes an injected bus, so
	// Close here does, after the runtime has stopped every subscriber.
	bus eventbus.Bus
	// rlStore is the shared rate-limit bucket store (nil when the limiter
	// runs in-proc). A dedicated pool, closed here.
	rlStore *pgstore.Store
	// metrics is the shared Prometheus registry, constructed here so components
	// living OUTSIDE core/api (the bus collectors, the audit checkpointer) can
	// register scrape-time families on the same /metrics the API serves.
	metrics *metrics.Registry
	// wifBroker is the in-process WIF credential broker (sessions + executor
	// planes). Held only so Close releases its lazily-dialed SPIRE Workload API
	// connection on shutdown; nil-safe when WIF was never minted from.
	wifBroker *wifCredentialBroker
	// haPublisher/haStop are the stage-2 HA leader-label publisher and the
	// cancel of its resync loop. nil outside the leader-routing layout. Close stops
	// the loop and best-effort demotes the label so the leader Service drops this
	// pod the moment it starts draining.
	haPublisher *haLeaderPublisher
	haStop      context.CancelFunc
	// haGate mirrors the HA leader-routing gate switch, so the serve command can
	// apply the same refusal to the AUXILIARY listeners it owns (they live outside
	// core/api's middleware chain).
	haGate bool
}

// nhiEnforcer is the subset of the governance module the hooks PEP needs for the
// Risk-conditional deny: resolve an agent ref to its bound NHI and report
// whether that NHI is currently blocked. *governance.Module satisfies it.
type nhiEnforcer interface {
	NHIEnforcementForAgentRef(ctx context.Context, tenant model.TenantID, agentRef string) (blocked bool, reason string, err error)
}

// boot wires the engine — this is the COMPOSITION ROOT. It constructs the Fase C
// modules (wire.go), registers them with the runtime (so their schema fans out at
// store open, the S02 §7 handoff), opens the store via the PUBLIC core/engine seam
// (never the internal store — that is why the binary lives in its own module), hands
// each module its tenant-scoped data accessor, wires the governance ABAC evaluator
// into the authorizer, and builds the API server with every module's routes mounted.
// It does not start any listener.
func boot(ctx context.Context, cfg bootConfig) (*engine, error) {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	// The console log surface is redacted at the broker. The canonical
	// credential catalog is injected here rather than imported by core/api,
	// because core must not depend on /modules (scripts/check-boundary.sh) and the
	// detector catalog is single-owner in modules/security — the same seam
	// shape SupportBundleRedact already uses below. RedactCredentials, not
	// RedactText: the log viewer keeps the network and contact identifiers that
	// ARE the operator's diagnosis, and the support bundle, which leaves the
	// machine, keeps the full PII catalog.
	logBroker := api.NewLogBroker(
		log.Handler(), logBrokerRingSize, loadLogCaptureLevel(osGetenv, log),
		api.WithLogRedactor(securitymodule.RedactCredentials),
	)
	log = slog.New(logBroker)
	dataDirWasImplicit := cfg.DataDir == "" && os.Getenv("OLIVARES_DATA_DIR") == ""
	if cfg.DataDir == "" {
		resolved, err := defaultDataDir()
		if err != nil {
			return nil, err
		}
		cfg.DataDir = resolved
	}
	// A command pointed at a REMOTE store carries its own DSN, and its data
	// directory holds nothing it needs. Requiring one there would refuse exactly
	// the documented Postgres invocation (`audit verify --engine postgres --dsn
	// env:DATABASE_URL`) for the sake of a directory it would never read — a
	// consequence the sol-max contrast pointed out.
	storeIsRemote := cfg.Engine == string(store.EnginePostgres) || strings.TrimSpace(cfg.DSN) != ""
	switch {
	case cfg.ReadOnly && !storeIsRemote:
		// Read, never install. An absent data dir is the answer, not a task.
		if err := requireDataDir(cfg.DataDir); err != nil {
			return nil, err
		}
	case cfg.NoImplicitInstall && dataDirWasImplicit && !storeIsRemote && !installationExistsAt(cfg.DataDir):
		// A mutating CLI verb (`secrets put`, `sources set`, `audit checkpoint`, …)
		// asked to write into an installation that is not there, at a RELATIVE
		// default path nobody named. Creating it would mint three private signing
		// keys and a store in whatever directory the operator happened to be in,
		// and the operator would learn about it from a WARN line. Initializing an
		// installation is `quickstart`/`setup`'s job, and it says so; naming the
		// directory explicitly is the other way to state the intent.
		//
		// The question is "is there an installation here", NOT "does the directory
		// exist": an empty ./olivares-data/ let this through and the command minted
		// key material inside it. Found by the sol-max contrast.
		return nil, exitcode.New(exitcode.NotFound, fmt.Errorf(
			"no installation at %s, and this command will not create one implicitly: "+
				"it would mint signing keys in the current directory — run `olivares quickstart` "+
				"(or `olivares setup`) to initialize, or pass --data-dir / OLIVARES_DATA_DIR to say "+
				"where the installation is", absOrSame(cfg.DataDir)))
	case cfg.ReadOnly:
		// Remote store: nothing local to require, and nothing to create either.
	default:
		// EnsureDataDir, not EnsureDir: the directory about to hold seven private keys
		// carries its own .gitignore, so an operator who points --data-dir inside their
		// own repository cannot commit the key material either (core/secure).
		if err := secure.EnsureDataDir(cfg.DataDir); err != nil {
			return nil, err
		}
		// And when it CANNOT carry one — the data dir is a repository root, where a `*`
		// rule would hide the operator's whole project — say so. A protection that
		// quietly does not apply is the defect this whole change is about.
		if w := secure.DataDirVCSWarning(cfg.DataDir); w != "" {
			log.Warn("data directory: " + w)
		}
	}
	// load the enterprise activation manifest into the osGetenv overlay
	// BEFORE buildModules reads any add-on's OLIVARES_*_CONFIG. A corrupt manifest
	// degrades to "nothing activated" (logged), never fails boot.
	if err := initActivationManifest(cfg.DataDir); err != nil {
		log.Warn("enterprise-activation: could not read the activation manifest; no preset add-ons activated this boot", "err", err)
	}
	// start with a clean slate so a config removed since a prior boot (or a
	// previous boot in the same test process) does not leak a stale cleartext-secret
	// WARN; the loaders below re-record as they read.
	resetUnsealedSecretConfigs()

	// resolve any storeless secret reference (file:/env:) on the DSNs BEFORE
	// the store opens, so the database password can live in a 0600 file or an env
	// var instead of in cleartext in the systemd env file (`olivares setup` writes
	// file:/etc/olivares/secrets/db.dsn). The store-backed schemes are refused here
	// — the store is the database we are about to open.
	for _, r := range []struct {
		label string
		dst   *string
	}{{"--dsn", &cfg.DSN}, {"--owner-dsn", &cfg.OwnerDSN}, {"--admin-dsn", &cfg.AdminDSN}} {
		resolved, err := resolveDSNRef(ctx, r.label, *r.dst, osGetenv)
		if err != nil {
			return nil, err
		}
		*r.dst = resolved
	}

	eng := store.EngineSQLite
	if cfg.Engine == string(store.EnginePostgres) {
		eng = store.EnginePostgres
	}
	dsn := cfg.DSN
	if eng == store.EngineSQLite && dsn == "" {
		dsn = filepath.Join(cfg.DataDir, "olivares.db")
		// The SQLite driver creates the file it is pointed at, so under ReadOnly
		// the absence has to be caught HERE — opening the store would otherwise
		// materialize an empty 6 MB database that the command then reports as
		// containing nothing. Only the path this function DERIVED is checked: an
		// operator-supplied --dsn is theirs to name.
		if cfg.ReadOnly && !fileExistsAt(dsn) {
			return nil, exitcode.New(exitcode.NotFound, fmt.Errorf(
				"no store at %s: this is not an initialized Olivares data directory, and a "+
					"read-only command never creates one — run `olivares quickstart`, or point "+
					"--data-dir (or OLIVARES_DATA_DIR) at the installation", dsn))
		}
	}
	// Bound the Postgres application pool. It was unbounded (database/sql
	// default 0 = unlimited), which is a latent bug HA surfaces: with replicaCount>1,
	// each node ALSO opens a dedicated leader-lock connection and (optionally) an
	// admin pool, so several unbounded app pools can exhaust the server's
	// max_connections. Default to a conservative per-node cap (leaving headroom for
	// the lock + admin connections under the typical 100-connection server limit);
	// OLIVARES_DB_MAX_CONNS overrides. SQLite is single-connection by construction.
	maxConns := 0
	if eng == store.EnginePostgres {
		maxConns = postgresMaxConns(osGetenv, log)
	}

	// build the multi-region residency registry from --region/--known-regions.
	// A malformed region config fails the boot HERE — before the store opens — rather
	// than at request time. nil/non-enforcing in single-region mode (no --region).
	residencyReg, err := residency.NewRegistry(cfg.Region, cfg.KnownRegions)
	if err != nil {
		return nil, err
	}

	// K3 content custody is explicit and boot-only. An absent path leaves the
	// sealer unbound; a declared path must load, unwrap and self-test before the
	// store opens so malformed custody can never create a partially booted node.
	communicationSealer, err := loadCommunicationContentSealer(
		ctx,
		osGetenv(envCommunicationContentKeyringFile),
		openCommunicationContentKeyringOperatorConfig,
	)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", envCommunicationContentKeyringFile, err)
	}

	// Load the audit signing key UP FRONT (it only touches a file/env/KEK, not the
	// store) so a module can take its public key at construction time — the security
	// module verifies audit checkpoints against it. In HA the key is SHARED (a
	// mounted Secret / env) and loaded fail-closed so every replica signs with the
	// same key (the ledger does not fork at failover); under CMEK custody it
	// is unwrapped IN MEMORY through the customer's KMS KEK and never exists in
	// clear at rest; single-node/dev mints it on first boot.
	auditKey, err := loadAuditSigningKey(cfg.DataDir, log, cfg.keyLoadOptions()...)
	if err != nil {
		return nil, err
	}
	if auditKey.created {
		log.Warn("generated a new audit signing key; back it up", "path", filepath.Join(cfg.DataDir, "audit-signing.key"))
	}
	// Load the catalog signing key UP FRONT too — an INDEPENDENT artifact-signing
	// key, NEVER the audit key — so module XIV ships SIGNING its approved registry
	// entries by default (the posture a governed internal marketplace needs; docs/SECURITY-HARDENING.md
	// §5). Same shared-or-local resolution as the audit key (shareable in HA so every
	// replica verifies pinned entries identically); a node without one keeps the
	// honest unpinned posture.
	catalogKey, err := loadCatalogSigningKey(cfg.DataDir, log, cfg.keyLoadOptions()...)
	if err != nil {
		return nil, err
	}
	if catalogKey.created {
		log.Warn("generated a new catalog signing key; back it up", "path", filepath.Join(cfg.DataDir, "catalog-signing.key"))
	}
	// the policy-artifact signing key — INDEPENDENT of both the audit and
	// catalog keys — backing the claude-policy distribution truth loop: publish
	// signs the exact distributed bytes, pull agents verify against its pinned
	// fingerprint. Same shared-or-local resolution as the catalog key.
	policyKey, err := loadPolicySigningKey(cfg.DataDir, log, cfg.keyLoadOptions()...)
	if err != nil {
		return nil, err
	}
	if policyKey.created {
		log.Warn("generated a new policy signing key; back it up and pin its fingerprint in the pull agents", "path", filepath.Join(cfg.DataDir, "policy-signing.key"))
	}
	// Optional off-box (KMS/HSM) checkpoint signer (R5). With none
	// configured, the default on-box Ed25519 signer is used unchanged. Per-event
	// signing always stays on-box (the hot path is never routed off-box).
	var signerOpts []audit.Option
	ck, ckErr := buildCheckpointKey(log)
	if ckErr != nil {
		return nil, fmt.Errorf("ledger checkpoint signer: %w", ckErr)
	}
	if ck != nil {
		signerOpts = append(signerOpts, audit.WithCheckpointKey(ck))
	}
	signer, err := audit.NewSigner(auditKey.priv, signerOpts...)
	if err != nil {
		return nil, err
	}
	// Custody governance: validate the DECLARED posture against the ACTUAL
	// wiring and fail closed on any mismatch — a buyer that mandated BYOK/CMEK/HYOK
	// must get a refused boot on a config regression, never a silent downgrade. Then
	// log the effective posture once, so every boot states its custody plainly.
	assertions, err := loadCustodyAssertions()
	if err != nil {
		return nil, err
	}
	if err := assertions.verify(auditKey.mode, ck != nil); err != nil {
		return nil, fmt.Errorf("key custody: %w", err)
	}
	checkpointPosture := "on-box ed25519"
	if ck != nil {
		checkpointPosture = "off-box " + string(ck.Algorithm()) + " " + ck.KeyID()
	}
	log.Info("key custody posture",
		"audit_key", auditKey.mode, "audit_kek", orDash(auditKey.kek),
		"audit_prior_generations", len(auditKey.priors),
		"catalog_key", catalogKey.mode, "checkpoints", checkpointPosture,
		"declared_key_custody", orDash(assertions.auditKey), "declared_ledger_custody", orDash(assertions.ledger))
	// A migration source left declared in the environment is inert HERE — the boot
	// path resolves custody through loadKeyWrapConfig alone and never consults it,
	// so the runtime keeps exactly one custody root. It is still said out loud
	// rather than ignored: the operator's CLI ceremonies WILL open with it, and a
	// variable nobody remembers setting is how the next rewrap becomes a mystery.
	// Only its presence and kind are logged; nothing else in that namespace is read.
	if kind := strings.TrimSpace(os.Getenv(envKeyWrapOld)); kind != "" {
		log.Warn("a KEK migration source is declared in the environment and this engine is IGNORING it — the boot path has one custody root by design; it changes which KEK `olivares keys` ceremonies OPEN with, so unset it once the migration window is closed",
			"env", envKeyWrapOld, "kind", kind)
	}

	// OBS-03: build the W3C Trace Context provider from env (opt-in; a no-op exporter
	// when no collector is configured). It NEVER blocks boot (OTLP connects lazily) and
	// a tracing fault never breaks a request. The instrumented HTTP client it returns
	// is the engine→Claude transport (traceparent injection + gen_ai span/metrics),
	// threaded into the inference seam via buildModules; the same provider's ingress
	// middleware is wired into the API server below.
	tracer, err := obstrace.New(ctx, obstrace.FromEnv(cfg.Version))
	if err != nil {
		return nil, fmt.Errorf("init tracing: %w", err)
	}
	if tracer.Enabled() {
		log.Info("observability: W3C Trace Context + OTLP export enabled (OBS-03)")
	}
	// On any boot error AFTER this point the engine struct is never returned, so its
	// Close() (which shuts the tracer down) never runs. Shut the tracer down here on
	// the error paths to avoid leaking the OTLP exporter's background goroutines;
	// cleared on the success path below so Close() owns shutdown thereafter.
	bootOK := false
	defer func() {
		if !bootOK {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = tracer.Shutdown(sctx)
			cancel()
		}
	}()

	// boot constructs (and owns) the event bus instead of letting the
	// runtime default one, so the serve path can select the distributed NATS
	// bridge (OLIVARES_BUS_CONFIG) and attach the saturation SLIs. ErrNotLeader
	// from a standby's store-writing subscribers is EXPECTED steady state in HA,
	// so it logs at Debug, not Warn (it still counts in the handler-errors SLI).
	// The demotion applies on EVERY topology on purpose: an HA pair without the
	// NATS bridge has the same standby write-gate noise, and on a single node
	// (always-leader elector) ErrNotLeader simply never fires.
	demoteNotLeader := func(err error) bool { return errors.Is(err, store.ErrNotLeader) }
	busCfg, err := loadBusConfig(osGetenv, log)
	if err != nil {
		// Fail-boot-closed (the buildCheckpointKey family): a declared distributed
		// bus that cannot be honored must never silently degrade to in-proc — the
		// node would run partitioned from the cluster.
		return nil, fmt.Errorf("event bus: %w", err)
	}
	// the durable backend and the open Core-NATS bridge are mutually exclusive —
	// the durable config already carries the NATS connection, so a second
	// OLIVARES_BUS_CONFIG is ambiguous intent. Refuse up front (deny-closed), before
	// either backend is constructed (so no connection leaks on this error path).
	if busCfg != nil && strings.TrimSpace(osGetenv(envDurableBusConfig)) != "" {
		return nil, fmt.Errorf("event bus: set only one of %s or %s", envDurableBusConfig, envBusConfig)
	}
	var bus eventbus.Bus
	// gatedBus is the chosen bus when it supports HA leader gating (the open NATS
	// bridge or the enterprise durable backend); nil for the single-node in-proc
	// default. boot installs the leader predicate on it once leadership is resolved.
	var gatedBus injectGatedBus
	// the enterprise durable JetStream backend (build-tag gated). In the
	// community build newDurableBus returns (nil,nil) unconfigured and an ERROR when
	// OLIVARES_DURABLE_BUS_CONFIG is set (fail-boot-closed — durability is enterprise).
	// Under -tags enterprise it returns the durable bus (licensed) or the open
	// Core-NATS bridge built from the same connection config (unlicensed fallback).
	durableBus, derr := newDurableBus(osGetenv, busPayloadDecoders(), demoteNotLeader, log, cfg.LicenseFile, cfg.DataDir)
	if derr != nil {
		return nil, fmt.Errorf("event bus: %w", derr)
	}
	switch {
	case durableBus != nil:
		bus, gatedBus = durableBus, durableBus
	case busCfg != nil:
		nb, nerr := natsbus.New(*busCfg, natsbus.Options{
			Logger: log, DemoteError: demoteNotLeader, Decoders: busPayloadDecoders(),
		})
		if nerr != nil {
			return nil, fmt.Errorf("event bus: %w", nerr)
		}
		bus, gatedBus = nb, nb
	default:
		bus = eventbus.NewInProc(eventbus.Options{Logger: log, DemoteError: demoteNotLeader})
	}
	defer func() {
		if !bootOK {
			_ = bus.Close()
		}
	}()

	// The shared metrics registry: built here (not inside api.New) so the bus
	// collectors and the audit checkpointer (cmd_serve) register scrape-time
	// families on the same /metrics exposition the API serves.
	reg := metrics.New(cfg.Version, time.Now())
	registerBusMetrics(reg, bus)
	busStats, _ := bus.(eventbus.StatsProvider)

	rt := runtime.New(runtime.Options{Logger: log, Bus: bus})

	// if the operator REQUIRED a semantic embedder (OLIVARES_EMBEDDINGS_REQUIRE)
	// but none is configured, refuse to boot rather than silently serve the lexical
	// local-hash fallback as if it were semantic (docs/SECURITY-HARDENING.md — never a silent gap).
	if err := checkEmbeddingsRequirement(osGetenv); err != nil {
		return nil, fmt.Errorf("embeddings requirement: %w", err)
	}

	// Load the operator's connector-wiring config ONCE (sources + identity roster +
	// knowledge document sources). buildModules consumes its Documents section to wire
	// the knowledge module's pull sources; wireSources/wireRoster (below, after the
	// store) consume Sources/Identity. One read, one place the wiring decision is made
	// (12 §7.3 IDN-06).
	srcCfg, err := loadSourcesConfig(log)
	if err != nil {
		return nil, fmt.Errorf("load sources operator config: %w", err)
	}

	// Construct and register every Fase C module (wire.go is the only file that
	// imports both /core and /modules). They must be added to the runtime BEFORE the
	// store opens so rt.RegisterSchema fans their schema out at construction.
	set, err := buildModules(signer, catalogKey.priv, policyKey.priv, auditKey.priors, tracer.AnthropicHTTPClient(nil), srcCfg, log)
	if err != nil {
		return nil, fmt.Errorf("load module operator config: %w", err)
	}
	if communicationSealer != nil {
		if set.sessions == nil {
			return nil, fmt.Errorf("bind %s: sessions module is unavailable",
				envCommunicationContentKeyringFile)
		}
		set.sessions.UseCommunicationContentSealer(communicationSealer)
	}
	for _, m := range set.all {
		sm, ok := m.(sdk.Module)
		if !ok {
			return nil, fmt.Errorf("module %q does not satisfy sdk.Module", m.APINamespace())
		}
		if err := rt.AddModule(sm, sdk.Config{}); err != nil {
			return nil, fmt.Errorf("register module %q: %w", m.APINamespace(), err)
		}
	}

	auditSpoolMaxBytes, auditSpoolOnFull, err := loadAuditSpoolConfig(osGetenv, log)
	if err != nil {
		return nil, fmt.Errorf("load audit spool operator config: %w", err)
	}
	auditMetaBlinding, err := loadAuditMetaBlinding(osGetenv, log)
	if err != nil {
		return nil, fmt.Errorf("load audit metadata blinding operator config: %w", err)
	}
	st, err := coreengine.Open(ctx, store.Config{
		Engine:              eng,
		DSN:                 dsn,
		AdminDSN:            cfg.AdminDSN,
		OwnerDSN:            cfg.OwnerDSN,
		MaxConns:            maxConns,
		AllowPrivilegedRole: cfg.AllowPrivilegedDBRole,
		AuditSpoolMaxBytes:  auditSpoolMaxBytes,
		AuditSpoolOnFull:    auditSpoolOnFull,
		AuditMetaBlinding:   auditMetaBlinding,
		// Sign every audit event at write time so the ledger is tamper-evident per
		// event, not only at the periodic checkpoints (closes the between-checkpoints
		// tail-rewrite window). The same key signs the checkpoints; verify off-box
		// with `audit verify --pubkey` (docs/SECURITY-HARDENING.md).
		SignEvent: signer.SignEvent,
	}, func(reg store.ExtensionRegistry) error {
		if err := rt.RegisterSchema(reg); err != nil {
			return err
		}
		// the tool-pin table is engine schema (tenant data), registered in
		// every edition so schemas stay deterministic; only the enterprise
		// overlay binds a verifier that writes to it.
		if err := registerToolPinSchema(reg); err != nil {
			return err
		}
		// the circuit-breaker tables are engine schema too, registered in every
		// edition for the same reason as the tool-pin table above — lint:schema-parity
		// compares community against enterprise, and an enterprise-only table fails it.
		return registerCircuitBreakerSchema(reg)
	})
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	// in a region-scoped deployment wrap the store with the deny-closed
	// residency guard, so every tenant-scoped unit of work for a tenant pinned to
	// another region is refused (store.ErrResidencyViolation) rather than silently
	// served an empty set. In single-region mode Guard returns st untouched (zero
	// overhead). System/Auth (including the leadership bootstrap below) pass through;
	// everything downstream (module data, authenticator, API) uses the wrapped store.
	if residencyReg.Enforces() {
		st = residency.Guard(st, residencyReg, log)
		log.Info("residency: region-scoped instance active — serving only this region's tenants, cross-region denied",
			"home_region", residencyReg.Home().String(), "known_regions", residencyReg.Known())
	}
	// Promotion recovery is custody, not service: it must be able to withdraw
	// credentials from a suspended local tenant, but it must retain this residency
	// guard and therefore cannot touch a tenant pinned to another region.
	sessionRuntimeRecoveryStore := st
	sessionRuntimeRecoveryData := api.NewModuleData(sessionRuntimeRecoveryStore)
	// wrap with the deny-closed service-withdrawal guard, so a tenant whose
	// org is suspended is not served by ANY path — REST, gRPC, or the in-process
	// background pumps, which all re-enter through View/Mutate. It is wrapped
	// OUTSIDE the residency guard on purpose: the innermost decorator's check runs
	// FIRST, so a cross-region request keeps reporting the precise residency
	// violation instead of being masked by "this instance has no org for you".
	// Always armed (any deployment can suspend a tenant); System/Auth pass through
	// so the operator can always restore, and a suspended tenant's users can still
	// authenticate and be told why they are refused.
	st = suspension.Guard(st, log)

	// Bind module data and the two private runtime credential issuers before the
	// first leadership acquisition. OnPromote must recover every durable runtime
	// handle before IsLeader becomes visible; wiring these after Leader.Run would
	// leave the initial leader with a post-promotion revocation window.
	data := api.NewModuleData(st)
	finData := finopsData{ModuleData: data, st: st}
	for _, m := range set.all {
		if dc, ok := m.(api.DataConsumer); ok {
			if set.finops != nil && m == set.finops {
				dc.UseData(finData)
				continue
			}
			dc.UseData(data)
		}
	}
	authr := auth.NewAuthenticator(st, nil)
	var communicationStoreWitness *communicationGuardStoreWitness
	if set.sessions != nil {
		set.sessions.UseRuntimeCredentialRecoveryData(sessionRuntimeRecoveryData)
		recoveryAuthr := auth.NewAuthenticator(sessionRuntimeRecoveryStore, nil)
		set.sessions.UseRuntimeCredentialRecoverySources(
			sessionWorkCredentialSource{authenticator: recoveryAuthr},
			sessionCommunicationCredentialSource{authenticator: recoveryAuthr},
		)
		set.sessions.UseWorkSessionCredentialSource(
			sessionWorkCredentialSource{authenticator: authr},
		)
		set.sessions.UseCommunicationSessionCredentialSource(
			sessionCommunicationCredentialSource{authenticator: authr},
		)
		// K3 guard repair is a leadership bootstrap, not tenant service. Bind its
		// closure-only, guard-scoped adapter over the pre-suspension store. The
		// residency wrapper remains load-bearing authority on every tenant mutation;
		// however the global witness below refuses CLEAN entirely when residency is
		// enforcing until a region-move ceremony can prove both sides of a repin.
		set.sessions.UseCommunicationGuardReconciliationData(
			sessions.NewCommunicationGuardReconciliationData(sessionRuntimeRecoveryData),
		)
		communicationStoreWitness = newCommunicationGuardStoreWitness(
			func(ctx context.Context) ([]model.Org, error) {
				var orgs []model.Org
				err := st.System(ctx, func(sys store.SystemScope) error {
					var err error
					orgs, err = sys.ListOrgs(ctx)
					return err
				})
				return orgs, err
			},
			residencyReg,
			set.sessions,
			st.Leader().IsLeader,
		)
		set.sessions.UseCommunicationStoreReadinessWitness(communicationStoreWitness)
	}
	recoverSessionRuntimeCredentials := func(ctx context.Context) error {
		return recoverSessionRuntimeCredentialsForPromotion(
			ctx,
			func(ctx context.Context) ([]model.Org, error) {
				var orgs []model.Org
				err := st.System(ctx, func(sys store.SystemScope) error {
					var err error
					orgs, err = sys.ListOrgs(ctx)
					return err
				})
				return orgs, err
			},
			residencyReg,
			set.sessions,
		)
	}
	// Build the Cedar reactivation barrier before registering OnPromote. The callback
	// runs before IsLeader becomes visible, including each later HA promotion; doing
	// this only after the first Run would let a promoted standby enforce its stale
	// in-memory G while durable authority has already reached G+1 elsewhere.
	pdpListOrgs := cfg.pdpListOrgs
	if pdpListOrgs == nil {
		pdpListOrgs = func(ctx context.Context, st store.Store) ([]model.Org, error) {
			var orgs []model.Org
			err := st.System(ctx, func(sys store.SystemScope) error {
				var listErr error
				orgs, listErr = pdpVisibleTenantInventory(ctx, sys, log)
				return listErr
			})
			return orgs, err
		}
	}
	pdpReload := cfg.pdpReload
	if pdpReload == nil {
		pdpReload = set.gov.ReloadActivePDP
	}
	reactivatePDPForPromotion := func(ctx context.Context) error {
		if !cfg.ServeMode {
			return nil
		}
		return reloadPDPForPromotion(ctx, func(ctx context.Context) ([]model.Org, error) {
			return pdpListOrgs(ctx, st)
		}, pdpReload, log)
	}

	// K1 durable work ports are composed only after the store exists and its
	// residency and suspension wrappers are installed. Sessions receives narrow adapters,
	// never another module's concrete implementation.
	if set.sessions != nil {
		set.sessions.UseWorkIdentityResolver(workIdentityResolver{
			st: st, sessions: set.sessions, agentLifecycle: set.gov,
		})
		set.sessions.UseProtocolLocalResourceResolver(protocolLocalResourceResolver{
			store: st, channels: set.sessions,
		})
		set.sessions.UseWorkContentGuard(workContentGuard{})
		set.sessions.UseWorkEventSink(workEventSink{eventing: set.eventing})
	}
	// Active-passive HA leadership. EnsureSystemTenant is the write-side
	// bootstrap only the ACTIVE writer performs, so register it as the elector's
	// OnPromote and let Run drive it: on the leader Run fires it synchronously before
	// returning; a standby skips it (the shared Postgres already holds the provisioned
	// system tenant) and follows, ready to run it the instant it is promoted. On
	// SQLite/single-node the always-leader elector fires it once — identical to the
	// unconditional call this replaces. Run also ARMS the write-gate, so from here a
	// standby's writes (and its background loops') fail closed with ErrNotLeader.
	//
	// Stage-2 (HA leader-ROUTING layout): when the operator deploys the
	// Patroni-style split it mounts a pod identity + a narrowly-scoped
	// ServiceAccount and sets OLIVARES_HA_LEADER_LABEL, so this node publishes its
	// role as a pod label the leader Service selects on. The label is DISCOVERY
	// only; the advisory lock below stays the sole write authority. Parsed BEFORE
	// the election so a misconfigured HA pod fails loudly at boot instead of
	// serving unroutable.
	haCfg, haErr := loadHALeaderConfig(osGetenv)
	if haErr != nil {
		_ = st.Close()
		return nil, fmt.Errorf("ha leader-routing config: %w", haErr)
	}
	var haPublisher *haLeaderPublisher
	if haCfg.PublishLabel {
		haPublisher, haErr = newHALeaderPublisher(haCfg, log)
		if haErr != nil {
			_ = st.Close()
			return nil, fmt.Errorf("ha leader-label publisher: %w", haErr)
		}
		// Publish STANDBY before the election. A pod object outlives its container:
		// after a crash-restart this pod may still carry the `leader` label of its
		// previous incarnation, and the leader Service would route writes to a node
		// that no longer holds the lock (they would fail closed, but the outage would
		// be silent). Refusing to boot when this cannot be published is deliberate:
		// an HA pod that cannot manage its own label is not safe to route to.
		if err := haPublisher.publishRole(ctx, haRoleStandby); err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("ha: could not publish the initial standby role label (a stale leader label from a previous incarnation would misroute traffic): %w", err)
		}
	}
	promote := func(ctx context.Context) error {
		if err := st.System(ctx, func(sys store.SystemScope) error {
			if _, e := sys.EnsureSystemTenant(ctx); e != nil {
				return e
			}
			// FASE X /: back-fill the default workspace for tenants provisioned
			// before in the same write-side bootstrap only the active writer
			// runs. New tenants get theirs in CreateOrg; this is idempotent, so a
			// standby that is later promoted re-runs it harmlessly.
			return sys.EnsureDefaultWorkspaces(ctx)
		}); err != nil {
			return err
		}
		if communicationStoreWitness != nil {
			if err := communicationStoreWitness.ReconcileAndVerify(ctx); err != nil {
				// WP-2 is an inert private cut until WP-3 binds the pump witness. A
				// failed or non-authoritative estate proof keeps store readiness
				// UNKNOWN/OFF, but must not take unrelated product surfaces down.
				log.Error("sessions: communication guard bootstrap incomplete; K3 store readiness remains off",
					"err", err)
			} else {
				log.Info("sessions: communication guard estate reconciled and verified")
			}
		}
		if err := recoverSessionRuntimeCredentials(ctx); err != nil {
			return err
		}
		if err := reactivatePDPForPromotion(ctx); err != nil {
			return fmt.Errorf("pdp: cannot establish a complete tenant inventory for authored Cedar reactivation: %w", err)
		}
		// Stage-2: the leader label is NOT published from here. This callback
		// runs while the elector is still `promoting` — leadership is not yet
		// established (the fencing epoch has not been bumped and IsLeader is still
		// false), so advertising the label here could route client traffic to a pod
		// whose promotion later fails. The resync loop publishes it the moment
		// IsLeader() flips, which is the same predicate the request gates use.
		return nil
	}
	if cfg.pdpPromotionRegistered != nil {
		cfg.pdpPromotionRegistered(promote)
	}
	st.Leader().OnPromote(promote)
	if err := st.Leader().Run(ctx); err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("leader election / provision system tenant: %w", err)
	}
	// The resync loop that converges the label onto live leadership is started at the
	// very END of boot (just before the engine is returned), so no failure path
	// between here and there leaks a goroutine that keeps patching the pod of an
	// engine that never came up.
	// E2: compatibility TOFU is allowed only after the enterprise tool-pin
	// store durably appends its HIGH auto-pin event. The community build binds
	// nothing; the enterprise overlay attaches this store-backed recorder before
	// any MCP resource server can accept traffic.
	bindToolPinAudit(set.pinVerifier, st, log)
	// rebuild pins/drifts from the tenant-scoped table and write through
	// from here on — a restart no longer clears pins, so compatibility TOFU can
	// no longer re-legitimate a rug-pull across restarts. Community binds
	// nothing (nil verifier).
	bindToolPinPersistence(ctx, set.pinVerifier, st, log)
	// arm the bridge's inject gate now that leadership exists. Remote
	// events reach local subscribers ONLY on the active node — one predicate at
	// the bus boundary instead of a standby-side-effect patch in every module
	// (duplicate notifications, derived findings, ErrNotLeader per event). The
	// gate is advisory (the elector's 2s tick, not a fence); the eventing
	// capture dedupe absorbs the failover-overlap double inject. Subscribers do
	// not exist until rt.Start below, so the unarmed window cannot deliver.
	//
	// the same call arms the enterprise durable bus's leader-gated JetStream
	// consumer lifecycle (it binds the durable consumer on promotion, stops it on
	// demotion) — the durable consumer's server-side position survives failover.
	if gatedBus != nil {
		gatedBus.SetInjectGate(st.Leader().Active)
	}
	// liveingest's observed-ref derivation is leader-gated for the same
	// reason — a stateless republisher has no store write to fence it, and the
	// leader already sees every node's edges over the bridge (exactly-once
	// derivation cluster-wide). Single-node (always-leader elector) unchanged.
	set.live.UseLeadership(st.Leader().Active)

	// Honest posture (R2): without a dedicated BYPASSRLS admin pool, authoritative
	// cross-tenant ceremonies on a properly secured Postgres app role fail with
	// ErrEnumerationNotAuthoritative. Explicit best-effort visible reads may still
	// be partial; neither outcome is silently treated as a complete org list.
	// SQLite needs no admin pool (no roles).
	if eng == store.EnginePostgres && cfg.AdminDSN == "" {
		log.Warn("no --admin-dsn configured: authoritative cross-tenant ceremonies fail closed with ErrEnumerationNotAuthoritative; explicitly best-effort visible reads may be partial; provision a NOSUPERUSER BYPASSRLS admin role (deploy/postgres/01-app-role.sql) and set --admin-dsn for full coverage")
	}

	// Module data was bound before leader election so session credential recovery
	// participates in the promotion barrier. The remaining late-bound consumers
	// below reuse that same accessor before the runtime starts.
	// late-bind the same tenant-scoped data handle into the knowledge retrieval
	// guard, which was constructed in buildModules before the store existed (the
	// approval-bridge late-binding pattern). It resolves agent→identity→groups/
	// clearance/region from the governed identity plane on each retrieval.
	if set.knowledgeGuard != nil {
		set.knowledgeGuard.useData(data)
	}
	if set.knowledgeEmbedder != nil {
		set.knowledgeEmbedder.UseData(data)
	}
	if set.knowledgeStatus != nil {
		set.knowledgeStatus.useGuardPostureStore(st, set.sourceScopeResolver)
	}
	// late-bind the raw store into the account eraser — it needs the AUTH
	// partition (Store.AuthMutate), which no tenant-scoped data handle can reach.
	if set.accountEraser != nil {
		set.accountEraser.useStore(st)
	}
	// late-bind the same data handle into the policy truth-loop seams, both
	// constructed in buildModules before the store existed. Until bound, the
	// distributor is deny-closed (publish reports enqueue-failed, never
	// "distributed") and the observed provider reports "could not be read".
	if set.policyDist != nil {
		set.policyDist.UseData(data)
	}
	if set.policyObserved != nil {
		set.policyObserved.UseData(data)
	}

	// C: serving processes establish every tenant's active Cedar PDP inside
	// OnPromote, before leadership becomes visible. That barrier is deliberately not
	// repeated here after Run: doing so would reload twice at initial boot and would
	// still miss later HA promotions. Inventory and each tenant reload run in separate
	// transactions inside reloadPDPForPromotion, preserving SQLite's single-connection
	// deadlock avoidance.

	// The governance module's ABAC evaluator restricts the built-in RBAC further
	// (AND, never widens; nil-safe). It shares its data handle via UseData above. The
	// SAME composed evaluator is captured for the hooks PEP, which consults it as a
	// deny-overlay for a tool-call (one source of policy truth, never a second copy).
	policyEval := set.gov.Evaluator()
	// the MAIN request authorizer additionally consults the per-tenant SCOPED-GRANT
	// engine (Cedar) BESIDE the deny-overlay, so authorization is no longer flat RBAC ∩
	// deny: a permit GRANTS within the scope tree, a forbid still RESTRICTS, default-
	// deny stands. RequestEvaluator() is the deny-overlay WITHOUT the authored policy (the
	// scoped seam evaluates that policy once — grants and forbids together). A tenant with
	// no authored grants makes the scoped engine abstain before any store read.
	authz := auth.NewAuthorizer(set.gov.RequestEvaluator(), auth.WithScopedGrants(set.gov.ScopedGrants()))
	setupTok := secure.NewSetupToken(filepath.Join(cfg.DataDir, "setup.token"))

	// late-bind the eventing platform's two seams. The SAME composed
	// authorizer (RBAC ∩ governance ABAC) that gates live requests gates every
	// outbound event delivery (deny-closed per event); the secret sealer holds
	// subscription signing secrets encrypted at rest under an engine-held key.
	// A sealer failure keeps the module's fail-closed default (no subscriptions
	// can be created; existing ones consume their retry ladder recording
	// secret_unavailable and dead-letter if the sealer never returns — visible
	// in the DLQ, recoverable by redeliver) — loud, never a cleartext downgrade
	// and never a boot abort.
	set.eventing.UseAuthorizer(authz)
	if set.sessions != nil {
		set.sessions.UseCommunicationRequestAuthority(authr, authz)
		set.sessions.UseWorkAuthorizer(authz)
	}
	// Unit G: late-bind the deployment's DURABLE disposition for the egress
	// destination control. Without it an absent policy permits on every deployment,
	// including a brand-new one that has nothing to grandfather — which is allow-all
	// with no expiry in the module whose thesis is governing egress. The engine
	// classified this deployment once, before it created eventing's tables; this is
	// where the module gets to read the answer.
	//
	// A store that does not expose the capability is a BOOT FAILURE and not a warning.
	// The alternative is the failure this campaign has already shipped: a control that
	// is present in the code, absent from the binary's behavior, and indistinguishable
	// from a working one until somebody audits it.
	if rollout, ok := newEventingEgressRollout(st); ok {
		set.eventing.UseEgressRollout(rollout)
		logEventingEgressRollout(ctx, log, rollout, osGetenv(envEventingEgressPolicy) != "")
	} else {
		return nil, fmt.Errorf("eventing: the store does not expose durable rollout state, so the egress destination control cannot establish whether it is in force")
	}
	// Unit H: the writer fence's own durable disposition. Its own key, its own epoch —
	// deriving it from the destination control's classification is the critical defect an
	// adversarial review of this design found, in both directions (see
	// eventing.EgressWriterFenceControlKey). Boot-fatal for the same reason as above: a control
	// present in the code and absent from the binary's behavior is what this campaign has
	// already shipped once.
	if fence, ok := newEventingWriterFence(st); ok {
		set.eventing.UseEgressWriterFence(fence)
		logEventingWriterFence(ctx, log, fence)
	} else {
		return nil, fmt.Errorf("eventing: the store does not expose durable rollout state, so the egress writer fence cannot establish whether it is armed")
	}
	eventingSealerPresent := false
	if sealer, serr := newEventingSealer(cfg.DataDir, osGetenv); serr != nil {
		log.Error("eventing: secret sealer unavailable; subscriptions are disabled until fixed", "err", serr)
	} else {
		set.eventing.UseSecretSealer(sealer)
		eventingSealerPresent = true
	}

	// the managed SSO config service. It backs the console SSO endpoints
	// and resolves the live federation provider from a store-backed, SEALED config —
	// so SSO is configurable from the console, not env-only. Since the
	// single-IdP provider BUILDER is OPEN-CORE (newFederationBuilder is non-nil in
	// BOTH builds), so the base AGPL build does real single-IdP login; the
	// env-configured provider is the FALLBACK used only when no managed config row
	// exists. The reserved MULTI-IdP capability (newFederationMultiIDP) is nil in the
	// base build — that nil is what enforces the single-IdP cap (a second active IdP
	// returns multi_idp_requires_enterprise) and limits Resolve to the global config.
	// A sealer failure leaves the service without a sealer: config WRITES fail closed
	// (never cleartext), reads/login still work — loud, never a boot abort, never a
	// cleartext downgrade.
	var fedSealer auth.FederationSealer
	federationSealerPresent := false
	if sealer, serr := newFederationSealer(cfg.DataDir, osGetenv); serr != nil {
		log.Error("federation: SSO secret sealer unavailable; SSO config writes are disabled until fixed", "err", serr)
	} else {
		fedSealer = sealer
		federationSealerPresent = true
	}
	fedSvc := auth.NewFederationService(st, fedSealer, newFederationBuilder(), newFederation(osGetenv, log), newFederationMultiIDP())
	// U8: converge the derived home-realm domain index (federation_domain_claims) from the
	// authoritative ClaimedDomains on every config, so an UPGRADED deployment's existing domains
	// route via the indexed path and any legacy cross-config duplicate is quarantined (deny-closed
	// to the global IdP) instead of mis-routing. Idempotent + collision-safe; loud on failure but
	// never a boot abort — a lagging index only degrades home-realm routing to the global fallback.
	// late-bind the SSO posture the identity console reports at
	// /v1/m/identity/sso. It happens HERE because the federation service needs the
	// store and the config sealer, so it does not exist when buildModules runs.
	if set.identityConsole != nil {
		set.identityConsole.UseSsoPosture(fedSsoPosture{svc: fedSvc})
	}
	if err := fedSvc.ReconcileDomainClaims(ctx); err != nil {
		log.Error("federation: home-realm domain index reconcile failed; home-realm routing degraded to the global IdP until fixed", "err", err)
	}

	// the runtime secret store + reference resolver. The sealer seals stored
	// secret values at rest under an engine-held key (distinct custody from SSO and
	// eventing). A sealer failure leaves the store read-only (writes fail closed and
	// `store:` references cannot resolve) — loud, never a boot abort, never a
	// cleartext downgrade. The resolver turns a `<scheme>:<locator>` config reference
	// into a live value at Open across every secret-bearing config path (sources,
	// roster, knowledge, notify, claude-agents); env/file/store are built in, the
	// external backends (vault + cloud secret managers) come from the secretref
	// readers for whichever the environment configures.
	var secretSealer auth.SecretSealer
	secretStoreSealerPresent := false
	if sealer, serr := newSecretSealer(cfg.DataDir, osGetenv); serr != nil {
		log.Error("secret-store: sealer unavailable; secret writes and store: references are disabled until fixed", "err", serr)
	} else {
		secretSealer = sealer
		secretStoreSealerPresent = true
	}
	secretStore := auth.NewSecretStore(st, secretSealer)
	secretResolver := newSecretResolver(secretStore, osGetenv, log)

	// the durable source roster (the store-backed successor to the file's
	// `sources[]`) and the live reconciler that wires it into the running runtime
	// without a restart. The connector scratch dir is created here (rather than just
	// before Start) so the reconciler can extract a first-party plugin binary on a
	// LIVE add too; it is removed on Close (or if the boot aborts). ConnectorTrust
	// for external (third-party) plugins stays operator-file config (a restart-time
	// trust decision), read once and handed to the reconciler.
	sourceStore := auth.NewSourceStore(st)
	connectorDir, derr := newConnectorScratchDir(cfg.DataDir)
	if derr != nil {
		_ = st.Close()
		return nil, fmt.Errorf("connector scratch dir: %w", derr)
	}
	defer func() {
		if !bootOK {
			_ = os.RemoveAll(connectorDir)
		}
	}()
	sourceReconcilerSvc := newSourceReconciler(rt, sourceStore, secretResolver, secretStore, connectorDir, srcCfg.ConnectorTrust, log)

	// In-place edition: resolve the commercial license by precedence (explicit
	// --license > OLIVARES_LICENSE_PATH > OLIVARES_LICENSE > the data-dir default
	// file) and hold it in a LIVE, swappable holder so `license install` / the console
	// / a reload hot-apply a renewal with ZERO downtime, and an expiry degrades back
	// to the community edition — the only restart left is the binary swap
	// open→enterprise (the Grafana model). It never costs a user account: self-hosted
	// accounts are unlimited in every tier (B10). A configured-but-unreadable source fails
	// loudly (explicit operator intent); the absent data-dir default is just "none".
	licPub := license.DefaultPublicKey()
	licSrc, lerr := resolveLicense(cfg.LicenseFile, cfg.DataDir, osGetenv)
	if lerr != nil {
		_ = st.Close()
		return nil, fmt.Errorf("resolve license: %w", lerr)
	}
	licHolder := newLicenseHolder(licPub, licSrc, time.Now, log)
	// Boot posture: WARN if the engine starts already expired/invalid (honest
	// degradation — the enterprise add-ons are off, but the engine never crashes,
	// never loses data and never caps user accounts).
	switch d := licHolder.display(); d.status {
	case "grace":
		log.Warn("license: the installed commercial license is past expiry but inside its grace window at boot — enterprise entitlements are maintained for now; renew before the grace ends (data and user accounts intact)", "licensee", d.licensee, "source", d.source.Kind)
	case "expired":
		log.Warn("license: the installed commercial license is EXPIRED at boot — running the community edition until renewed (data intact)", "licensee", d.licensee, "source", d.source.Kind)
	case "invalid":
		// The reason, not a guess at it: this line used to name the KEY unconditionally, and a
		// signed container this build cannot read verifies against the key perfectly well.
		log.Warn("license: the installed license could not be read — running the community edition",
			"reason", errText(d.reason), "source", d.source.Kind)
	case "valid", "perpetual":
		log.Info("license: commercial license loaded", "status", d.status, "licensee", d.licensee, "source", d.source.Kind)
	}

	// Seat seam (retained, display-only since B10). Self-hosted user accounts are
	// UNLIMITED in every tier: the community policy reports unlimited, no policy can
	// refuse an account (core/auth.enforceSeatCapTx is an unconditional no-op), and
	// no license state — valid, expired or absent — changes that. The wiring stays so
	// the enterprise overlay keeps its injection point and the console keeps its
	// (usage-only) figure. Set once here, before the server is built — race-free.
	authr.WithSeatPolicy(newSeatPolicy(licHolder.claims, crlViewFromDataDir(cfg.DataDir)))
	// Grant list for the closed add-on gates. No-op in the AGPL build; the
	// overlay binds it as the EntitlementFunc every Authorize consults.
	bindEnterpriseEntitlement(licHolder.grants)

	// Login enforcement: require-SSO + network/IP allow-list over the login
	// surface. The default (AGPL) build wires nil (newLoginPolicy → no enforcement,
	// login byte-identical to today); the enterprise build injects the closed engine
	// (enterprise/ssoenforce), which reads the stored posture via fedSvc.Posture and
	// decides — the open binary never links the enforcement code. Set once here,
	// before the server is built — race-free, like WithSeatPolicy.
	authr.WithLoginPolicy(newLoginPolicy(osGetenv, fedSvc, log))

	// Login-time group mapping: the default (AGPL) build wires nil
	// (newGroupMapper → asserted IdP groups are extracted but never mapped to
	// grants); the enterprise build injects the reserved GroupMapper
	// (enterprise/federation), which resolves the groups an IdP asserts at login to
	// the tenant's directory groups so the existing MappedRole/group-subject grants
	// fire. The open binary never links the mapping code. Set once here, before the
	// server is built — race-free, like WithSeatPolicy/WithLoginPolicy.
	authr.WithGroupMapper(newGroupMapper())

	// Agent-OBO lifecycle check: the governance module's
	// CheckAgentForExchange method validates that a named agent exists with
	// kind=agent, is not blocked/orphaned, and the exchange subject IS the
	// agent's registered human sponsor. Without this wiring, any token-exchange
	// request with requested_actor is rejected (deny-closed). Set once here,
	// before the server is built — race-free, like the seat/login policies.
	authr.SetAgentLifecycleChecker(set.gov)

	// CAEP transmitter: emit agent-risk events to external SSF receivers.
	// The community (AGPL) build wires nil — no events are pushed outbound and the
	// open receiver (core/auth/caep_events.go) is unaffected (no rug-pull). The
	// enterprise build (caeptransmit_enterprise.go) reads
	// OLIVARES_CAEP_TRANSMITTER_CONFIG and signs + HTTP-pushes SETs to configured
	// endpoints (RFC 8935). Set once here before the server is built. Bus
	// subscription over circuit-breaker and session-revoke events is wired in a
	// follow-up task.
	caepTx, err := newCAEPTransmitter(osGetenv, log)
	if err != nil {
		return nil, fmt.Errorf("load CAEP transmitter operator config: %w", err)
	}
	_ = caepTx // bus subscription wired in follow-up (Task 7)

	// The engine-side edition/license service backs the console/CLI install/status +
	// the live server-info status. Pure edition plumbing — it never gates a feature
	// (LICENSING.md); it persists/observes/hot-applies the artifact and enforces the
	// downgrade-acknowledge guard against the LIVE active estate.
	licSvc := newLicenseService(licHolder, authr, cfg.DataDir, cfg.LicenseFile, osGetenv, buildEdition, log)

	// the enterprise activation service backs the console /v1/console/activation
	// surface (per-add-on state + enable/disable/promote). It is enterprise-only — the
	// community build's seam returns nil (wire_noenterprise.go) so the routes 501. It
	// reads/writes the SAME governed activation manifest the CLI does and audits changes
	// through the live store (SystemTenantID scope).
	actSvc := newActivationService(cfg.DataDir, st, buildEdition, log)

	// Privileged login: the PIV/CAC route config is loaded once and shared
	// between the API (verification) and the serve command (the HTTP listener
	// must request the optional client certificate — engine.pivConfig).
	pivCfg, err := loadPIVConfig(osGetenv, log)
	if err != nil {
		return nil, fmt.Errorf("load PIV operator config: %w", err)
	}

	// OPS-5 inbound rate limiting, always non-nil (secure-by-default), with
	// the shared-store selection on top: in HA the buckets must be GLOBAL
	// (per-node shards multiply every quota by the replica count — the limit
	// PR #33 documented). OLIVARES_RATELIMIT_STORE=postgres opts in (the Helm
	// chart sets it when replicaCount > 1); single-node stays in-proc and pays
	// no per-request round trip. Store outages degrade to per-node enforcement
	// behind a circuit breaker — bounded, counted, alertable, never unlimited.
	rlCfg, err := loadRateLimitConfig(osGetenv, log)
	if err != nil {
		return nil, fmt.Errorf("load rate limit operator config: %w", err)
	}
	rlCfg.LogWarn = func(msg string, err error) { log.Warn(msg, "err", err) }
	var rlStore *pgstore.Store
	if useShared, serr := resolveRateLimitStore(osGetenv, eng); serr != nil {
		return nil, fmt.Errorf("rate limit store: %w", serr)
	} else if useShared {
		ps, perr := pgstore.Open(ctx, dsn, pgstore.Options{
			IdleTTL:  ratelimit.IdleTTLFor(rlCfg.Tiers),
			Registry: reg,
			Logger:   log,
			// Owner/app split: the bucket-table + take-function DDL needs
			// CREATE on the schema, which the app role deliberately lacks.
			DDLDSN: cfg.OwnerDSN,
		})
		if perr != nil {
			return nil, fmt.Errorf("rate limit store: %w", perr)
		}
		rlStore = ps
		rlCfg.Store = ps
		log.Info("rate limit: shared Postgres bucket store active (global quotas across nodes)")
	}
	defer func() {
		if !bootOK && rlStore != nil {
			_ = rlStore.Close()
		}
	}()

	// OTA update indicator: OPT-IN. With OLIVARES_UPDATE_ENDPOINT set AND a
	// release key embedded, run a cached background check the console reads via the
	// health summary. Unset (air-gapped) ⇒ nil ⇒ the console shows no indicator, no
	// error, and NO outbound calls — silence is the honest air-gap default.
	var (
		updateStatusFn  func() updatecheck.Status
		updateRefreshFn func(context.Context) updatecheck.Status
	)
	if ep := strings.TrimSpace(os.Getenv("OLIVARES_UPDATE_ENDPOINT")); cfg.ServeMode && ep != "" {
		if pk := release.EmbeddedKey(); pk != nil {
			ch := strings.TrimSpace(os.Getenv("OLIVARES_UPDATE_CHANNEL"))
			if ch == "" {
				ch = release.ChannelStable
			}
			checker := updatecheck.NewChecker(updatecheck.Config{
				Endpoint: ep, Channel: ch, CurrentVersion: cfg.Version,
				InstallID: resolveInstallID(cfg.DataDir), PubKey: pk,
			}, 6*time.Hour)
			go checker.Run(ctx)
			updateStatusFn = checker.Latest
			updateRefreshFn = checker.Refresh
			log.Info("update check enabled (console indicator)", "endpoint", ep, "channel", ch)
		} else {
			log.Warn("OLIVARES_UPDATE_ENDPOINT set but this build embeds no release key; update checking disabled")
		}
	}

	keyCustody := api.KeyCustodyInfo{Keys: []api.KeyInfo{
		auditKey.custodyInfo("audit"),
		catalogKey.custodyInfo("catalog"),
		policyKey.custodyInfo("policy"),
		{
			Purpose:     "license",
			Algorithm:   "ed25519",
			Origin:      license.KeyOrigin(),
			Fingerprint: license.KeyFingerprint(),
		},
		sealerCustodyInfo("eventing", osGetenv(eventingSecretKeyEnv), eventingSealerPresent),
		sealerCustodyInfo("sso", osGetenv(federationSecretKeyEnv), federationSealerPresent),
		sealerCustodyInfo("secret-store", osGetenv(secretStoreKeyEnv), secretStoreSealerPresent),
	}}

	apiSrv, err := api.New(api.Options{
		Store: st, Authenticator: authr, Authorizer: authz, Signer: signer,
		SetupToken: setupTok, Logger: log, LogBroker: logBroker, Version: cfg.Version,
		// the boot-owned registry, shared with the bus collectors and the
		// audit checkpointer so /metrics is one exposition.
		Metrics:          reg,
		LicensePublicKey: licPub,
		KeyCustody:       keyCustody,
		BusStats:         busStats,
		TLSCertNotAfter:  cfg.TLSCertNotAfter,
		// the LIVE edition/license service supersedes the static boot blob — it
		// backs /v1/console/license and the live server-info status (hot-applied
		// install/renewal/expiry reflects without a restart). Pure edition plumbing.
		License: licSvc,
		// enterprise activation surface (nil in the community build ⇒ 501).
		Activation: actSvc,
		Modules:    set.all,
		// whoami reports authority a tenant-scoped grant confers, not just what the
		// ROLE confers. Same module that DECIDES the request through the ScopedAuthorizer
		// above, in its reporting capacity — one source, two capabilities, so the set the
		// console is handed cannot drift from the engine that produces the 403.
		UnconditionalGrants: set.gov.UnconditionalGrants(),
		// SSO federation: the managed config service backs the console SSO
		// endpoints AND resolves the live provider (store-driven, with the
		// env-configured provider as the no-config fallback). The default build has
		// no provider builder, so login answers 501 until rebuilt -tags enterprise.
		FederationService: fedSvc,
		// the runtime secret store backs the console/CLI secret CRUD endpoints
		// (/v1/console/secrets). Superadmin + AAL3 gated; secrets are sealed at rest
		// and only a non-secret hint is ever returned.
		SecretStore: secretStore,
		// the live source-reconfiguration surface backs the console/CLI source
		// CRUD and POST /v1/console/runtime/reload — add/remove/rotate connectors in
		// the running engine without a restart. Superadmin + AAL3 gated.
		SourceRoster: sourceReconcilerSvc,
		// the console connector-onboarding surface (same reconciler) backs the
		// descriptor catalog + sealed-credential CRUD + test under
		// /v1/console/connectors — it seals an inline credential into the secret store
		// and persists a reference-only source it applies live.
		ConnectorOnboarding: sourceReconcilerSvc,
		KnowledgeStatus:     set.knowledgeStatus,
		// OTA update-availability indicator (nil unless OLIVARES_UPDATE_ENDPOINT set).
		UpdateStatus:  updateStatusFn,
		UpdateRefresh: updateRefreshFn,
		// Wave 2: live effective-config projection and the shared support-bundle
		// safety policy. Both config callbacks are evaluated per request so
		// activation overlay changes and unknown env keys stay current.
		EffectiveConfig: func() []api.EffectiveConfigEntry {
			return effectiveConfigEntries(os.Environ(), osGetenv)
		},
		EffectiveConfigViolations: func() []string {
			return unknownConfigEnvKeys(os.Environ())
		},
		SupportBundleRedact:            securitymodule.RedactText,
		SupportBundleContainsSensitive: securitymodule.ContainsSecretOrPII,
		// Stage-2: in the HA leader-routing layout every healthy replica is
		// Pod-Ready (and therefore dialable), so application routes re-check
		// leadership and answer the retryable 503 not_leader on a standby. Off
		// unless the deployment opts in — a legacy HA layout is unchanged.
		LeaderRouteGate: haCfg.Gate,
		// enable console DR; dr_handler resolves an empty BackupDir to
		// <DataDir>/backups. The passphrase file — the CLI DR commands' own
		// $OLIVARES_DR_PASSPHRASE_FILE — is what lets the backup schedule run
		// unattended; without it the schedule runner refuses loudly instead of
		// writing a bundle it could not seal.
		DR: &api.DRConfig{DataDir: cfg.DataDir, EngineKind: string(eng), PassphraseFile: osGetenv("OLIVARES_DR_PASSPHRASE_FILE")},
		// Enable the collector→core ingest endpoint on the gRPC server (CB-1 option
		// C): pushed observations are authorized (ingest:write) and lifted onto the
		// bus through the runtime. Remote use requires --grpc-client-ca (mTLS).
		Ingest: rt,
		// OBS-03: the ingress trace-context extractor (continues the caller/mesh trace;
		// no-op when no collector — never breaks a request).
		Tracing: tracer,
		// OPS-5: inbound per-tenant/per-endpoint-class rate limiting. Always
		// non-nil (secure-by-default: production is rate-limited even unconfigured);
		// OLIVARES_RATELIMIT_CONFIG overlays tiers/quotas/mode/per-tenant assignment;
		// OLIVARES_RATELIMIT_STORE selects the shared Postgres buckets (above).
		RateLimit: rlCfg,
		// the residency registry, so org provisioning validates a tenant's
		// region pin against the configured regions (deny-closed).
		Residency: residencyReg,
		// privileged-session recording — every module route is gated and
		// captured through the recording module (deny-closed on recorded surfaces).
		Recorder: set.recorder,
		// Privileged login: pinned WebAuthn relying party
		// (zero value = per-request derivation) and the PIV/CAC client-cert
		// route (nil = honest 501 seam, no elevation).
		WebAuthn: loadWebAuthnRP(osGetenv, log),
		PIV:      pivCfg,
		// /metrics access-control (env-configurable):
		// OLIVARES_METRICS_TOKEN = static bearer token for scrape auth;
		// OLIVARES_METRICS_ALLOWED_CIDRS = comma-separated CIDRs. Unset ⇒
		// unauthenticated (network-level controls only).
		MetricsAuth: loadMetricsConfig(osGetenv),
		// AuthZEN/access-review surface EXPOSURE controls (env-configurable):
		// OLIVARES_AUTHZEN_DISABLED / _SEARCH_DISABLED / _EXPORT_DISABLED toggle the
		// whole surface, the reverse-query searches, or the sealed export; and
		// OLIVARES_AUTHZEN_ALLOWED_CIDRS confines it to an intra-cluster network. Unset ⇒
		// fully enabled (the per-call bearer + authz:read/authz:admin + AAL3 gates apply).
		AuthZen: loadAuthZenConfig(osGetenv),
	})
	if err != nil {
		_ = st.Close()
		return nil, err
	}

	// late-bind the engine's API handler into the OUTBOUND ApprovalGate
	// bridge (it was constructed in buildModules, before the API server existed). The
	// gates are only ever CALLED at request time — after Start, below — so binding here
	// is in time; a call before binding fails closed. nil when no bridge is configured.
	if set.approvalBridge != nil {
		set.approvalBridge.useHandler(apiSrv.Handler())
	}

	// subscribe the FinOps→upstream-cap backstop to the bus, AFTER the approval
	// bridge's handler is bound (its governed actuator gates through that bridge). nil
	// (opt-in OFF / unprovisioned) is a no-op. A subscribe error leaves the backstop
	// inactive — never a boot failure — and the finops_budget_cap finding still stands.
	if set.finopsBackstop != nil {
		if err := set.finopsBackstop.subscribe(bus); err != nil {
			log.Warn("finops-backstop: bus subscription failed; upstream-cap backstop inactive", "err", err)
		}
	}

	// subscribe the enterprise ITSM/ChatOps governance close-loop (build-tag
	// gated; nil and a no-op in the default AGPL build) to finding.reported, so
	// governance lifecycle findings carry their state onto the correlated PagerDuty/
	// Opsgenie incident. Opt-in + fail-inert; a subscribe failure leaves it inactive,
	// never a boot failure.
	// Circuit-breaker. Constructed once and held on the engine
	// because TWO consumers need the SAME instance: the finding rail drives OnFinding
	// below, and the inference proxy consults State() on every request. nil in the
	// community build, so both consumers stay inert and behavior is unchanged.
	circuitBreaker := newCircuitBreakerEngine(osGetenv, circuitBreakerDeps{Data: data, Gov: set.gov}, log)
	subscribeCircuitBreaker(circuitBreaker, bus, log)
	subscribeIncidentCloseLoop(ctx, osGetenv, bus, log)

	// subscribe the enterprise AI threat-intel feed engine (build-tag gated;
	// nil and a no-op in the default AGPL build). It turns curated signed-feed
	// content into ADDITIVE findings (agentic signatures over guardrail.observed,
	// MCP reputation over finding.reported, model-lifecycle over cost.sampled). Opt-in
	// + fail-inert; it never alters the open engine's decisions.
	subscribeThreatIntel(ctx, osGetenv, bus, log)

	// late-bind the engine handler into the deploy IdentityBinder (the firm
	// per-agent NHI binding via in-process) and the drift loop, and register the
	// drift loop on the runtime's OWN periodic scheduler (before Start, like the roster
	// sync). Both were constructed in buildModules; nil when unconfigured (the module
	// keeps its degraded-attribution / no-loop defaults). A drift call before binding
	// fails closed (nil handler => the next tick runs).
	if set.deployBinder != nil {
		set.deployBinder.useHandler(apiSrv.Handler())
	}
	if set.deployDrift != nil {
		set.deployDrift.useHandler(apiSrv.Handler())
		if err := set.deployDrift.register(rt); err != nil {
			log.Warn("deploy-drift: could not register the drift loop on the scheduler; drift detection disabled", "err", err)
		}
	}

	// the retention sweep (contract §6) and the continuous ledger archival
	// (§8.5), on the runtime's OWN periodic scheduler, registered before Start.
	// Both hold the real store and are leader-gated per tick (the checkpointer
	// pattern), so a promoted standby picks them up on its next tick. nil = the
	// operator disabled the sweep / configured no archive sink (each warned).
	if sweep := newRetentionSweepLoop(osGetenv, st, set.compliance, log); sweep != nil {
		if err := sweep.register(rt); err != nil {
			log.Warn("retention-sweep: could not register the sweep loop on the scheduler; periodic retention disposition disabled", "err", err)
		}
	}
	// the report schedule pump — fires DUE scheduled reports per tenant
	// and records each run. nil in the community build (the reporting scheduler is
	// not wired) or when the operator disabled the cadence — no rug-pull.
	if pump := newReportSchedulePump(osGetenv, st, set.reporting, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("reporting-schedule: could not register the schedule pump on the scheduler; scheduled reports disabled", "err", err)
		}
	}
	// the console backup schedule pump — evaluates the PERSISTED DR
	// schedule each tick and runs a due backup through the exact runBackup path
	// the console trigger uses, then applies retention and records the run.
	if pump := newDRSchedulePump(osGetenv, st, apiSrv, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("dr-schedule: could not register the backup schedule pump on the scheduler; scheduled backups disabled", "err", err)
		}
	}
	archCfg := loadAuditArchiveConfig(osGetenv, log)
	arch, err := newAuditArchiveLoop(archCfg, st, signer, auditKey.priors, log)
	if err != nil {
		return nil, fmt.Errorf("load audit archive operator config: %w", err)
	}
	if arch != nil {
		if err := arch.register(rt); err != nil {
			log.Warn("audit-archive: could not register the archival loop on the scheduler; continuous ledger archival disabled", "err", err)
		}
		// the long-horizon legal-hold orchestrator reconciles object-lock legal
		// holds on the SAME archive sink with each tenant's active engine legal holds.
		// nil in the community build / when the add-on is not opted in (newLongHorizonHold
		// returns nil there ⇒ newLongHorizonHoldLoop returns nil) — no rug-pull.
		if recon := newLongHorizonHold(osGetenv, arch.sink, set.compliance, log); recon != nil {
			if loop := newLongHorizonHoldLoop(osGetenv, st, recon, log); loop != nil {
				if err := loop.register(rt); err != nil {
					log.Warn("audit-legalhold: could not register the reconciliation loop on the scheduler; archive legal-hold reconciliation disabled", "err", err)
				}
			}
		}
	}
	// bind the enterprise RTBF crypto-shred coordinator's evidence ports —
	// the legal-hold checker over the compliance module and the SAME WORM archive
	// sink the ledger archival writes to (nil when archival is off; the coordinator
	// then blocks WORM-coordinated shreds, deny-closed). A no-op in the default
	// build (wire_noenterprise.go) and when the coordinator is not configured.
	var archSink audit.ArchiveSink
	if arch != nil {
		archSink = arch.sink
	}
	bindCryptoShredPorts(set.rtbfCoordinator, archSink, archCfg.sink, set.compliance, log)

	// the eventing dispatch pump (retry cadence, crash recovery, retention
	// pruning), on the runtime's OWN periodic scheduler, leader-gated per tick
	// like the sweeps above. nil = the operator disabled it (warned loudly).
	if pump := newEventingPump(osGetenv, st, set.eventing, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("eventing-dispatch: could not register the pump on the scheduler; webhook retries and pruning disabled", "err", err)
		}
	}
	// K1/K2: recover committed WorkOutbox rows and expired WorkLease owners after
	// a process restart. Only this leader-gated cadence makes recovery independent
	// of receiving another command.
	if pump := newWorkOutboxPump(osGetenv, st, set.sessions, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("sessions-work-outbox: could not register the pump; durable work events remain pending", "err", err)
		}
	}

	// the orchestration cadence pump — runs the cadence-miss anti-evasion
	// scan per business tenant so a silent schedule raises its Finding without
	// anyone reading the console. Same posture as the eventing pump: runtime
	// scheduler, leader-gated per tick. nil = the operator disabled it (warned).
	if pump := newOrchCadencePump(osGetenv, st, set.orchestration, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("orchestration-cadence: could not register the pump on the scheduler; unattended cadence-miss detection disabled", "err", err)
		}
	}

	// the workflow-run pump — advances running DAG workflows (waits,
	// approval-gate polling, kill-switch-frozen resumes, crash-orphaned claims)
	// per business tenant. Same posture: runtime scheduler, leader-gated per
	// tick. nil = the operator disabled it (warned).
	if pump := newOrchWorkflowPump(osGetenv, st, set.orchestration, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("orchestration-workflow: could not register the pump on the scheduler; wait/approval-gate workflow steps will not advance in the background", "err", err)
		}
	}

	// the durable notification-outbox pump — delivers enqueued notifications with
	// retry/backoff and dead-letters on exhaustion, out of band of the bus handler. Same
	// posture as the eventing pump: runtime scheduler, leader-gated per tick. With
	// routing now enqueue-only, this pump is what actually delivers, so a disable warns.
	if pump := newNotifyPump(osGetenv, st, set.notify, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("notify-dispatch: could not register the pump on the scheduler; notifications will NOT be delivered", "err", err)
		}
	}

	// the SIEM ledger-forward pump — walks each tenant's tamper-evident audit
	// ledger from a cursor and forwards new records to SIEM control towers over the
	// eventing engine. Same posture as the eventing pump: runtime scheduler,
	// leader-gated per tick. nil = the module is unwired or the operator disabled it.
	if pump := newLedgerForwardPump(osGetenv, st, set.siemforward, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("siem-ledger-forward: could not register the pump on the scheduler; the audit ledger will not reach SIEM towers", "err", err)
		}
	}

	// late-bind the OPERATE governance onto module II (sessions) before Start —
	// the kill-switch StopGate + active-termination sweep, the budget/HITL/PEP
	// LaunchGate, and the I/O Recorder. Module II was constructed first (its live
	// read-model backs the evals monitor), so its governance gates late-bind here, now
	// that the store, the approval bridge and FinOps are all live. stopDeny is the
	// shared throttled deny recorder (the engine reuses the same instance below).
	stopDeny := newStopDenyRecorder(st, log)
	wireSessionGovernance(set, st, stopDeny, bus, osGetenv, log)

	// the guardian sweep pump — executes guardian containments whose HITL
	// approval landed (the human's click takes effect within one tick). Same
	// posture as the eventing pump: runtime scheduler, leader-gated per tick.
	if pump := newGuardianPump(osGetenv, st, set.gov, log); pump != nil {
		if err := pump.register(rt); err != nil {
			log.Warn("guardian-sweep: could not register the pump on the scheduler; approved containments will not auto-execute", "err", err)
		}
	}

	// Optionally provision a synthetic demo estate (org + agents + a seed source)
	// BEFORE Start, so its agent-origin edges attribute and its observations flow
	// through the real bus. Demo-only; never touches a real estate's data.
	var demoTenant model.TenantID
	if cfg.DemoSeed {
		t, derr := seedDemoEstate(ctx, st, rt, time.Now())
		if derr != nil {
			_ = st.Close()
			return nil, fmt.Errorf("seed demo estate: %w", derr)
		}
		demoTenant = t
	}

	// CB-1 — wire the real ingestion as a PRODUCTION caller. Identity roster providers
	// are resolved from OLIVARES_SOURCES_CONFIG BEFORE rt.Start (unchanged: the roster
	// is read-once boot config, requires-restart to change). The OBSERVATION SOURCES,
	// since live in the DURABLE ROSTER (the sealed store), not the file: the
	// file's `sources[]` is imported once as a bootstrap seed (below), then the table
	// is authoritative and the live reconciler — run just after Start — wires it. An
	// unconfigured/un-embedded source or an empty roster warns honestly.
	if set.deferredSecrets != nil {
		set.deferredSecrets.rt = rt
		set.deferredSecrets.connectorDir = connectorDir
	}
	set.deferredSecrets.openAll(ctx, secretResolver, log)
	seedSourceRosterIfEmpty(ctx, sourceStore, srcCfg, log)
	wireRoster(ctx, rt, set.gov, set.wif, srcCfg, secretResolver, log)

	// Start the runtime: it opens outputs, Inits+subscribes+Starts modules, then fires
	// the periodic scheduler (roster sync). Observation SOURCES are wired by the
	// reconciler immediately AFTER Start (below) — outputs and modules have already
	// subscribed, so the ordering invariant "no early event is missed" holds, and the
	// live-wiring path is identical to a later reload. A module/source that fails to
	// open is marked failed and skipped (it shows in rt.Status()), never aborting.
	// a roster READER stops here. Starting the runtime would open outputs and
	// modules, and the reconcile below would prepare, open and wire every enabled
	// connector — network calls and subprocesses on behalf of a command that was
	// asked to print or preview. The engine object is still usable for what those
	// commands need (the store, the source store, the reconciler's prepare path);
	// it simply is not running.
	if cfg.NoIngest {
		log.Debug("boot: roster-read boot — the runtime was not started and no source was wired")
	} else {
		if err := rt.Start(ctx); err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("start runtime: %w", err)
		}

		// the initial source reconcile — wire every enabled roster row into the
		// running engine. Identical to a later live reload, so boot and reload converge
		// (the ReloadActivePDP precedent). Deny-closed per source; a per-source failure
		// is logged, never aborts the boot.
		if report, rerr := sourceReconcilerSvc.reconcile(ctx); rerr != nil {
			log.Warn("ingest: initial source reconcile failed; the engine starts with no live sources until a reload succeeds", "err", rerr)
		} else {
			log.Info("ingest: sources wired from the durable roster",
				"added", len(report.Added), "rejected", len(report.Rejected))
			for _, rej := range report.Rejected {
				log.Warn("ingest: source not wired (deny-closed)", "name", rej.Name, "reason", rej.Reason)
			}
		}
	}

	// a SEALED operator config that could not be opened (revoked KEK, KMS
	// outage, custody typo) is a custody failure, not a missing config — the
	// loaders degraded honestly above, but the boot must fail closed rather than
	// run those subsystems silently unconfigured (docs/SECURITY-HARDENING.md: never a silent gap).
	if scErr := sealedConfigFailure(); scErr != nil {
		return nil, fmt.Errorf("key custody: %w (a sealed config that cannot be opened fails the boot; unseal or fix the KEK to proceed)", scErr)
	}

	// advisory — surface any operator config that carried CLEARTEXT secrets
	// unsealed (one WARN per file; never fatal). See warnUnsealedSecretConfigs.
	warnUnsealedSecretConfigs(log)

	// a successful Postgres Open above proves every registered tenant table
	// has ENABLE + FORCE RLS + tenant policy (the store conformance self-test). With
	// the privileged-role opt-out disabled, it also proves the app role cannot bypass
	// RLS. Replica count and load-balancer state are external and cannot be attested
	// by this process, so this consolidated boot-report hook checks only the locally
	// observable T2 posture and remains advisory.
	warnUnsupportedProductionPosture(log, eng, eng == store.EnginePostgres && !cfg.AllowPrivilegedDBRole)

	// unknown OLIVARES_* keys are an operator-visible contract violation,
	// but boot remains backward-compatible. Report every key in one consolidated
	// warning; `config effective --strict` is the CI/pre-production hard gate.
	if unknown := unknownConfigEnvKeys(os.Environ()); len(unknown) > 0 {
		log.Warn("unrecognized OLIVARES_* env keys ignored", "keys", unknown)
	}

	bootOK = true // success: the engine's Close() now owns tracer/bus shutdown
	// Stage-2: start the HA label resync loop now that boot cannot fail. It
	// converges the pod label onto live leadership every tick: it republishes after a
	// transient apiserver failure, restores a label edited out of band, and — since
	// the store's elector seam has no demotion callback (and giving a plain Postgres
	// elector Kubernetes-shaped callbacks would couple the cross-platform leadership
	// contract to the orchestrator) — demotes the label when this node loses the
	// lock. Its context is owned by the engine and canceled by Close.
	var haStop context.CancelFunc
	if haPublisher != nil {
		haCtx, cancel := context.WithCancel(context.Background())
		haStop = cancel
		go haPublisher.run(haCtx, st.Leader().IsLeader)
	}

	return &engine{
		store: st, rt: rt, signer: signer, authr: authr, authz: authz,
		setupTok: setupTok, api: apiSrv, tracer: tracer, dataDir: cfg.DataDir, log: log,
		logBroker:  logBroker,
		demoTenant: demoTenant, connectorDir: connectorDir, vectorIndex: set.vectorIndex, knowledgeMod: set.knowledge, sessionsMod: set.sessions,
		protocolBindingReconciler: set.protocolBindingReconciler,
		approvalBridge:            set.approvalBridge, policyEval: policyEval, scopedGrants: set.gov.ScopedGrants(), nhiEnforcer: set.gov,
		killSwitch: set.gov, stopDeny: stopDeny, pinVerifier: set.pinVerifier,
		circuitBreaker: circuitBreaker,
		models:         set.models, finops: set.finops, inferenceProxy: set.inferenceProxy, residencyReg: residencyReg,
		auditPriors: auditKey.priors, pivConfig: pivCfg, fedSvc: fedSvc, secretStore: secretStore,
		sourceStore: sourceStore, sourceReconciler: sourceReconcilerSvc, licenseService: licSvc,
		notifyDispatcher: set.deferredSecrets.notify, secretResolver: secretResolver,
		bus: bus, metrics: reg, rlStore: rlStore, wifBroker: set.wifBroker,
		haPublisher: haPublisher, haStop: haStop, haGate: haCfg.Gate,
	}, nil
}

func warnUnsupportedProductionPosture(log *slog.Logger, eng store.Engine, effectiveRLSAttested bool) {
	if eng == store.EnginePostgres && effectiveRLSAttested {
		return
	}

	if eng == store.EngineSQLite {
		log.Warn("running on SQLite single-node — this is the evaluation/pilot profile, not the supported production profile (T2: HA active-passive, Postgres + RLS FORCE)",
			"topology", "T1", "store", eng, "rls_force", false)
		return
	}

	log.Warn("Postgres privileged-role opt-out enabled — effective RLS FORCE is not attested; this is not the supported production profile (T2: HA active-passive, Postgres + RLS FORCE)",
		"store", eng, "effective_rls_attested", effectiveRLSAttested)
}

// seedSourceRosterIfEmpty performs the ONE-TIME bootstrap import of the operator's
// file `sources[]` into the durable roster. It runs only when the roster is
// empty, so an existing deployment's file config migrates into the store on the
// first boot after upgrade, and thereafter the table is the source of truth (the
// file's sources are ignored — a stable, honest migration). It never fails the
// boot: a seed error is logged and the engine proceeds (the roster simply stays
// as it was). Identity/Documents/ConnectorTrust are NOT seeded — they remain
// file-config (requires-restart to change).
func seedSourceRosterIfEmpty(ctx context.Context, store *auth.SourceStore, cfg sourcesConfig, log *slog.Logger) {
	existing, err := store.List(ctx, auth.GlobalSourceScope)
	if err != nil {
		log.Warn("ingest: could not read the durable source roster; skipping the bootstrap seed", "err", err)
		return
	}
	if len(existing) > 0 {
		if len(cfg.Sources) > 0 {
			log.Info("ingest: durable source roster is authoritative; the file's sources[] is ignored (edit via the console/CLI or POST /v1/console/runtime/reload)",
				"roster_rows", len(existing), "file_sources", len(cfg.Sources))
		}
		return
	}
	if len(cfg.Sources) == 0 {
		return
	}
	// A system actor for the import audit (no human triggered it). It is now
	// ATTRIBUTABLE: the ledger records the path and the host, and classifies the
	// event as system rather than as a human operator (B-12). The previous
	// literal recorded the subject "user:" — the same string the CLI's own
	// privileged writes produced.
	actor, aerr := auth.NewSystemOperator("boot/seed", "seeding the source roster from the boot config")
	if aerr != nil {
		log.Error("boot: cannot attribute the roster seed; refusing to seed unattributed", "err", aerr)
		return
	}
	defs := make([]model.SourceDef, 0, len(cfg.Sources))
	for _, s := range cfg.Sources {
		defs = append(defs, sourceDefFromSpec(s))
	}
	// Atomic: either every file source migrates into the roster, or none does and the
	// roster stays empty so the NEXT boot retries the whole seed — never a silent,
	// partially-migrated roster (docs/SECURITY-HARDENING.md: never a silent gap). A malformed entry
	// fails loudly and names itself; fix the file and reboot.
	seeded, err := store.SeedAll(ctx, actor, defs)
	if err != nil {
		log.Error("ingest: could not seed the durable source roster from the operator file; NO sources were imported and the roster stays empty (it will retry next boot). Fix the offending entry in OLIVARES_SOURCES_CONFIG.",
			"err", err)
		return
	}
	if seeded > 0 {
		log.Info("ingest: seeded the durable source roster from the operator file (one-time bootstrap; the table is now authoritative)", "seeded", seeded)
	}
}

// Close stops the runtime and closes the store (called after the HTTP/gRPC
// servers have drained). It also removes the extracted plugin binaries.
func (e *engine) Close() error {
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Stage-2: stop the HA label resync loop and hand the leader role back
	// before the store (and with it the election lock) closes, so the leader Service
	// drops this pod immediately rather than after the endpoint controller notices.
	if e.haStop != nil {
		e.haStop()
	}
	if e.haPublisher != nil {
		e.haPublisher.haShutdownLabel()
	}
	_ = e.rt.Stop(stopCtx)
	// Close the boot-owned bus AFTER the runtime stopped every subscriber (the
	// runtime never closes an injected bus). On the NATS bridge this also
	// flushes the node's last outbound publishes.
	if e.bus != nil {
		_ = e.bus.Close()
	}
	// Flush and stop the OTLP exporters (no-op when tracing is disabled).
	if e.tracer != nil {
		_ = e.tracer.Shutdown(stopCtx)
	}
	if e.connectorDir != "" {
		_ = os.RemoveAll(e.connectorDir)
	}
	// Close the ANN backend's connection pool; its DSN/pool is independent of
	// the core store, so closing the store does not release it.
	if e.vectorIndex != nil {
		_ = e.vectorIndex.Close()
	}
	// The shared rate-limit bucket pool is likewise independent.
	if e.rlStore != nil {
		_ = e.rlStore.Close()
	}
	// release the WIF broker's lazily-dialed SPIRE Workload API connection (nil-safe;
	// a no-op when the broker never minted a credential).
	if e.wifBroker != nil {
		_ = e.wifBroker.Close()
	}
	return e.store.Close()
}

// legacyDataDirName is the RELATIVE directory the engine defaulted to before.
// It survives only as a compatibility lookup: an operator who already has a real
// installation there keeps using it (see defaultDataDir).
const legacyDataDirName = "olivares-data"

// defaultDataDir resolves the data directory when nobody named one.
//
// THE DEFECT THIS CLOSES. This function used to return the RELATIVE literal
// "olivares-data". Every command that reaches boot() without --data-dir therefore
// resolved it against the process's working directory, and `olivares serve` — the
// first command anyone runs, and the one command guards deliberately do NOT
// restrain, because initializing IS its job — minted four private keys at 0600 and a
// multi-megabyte store right there. Run it once inside a clone (which is where
// somebody trying the product out is standing) and `git status` listed the lot:
// private key material one `git add -A` from being published, in a repository that
// may well be public. `.gitignore` in THIS repository would have covered this
// repository and nobody else's.
//
// The fix is not to demand configuration. Booting with none, in about a second, is
// something this product is good at and measured as a virtue — it stays. What
// changes is WHERE "no configuration" points: a per-user directory, which is what
// the XDG base-directory spec exists to name, instead of wherever the shell was.
//
// Precedence, and each step is a deliberate answer:
//
//  1. OLIVARES_DATA_DIR — an explicit statement, honored verbatim, as before.
//  2. A REAL installation at ./olivares-data — an operator who ran an older build
//     has their keys and store there. Silently starting a second, empty installation
//     elsewhere would look exactly like data loss, so the legacy path wins while it
//     holds an installation. The question is installationExistsAt's, not "does the
//     directory exist": an EMPTY ./olivares-data is not an installation and must not
//     drag the default back into the working directory.
//  3. $XDG_DATA_HOME/olivares, else $HOME/.local/share/olivares.
//  4. Nothing usable — refuse, and say which flag to pass. The one answer that is
//     never correct here is falling back to the working directory: that is the
//     defect. A container or a systemd unit with no HOME is exactly the deployment
//     that must be told to name its data directory, and every image and unit we ship
//     already passes one.
func defaultDataDir() (string, error) {
	if d := os.Getenv("OLIVARES_DATA_DIR"); d != "" {
		return d, nil
	}
	if installationExistsAt(legacyDataDirName) {
		return absOrSame(legacyDataDirName), nil
	}
	if root := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); root != "" && filepath.IsAbs(root) {
		return filepath.Join(root, "olivares"), nil
	}
	// The home directory must be ABSOLUTE, and that is checked rather than assumed:
	// os.UserHomeDir returns $HOME verbatim without validating it, so HOME="." (or a
	// path with surrounding whitespace) would have produced ".local/share/olivares"
	// — a relative default, which is the entire defect this function exists to
	// remove. Caught by the sol-max contrast; the tests only used absolute or empty.
	if home, err := os.UserHomeDir(); err == nil {
		if home = strings.TrimSpace(home); filepath.IsAbs(home) {
			return filepath.Join(home, ".local", "share", "olivares"), nil
		}
	}
	return "", exitcode.New(exitcode.Usage, errors.New(
		"cannot choose a data directory: no OLIVARES_DATA_DIR, no existing installation, and no "+
			"home directory to fall back on. Say where the data lives with --data-dir (or "+
			"OLIVARES_DATA_DIR): it holds private signing keys and the store, so it is never "+
			"placed in the current working directory by default"))
}

// defaultPostgresMaxConns is the conservative per-node application-pool cap when
// OLIVARES_DB_MAX_CONNS is unset. It leaves headroom under a default 100-connection
// Postgres for two HA nodes plus their lock/admin connections.
const defaultPostgresMaxConns = 20

// postgresMaxConns resolves the Postgres application-pool cap from
// OLIVARES_DB_MAX_CONNS, falling back to defaultPostgresMaxConns. A non-positive
// or unparseable value logs a warning and uses the default rather than silently
// running unbounded.
func postgresMaxConns(getenv func(string) string, log *slog.Logger) int {
	raw := strings.TrimSpace(getenv("OLIVARES_DB_MAX_CONNS"))
	if raw == "" {
		return defaultPostgresMaxConns
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Warn("OLIVARES_DB_MAX_CONNS is not a positive integer; using the default", "value", raw, "default", defaultPostgresMaxConns)
		return defaultPostgresMaxConns
	}
	return n
}
