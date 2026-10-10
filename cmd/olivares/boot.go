// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/agenttoolsapi"
	"github.com/olivaresai/olivares/cmd/olivares/internal/githostdiff"
	"github.com/olivaresai/olivares/cmd/olivares/internal/inferencepep"
	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/ratelimit"
	"github.com/olivaresai/olivares/core/api/ratelimit/pgstore"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/metrics"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/modulespec"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/release"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/suspension"
	"github.com/olivaresai/olivares/core/updatecheck"
	"github.com/olivaresai/olivares/core/webaddr"
	"github.com/olivaresai/olivares/modules/gitpublish"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/knowledge"
	securitymodule "github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/accounthome"
	"github.com/olivaresai/olivares/sdk"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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
	// Only the installed package serve path supplies this root-owned request file.
	upgradeSnapshotRequest string
	// LoginTrustedProxies is already resolved by the serve family. A nil value
	// resolves the environment for direct boot callers; a zero set trusts none.
	LoginTrustedProxies *auth.TrustedLoginProxies
	DataDir             string
	// dataDirReady attaches quickstart's log after validation and directory setup,
	// before store initialization can fail. Other boot callers leave it nil.
	dataDirReady func(string) error
	// Non-nil only for quickstart: provision PostgreSQL or reuse its saved DSNs.
	quickstartPostgres  *string
	storeEngineExplicit bool
	Engine              string
	DSN                 string
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
	// goroutine that outlives the command).. Nor may they run the
	// serve-owned work: Cedar reactivation, session runtime recovery and the
	// recovery of launches waiting for an approval.
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
	// workOutboxPumpPrepared is a test-only seam to wrap the nudge drain before
	// registration starts its goroutine. Production leaves it nil.
	workOutboxPumpPrepared func(*workOutboxPump)
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
	// ApplyModuleProfile builds the node's module profile (moduleprofile.go): a
	// module outside it is dormant. Only the serving engine sets it; every other
	// boot (CLI verbs, tests) runs every module, as before profiles existed.
	ApplyModuleProfile bool
	// TLSCertNotAfter reads the certificate currently served by the listener.
	// The serve command supplies the live reload-aware accessor; non-listener
	// commands leave it nil.
	TLSCertNotAfter func() (time.Time, bool)
	// PublicAddr is the operator-declared browser address of this console, already
	// parsed and canonicalized by the caller. The zero value means none was
	// declared. Commands that never serve a console leave it zero, and the WebAuthn
	// relying party then falls back exactly as it always has.
	PublicAddr webaddr.Address
	// PublicAddrSource is WHICH input supplied PublicAddr, so the boot log names
	// the flag when it was a flag. Naming the environment key unconditionally sent
	// an operator to the wrong file.
	PublicAddrSource publicAddrSource

	// Restart is the serving engine's self-restart (selfrestart.go); nil for a
	// CLI command, which then offers no restart to the edition.
	Restart *selfRestart
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
		"olivares.db", "audit-signing.key", "catalog-signing.key", "policy-signing.key", memoryPortabilityKeyFile,
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
	sessionHooks     *sessionHookCredentials
	sessionMCP       *mcpManagement
	agentTools       *agenttoolsapi.Module
	gatewayConfig    *mcpgateway.Config
	editionResources []io.Closer
	store            store.Store
	rt               *runtime.Runtime
	signer           *audit.Signer
	authr            *auth.Authenticator
	authz            *auth.Authorizer
	setupTok         *secure.SetupToken
	api              *api.Server
	tracer           *obstrace.Provider
	dataDir          string
	log              *slog.Logger
	// webAuthn is the relying party this boot RESOLVED, kept so the startup panel
	// can describe what will actually happen at a passkey prompt instead of
	// guessing from the address. The panel used to promise a localhost ceremony
	// whatever the plan was, including for a deployment whose every leg answers
	// 503 and for a pin whose origins exclude localhost.
	webAuthn webAuthnPlan
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
	// communicationPump is the registered local outbox pump (K1/K2 lanes plus the
	// gated K3 lane). Close stops its readiness witness before the runtime stops.
	communicationPump *workOutboxPump
	// jobsNotRunning lists the jobs this node composes but cannot run, for
	// server-info; estateEnumerable is whether the store can enumerate every
	// tenant (jobsnotrunning.go).
	jobsNotRunning   *jobsNotRunning
	estateEnumerable bool
	// communicationComposition retains the exact adapters bound into sessions.
	// It is not an alternate authority path; lifecycle diagnostics and defensive
	// composition tests use it to replace one port and restore that same instance.
	communicationComposition *communicationComposition
	// workSink is the durable Eventing intake the sessions outbox publishes
	// through, retained like sessionsMod so the composition's recovery tests can
	// reproduce the crash window between a capture and its settlement with the
	// real sink instead of a double.
	workSink workEventSink
	// moduleProfile is the module profile this start built its modules with
	// (moduleprofile.go); serve reconciles it with the deployment settings.
	moduleProfile moduleProfile
	// protocolBindingReconciler is the late-bound REST multiplexer. A2A is
	// installed during module composition; the MCP adapter is added only after
	// the configured durable store and real upstream have both been constructed.
	protocolBindingReconciler *protocolBindingReconcileMux
	// approvalBridge is the OUTBOUND ApprovalGate bridge, exposed here so the
	// Agent-protocols gateway (MCP tools/call HITL) and the Claude Code hooks
	// PEP can reuse the SAME governed approval path instead of opening a second one. nil
	// when no bridge is configured.
	approvalBridge  *approvalBridge
	engineApprovals *governance.EngineApprovals
	// gitpublish is the running publication module, which the session publish
	// proposal tools call after a person approves; nil when it does not run.
	gitpublish *gitpublish.Module
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
	models         inferencepep.ModelAccessGate
	finops         budgetChecker
	inferenceProxy inferencepep.PolicySource
	residencyReg   *residency.Registry
	// contentFirewall is the moduleSet's Messages proxy attachment recorder, written once by
	// buildClaudeMessagesProxyServer (contentfirewallstatus.go).
	contentFirewall *messagesInspectorBinding
	// pivConfig is the PIV/CAC route config (nil = unconfigured). The serve
	// command arms the HTTP listener with it (optional client-cert request) so
	// the handlers can read the verified peer certificate.
	pivConfig *auth.PIVConfig
	// fedSvc is the managed SSO config service, held so the enterprise build's
	// routes (the httpRoutes edition port) use the SAME live federation service login uses.
	// nil only in embedders that skip it.
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
	// census is the opened store's composition census: its closed registry and
	// the readiness verdict computed at open (erasurecensus.go). nil when the
	// store does not expose one.
	census store.CompositionCensus
	// declared are the modules this composition declares to the census, each
	// with its retirement step (erasurecensus.go).
	declared []declaredModule
	// retirementPump drives pending retirements on the active writer
	// (retirementpump.go).
	retirementPump *retirementPump
	// retirementStop ends the pump's loop; nil when it was not started.
	retirementStop context.CancelFunc
	// legacyAdoptStop ends the naming of legacy provider profiles; nil when none was started.
	legacyAdoptStop context.CancelFunc
	// standing is the standing reader every fenced writer reads through
	// (standingport.go).
	standing *standingPort
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
	if err := checkCMEKInstall(cfg.DataDir); err != nil {
		return nil, err
	}
	if cfg.Engine == "" || cfg.Engine == "sqlite" {
		if err := checkCMEKSQLiteStoreRef(ctx, cfg.DSN); err != nil {
			return nil, err
		}
	}
	b := &bootState{cfg: cfg}
	defer b.unwind()

	if err := b.configure(ctx); err != nil {
		return nil, err
	}
	if err := b.loadSigningCustody(ctx); err != nil {
		return nil, err
	}
	if err := b.buildRuntime(ctx); err != nil {
		return nil, err
	}
	if err := b.openStore(ctx); err != nil {
		return nil, err
	}
	if err := b.bindIdentityAndLeadership(ctx); err != nil {
		return nil, err
	}
	if err := b.bindServices(ctx); err != nil {
		return nil, err
	}
	if err := b.bindSources(ctx); err != nil {
		return nil, err
	}
	if err := b.buildAPI(ctx); err != nil {
		return nil, err
	}
	if err := b.bindJobs(ctx); err != nil {
		return nil, err
	}
	if err := b.bindSessionGovernance(ctx); err != nil {
		return nil, err
	}
	b.composeEngine()
	if err := b.startRuntime(ctx); err != nil {
		return nil, err
	}
	return b.finish(ctx)
}

// bootState carries values between the ordered startup phases. It is private to
// one boot; the returned engine retains ownership of the running components.
type bootState struct {
	cfg                         bootConfig
	loginProxies                *auth.TrustedLoginProxies
	log                         *slog.Logger
	logBroker                   *api.LogBroker
	webAuthn                    webAuthnPlan
	storeIsRemote               bool
	quickstartFresh             *postgresInit
	eng                         store.Engine
	dsn                         string
	maxConns                    int
	residencyReg                *residency.Registry
	communicationActivation     communicationActivationConfig
	communicationSealer         *communicationContentSealer
	communicationCursorKeyring  *sessions.CommunicationCursorTokenKeyring
	communicationCursorStatus   communicationCursorKeyringStatus
	freshDirectoryInitialized   bool
	prepareFreshCommunication   func()
	preserveFreshModuleProfile  func() error
	restorePublication          *coreengine.BootPublication
	custody                     coreengine.CustodyRequirement
	auditKey                    loadedSigningKey
	catalogKey                  loadedSigningKey
	policyKey                   loadedSigningKey
	memoryKey                   loadedSigningKey
	memoryKeyErr                error
	custodyObserved             coreengine.CustodyObservation
	signer                      *audit.Signer
	tracer                      *obstrace.Provider
	bootOK                      bool
	legacyAdoptStop             context.CancelFunc
	bus                         eventbus.Bus
	gatedBus                    injectGatedBus
	reg                         *metrics.Registry
	busStats                    eventbus.StatsProvider
	srcCfg                      sourcesConfig
	set                         moduleSet
	profile                     moduleProfile
	running                     moduleSet
	localCommunication          bool
	rt                          *runtime.Runtime
	declared                    []declaredModule
	storeSchema                 func(store.ExtensionRegistry) error
	storeQuiesce                func(context.Context) error
	st                          store.Store
	census                      store.CompositionCensus
	sessionRuntimeRecoveryStore store.Store
	sessionRuntimeRecoveryData  api.ModuleData
	enumerable                  bool
	notRunning                  *jobsNotRunning
	data                        api.ModuleData
	authr                       *auth.Authenticator
	standing                    *standingPort
	retirement                  *retirementPump
	communicationComposition    *communicationComposition
	loginCap                    loginCapabilityBoot
	haCfg                       haLeaderConfig
	haPublisher                 *haLeaderPublisher
	policyEval                  auth.PolicyEvaluator
	authz                       *auth.Authorizer
	setupTok                    *secure.SetupToken
	eventingSealerPresent       bool
	federationSealerPresent     bool
	fedSvc                      *auth.FederationService
	secretStoreSealerPresent    bool
	secretStore                 *auth.SecretStore
	secretResolver              *secret.Resolver
	settings                    *productSettings
	tracingSettings             *tracingSettingsService
	editionResources            []io.Closer
	sourceStore                 *auth.SourceStore
	connectorDir                string
	sourceReconcilerSvc         *sourceReconciler
	licSvc                      *licenseService
	pivCfg                      *auth.PIVConfig
	rlStore                     *pgstore.Store
	agentTools                  *agenttoolsapi.Module
	gatewayCfg                  mcpgateway.Config
	gatewayManagement           *mcpManagement
	apiSrv                      *api.Server
	circuitBreaker              circuitBreakerEngine
	archCfg                     auditArchiveConfig
	arch                        *auditArchiveLoop
	communicationPump           *workOutboxPump
	stopDeny                    *stopDenyRecorder
	sessionHooks                *sessionHookCredentials
	demoTenant                  model.TenantID
	runtimeEngine               *engine
	cleanup                     []func()
}

// unwind runs the original boot defers in reverse registration order. Using
// defer here also preserves Go's cleanup semantics if a cleanup itself panics.
func (b *bootState) unwind() {
	if !b.bootOK && b.legacyAdoptStop != nil {
		b.legacyAdoptStop()
	}
	for _, close := range b.cleanup {
		defer close()
	}
}

func (b *bootState) configure(ctx context.Context) (err error) {
	if b.cfg.Engine == "" && !b.cfg.storeEngineExplicit {
		b.cfg.Engine = string(store.EngineSQLite)
	}
	if b.cfg.Engine != string(store.EngineSQLite) && b.cfg.Engine != string(store.EnginePostgres) {
		return exitcode.New(exitcode.Usage, fmt.Errorf("--engine %q must be sqlite or postgres", b.cfg.Engine))
	}
	b.loginProxies, err = (loginProxyOptions{resolved: b.cfg.LoginTrustedProxies}).resolve(osGetenv)
	if err != nil {
		return err
	}
	b.log = b.cfg.Logger
	if b.log == nil {
		b.log = slog.Default()
	}

	// The console log surface is redacted at the broker. The canonical
	// credential catalog is injected here rather than imported by core/api,
	// because core must not depend on /modules (scripts/check-boundary.sh) and the
	// detector catalog is single-owner in modules/security — the same seam
	// shape SupportBundleRedact already uses below. RedactCredentials, not
	// RedactText: the log viewer keeps the network and contact identifiers that
	// ARE the operator's diagnosis, and the support bundle, which leaves the
	// machine, keeps the full PII catalog.
	b.logBroker = api.NewLogBroker(
		b.log.Handler(), logBrokerRingSize, loadLogCaptureLevel(osGetenv, b.log),
		api.WithLogRedactor(securitymodule.RedactCredentials),
	)
	b.log = slog.New(b.logBroker)

	// unknown OLIVARES_* keys are an operator-visible contract violation,
	// but boot remains backward-compatible. Report every key in one consolidated
	// warning; `config effective --strict` is the CI/pre-production hard gate.
	if unknown := unknownConfigEnvKeys(os.Environ()); len(unknown) > 0 {
		b.log.Warn("unrecognized OLIVARES_* env keys ignored", "keys", unknown)
	}

	// AN EXPLICIT AUTHENTICATION CONFIGURATION IS SETTLED BEFORE ANYTHING MUTABLE
	// HAPPENS. This runs before the data directory is created, before the store
	// opens and before a key is minted, because the remedy for a refusal here is
	// editing a file — not cleaning up a half-built installation first.
	//
	// It replaces a loader that built the relying party out of two environment
	// variables with no predicate applied to either, so a pinned IP or a
	// single-label name produced a deployment where every ceremony failed and
	// nothing named the cause. A half-set pair used to be dropped with a warning;
	// it is now a refusal, because silently deriving a different authentication
	// authority than the operator wrote is the behavior this closes.
	b.webAuthn, err = resolveWebAuthnRP(b.cfg.PublicAddr, b.cfg.PublicAddrSource, osGetenv)
	if err != nil {
		return err
	}
	b.webAuthn.log(b.log)

	dataDirWasImplicit := b.cfg.DataDir == "" && envconfig.Get("OLIVARES_DATA_DIR") == ""
	if b.cfg.DataDir == "" {
		resolved, err := defaultDataDir()
		if err != nil {
			return err
		}
		b.cfg.DataDir = resolved
	}
	// The backend belongs to the installation, including a later `serve` or
	// read-only CLI invocation. Explicit DSNs still select their own store.
	if b.cfg.DSN == "" {
		db, err := quickstartPostgresConfig(ctx, b.cfg.DataDir, "")
		if err != nil {
			return err
		}
		if db.Engine == store.EnginePostgres {
			if b.cfg.storeEngineExplicit && b.cfg.Engine != string(store.EnginePostgres) {
				return errors.New("this data directory uses PostgreSQL; omit --engine or choose a separate --data-dir for SQLite")
			}
			b.cfg.Engine, b.cfg.DSN = string(db.Engine), db.DSN
			if b.cfg.OwnerDSN == "" {
				b.cfg.OwnerDSN = db.OwnerDSN
			}
			if b.cfg.AdminDSN == "" {
				b.cfg.AdminDSN = db.AdminDSN
			}
		}
	}
	// A command pointed at a REMOTE store carries its own DSN, and its data
	// directory holds nothing it needs. Requiring one there would refuse exactly
	// the documented Postgres invocation (`audit verify --engine postgres --dsn
	// env:DATABASE_URL`) for the sake of a directory it would never read — a
	// consequence the sol-max contrast pointed out.
	b.storeIsRemote = b.cfg.Engine == string(store.EnginePostgres) || strings.TrimSpace(b.cfg.DSN) != ""
	switch {
	case b.cfg.ReadOnly && !b.storeIsRemote:
		// Read, never install. An absent data dir is the answer, not a task.
		if err := requireDataDir(b.cfg.DataDir); err != nil {
			return err
		}
	case b.cfg.NoImplicitInstall && dataDirWasImplicit && !b.storeIsRemote && !installationExistsAt(b.cfg.DataDir):
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
		return exitcode.New(exitcode.NotFound, fmt.Errorf(
			"no installation at %s, and this command will not create one implicitly: "+
				"it would mint signing keys in the current directory — run `olivares quickstart` "+
				"(or `olivares setup`) to initialize, or pass --data-dir / OLIVARES_DATA_DIR to say "+
				"where the installation is", absOrSame(b.cfg.DataDir)))
	case b.cfg.ReadOnly:
		// Remote store: nothing local to require, and nothing to create either.
	default:
		// EnsureDataDir, not EnsureDir: the directory about to hold seven private keys
		// carries its own .gitignore, so an operator who points --data-dir inside their
		// own repository cannot commit the key material either (core/secure).
		if err := secure.EnsureDataDir(b.cfg.DataDir); err != nil {
			return err
		}
		// And when it CANNOT carry one — the data dir is a repository root, where a `*`
		// rule would hide the operator's whole project — say so. A protection that
		// quietly does not apply is the defect this whole change is about.
		if w := secure.DataDirVCSWarning(b.cfg.DataDir); w != "" {
			b.log.Warn("data directory: " + w)
		}
	}
	if b.cfg.dataDirReady != nil {
		if err := b.cfg.dataDirReady(b.cfg.DataDir); err != nil {
			return err
		}
	}
	// quickstartFresh is the PostgreSQL database quickstart provisioned in this start,
	// if any: it gets the tenant inventory once the store has migrated it (below).

	if b.cfg.quickstartPostgres != nil && *b.cfg.quickstartPostgres != "" {
		init, err := initPostgresConfig(ctx, b.cfg.DataDir, *b.cfg.quickstartPostgres, store.PgProvisionSpec{}, true)
		if err != nil {
			return err
		}
		db := init.config
		if init.fresh && db.Engine == store.EnginePostgres {
			b.quickstartFresh = &init
		}
		if db.Engine == store.EnginePostgres {
			b.cfg.Engine, b.cfg.DSN, b.cfg.OwnerDSN = string(db.Engine), db.DSN, db.OwnerDSN
			if b.cfg.AdminDSN == "" {
				b.cfg.AdminDSN = db.AdminDSN
			}
		}
	}
	// load the enterprise activation manifest into the osGetenv overlay
	// BEFORE buildModules reads any add-on's OLIVARES_*_CONFIG. A corrupt manifest
	// degrades to "nothing activated" (logged), never fails boot.
	if err := initActivationManifest(b.cfg.DataDir); err != nil {
		b.log.Warn("enterprise-activation: could not read the activation manifest; no preset add-ons activated this boot", "err", err)
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
	}{{"--dsn", &b.cfg.DSN}, {"--owner-dsn", &b.cfg.OwnerDSN}, {"--admin-dsn", &b.cfg.AdminDSN}} {
		resolved, err := resolveDSNRef(ctx, r.label, *r.dst, osGetenv)
		if err != nil {
			return err
		}
		*r.dst = resolved
	}

	b.eng = store.EngineSQLite
	if b.cfg.Engine == string(store.EnginePostgres) {
		b.eng = store.EnginePostgres
	}
	b.dsn = b.cfg.DSN
	if b.eng == store.EngineSQLite && b.dsn == "" {
		b.dsn = filepath.Join(b.cfg.DataDir, "olivares.db")
		// The SQLite driver creates the file it is pointed at, so under ReadOnly
		// the absence has to be caught HERE — opening the store would otherwise
		// materialize an empty 6 MB database that the command then reports as
		// containing nothing. Only the path this function DERIVED is checked: an
		// operator-supplied --dsn is theirs to name.
		if b.cfg.ReadOnly && !fileExistsAt(b.dsn) {
			return exitcode.New(exitcode.NotFound, fmt.Errorf(
				"no store at %s: this is not an initialized Olivares data directory, and a "+
					"read-only command never creates one — run `olivares quickstart`, or point "+
					"--data-dir (or OLIVARES_DATA_DIR) at the installation", b.dsn))
		}
	}
	// Bound the Postgres application pool. It was unbounded (database/sql
	// default 0 = unlimited), which is a latent bug HA surfaces: with replicaCount>1,
	// each node ALSO opens a dedicated leader-lock connection and (optionally) an
	// admin pool, so several unbounded app pools can exhaust the server's
	// max_connections. Default to a conservative per-node cap (leaving headroom for
	// the lock + admin connections under the typical 100-connection server limit);
	// OLIVARES_DB_MAX_CONNS overrides. SQLite is single-connection by construction.
	b.maxConns = 0
	if b.eng == store.EnginePostgres {
		b.maxConns = postgresMaxConns(osGetenv, b.log)
	}

	// build the multi-region residency registry from --region/--known-regions.
	// A malformed region config fails the boot HERE — before the store opens — rather
	// than at request time. nil/non-enforcing in single-region mode (no --region).
	b.residencyReg, err = residency.NewRegistry(b.cfg.Region, b.cfg.KnownRegions)
	if err != nil {
		return err
	}

	// K3 activation is REQUESTED configuration, parsed first so a malformed
	// request refuses boot before the store opens. Custody that is not declared
	// is recorded as a blocker on the request, never as a boot error: K3 custody
	// belongs to K3 alone. Effective readiness is decided later, per witness.
	b.communicationActivation, err = loadCommunicationActivationConfig(osGetenv)
	if err != nil {
		return err
	}
	// K3 content custody is explicit and boot-only. An absent path leaves the
	// sealer unbound; a declared path must load, unwrap and self-test before the
	// store opens. When activation is NOT requested a declared keyring that does
	// not load is the historical configuration error (nothing to degrade). When
	// it IS requested, the failure is a K3 custody blocker: the sealer stays
	// unbound, the K3 lane is not composed, and core/K1/K2 boot and serve. No
	// key is minted, no other key is tried, no ciphertext is touched.
	b.communicationSealer, err = loadCommunicationContentSealer(
		ctx,
		osGetenv(envCommunicationContentKeyringFile),
		openCommunicationContentKeyringOperatorConfig,
	)
	if err != nil {
		if !b.communicationActivation.Requested {
			return fmt.Errorf("load %s: %w", envCommunicationContentKeyringFile, err)
		}
		b.communicationActivation.blockCustody(envCommunicationContentKeyringFile, err.Error())
		b.communicationSealer = nil
		b.log.Error("sessions: K3 content keyring custody is unusable; the requested activation stays OFF with this cause while core, K1 and K2 boot",
			"env", envCommunicationContentKeyringFile, "err", err)
	}
	b.communicationCursorKeyring, b.communicationCursorStatus, err = loadCommunicationCursorKeyring(
		ctx, b.communicationActivation.CursorKeyringPath, openCommunicationContentKeyringOperatorConfig, time.Now(),
	)
	if err != nil {
		if !b.communicationActivation.Requested {
			return fmt.Errorf("load %s: %w", envCommunicationCursorKeyringFile, err)
		}
		b.communicationActivation.blockCustody(envCommunicationCursorKeyringFile, err.Error())
		b.communicationCursorKeyring, b.communicationCursorStatus = nil, communicationCursorKeyringStatus{}
		b.log.Error("sessions: K3 cursor keyring custody is unusable; the requested activation stays OFF with this cause while core, K1 and K2 boot",
			"env", envCommunicationCursorKeyringFile, "err", err)
	}

	return nil
}

func (b *bootState) loadSigningCustody(ctx context.Context) (err error) {
	// THE LOCAL RESTORE GUARD, TAKEN HERE — BEFORE ANY KEY IS LOADED OR MINTED.
	//
	// Position is the guarantee, and the version that checked and released would have
	// been worth nothing. The three loaders below MINT on a data directory that has
	// none, so a boot that read the control, let go and then loaded keys could create
	// a fresh audit key inside the window a restore was replacing that very custody —
	// and the ledger the restore then verified would be signed by a key nobody chose.
	//
	// It is held from here THROUGH the store's own publication decision and released
	// immediately after it, because that is the span in which this process can create
	// custody or publish a store for the destination. It is deliberately NOT held for
	// the life of the engine: these locks coordinate construction, and a lock held by
	// a serving process would advertise a revocation protocol the product does not
	// have.
	//
	// The guard is SHARED: several ordinary boots of one installation are normal, and
	// what must not coexist with them is an operation that changes the destination.
	//
	// ⛔ AND IT IS NO LONGER ONLY LOCAL. The destination's OWN control is read inside
	// the admission too, on a session it retains through the store's decision. Reading
	// it after the loaders is F3-IR-5: a remote database can carry a completed or
	// pending control on a node with nothing on disk, so the three loaders below would
	// have minted custody for a restored estate before the fact was learned.
	//
	// THE WHOLE DESTINATION CONFIGURATION IS ASSEMBLED FIRST, and the admission binds
	// to it. Everything it needs is operator configuration parsed from the environment,
	// so there is nothing here that has to wait for a key: assembling it now means the
	// boot never holds a SECOND, independently mutable connection config that could
	// name a different destination than the one it fenced. The only field bound later
	// is SignEvent, from the audit signer built out of the key loaded below.
	auditSpoolMaxBytes, auditSpoolOnFull, err := loadAuditSpoolConfig(osGetenv, b.log)
	if err != nil {
		return fmt.Errorf("load audit spool operator config: %w", err)
	}
	auditMetaBlinding, err := loadAuditMetaBlinding(osGetenv, b.log)
	if err != nil {
		return fmt.Errorf("load audit metadata blinding operator config: %w", err)
	}
	b.freshDirectoryInitialized = false

	b.preserveFreshModuleProfile = func() error {
		if !b.cfg.ApplyModuleProfile {
			return nil
		}
		if _, found, readErr := loadNodeModuleDocument(b.cfg.DataDir); readErr == nil && !found {
			initial := moduleSelectionDoc{Selected: standardModuleSelection(), ImportPending: true}
			if b.cfg.DemoSeed {
				// The demo promises its access graph. Its rows belong to the
				// core, so the module-table census cannot select its reader.
				initial.Selected = append(initial.Selected, "accessmap")
			}
			if err := saveNodeModuleDocument(b.cfg.DataDir, initial, time.Now()); err != nil {
				return fmt.Errorf("preserve the fresh module selection before initialization: %w", err)
			}
		}
		return nil
	}
	storeCfg := store.Config{
		InitializeDirectoryWriter: func() error {
			b.freshDirectoryInitialized = true
			if err := b.preserveFreshModuleProfile(); err != nil {
				return err
			}
			if b.prepareFreshCommunication != nil {
				b.prepareFreshCommunication()
			}
			return nil
		},
		Engine:              b.eng,
		DSN:                 b.dsn,
		AdminDSN:            b.cfg.AdminDSN,
		OwnerDSN:            b.cfg.OwnerDSN,
		MaxConns:            b.maxConns,
		AllowPrivilegedRole: b.cfg.AllowPrivilegedDBRole,
		AuditSpoolMaxBytes:  auditSpoolMaxBytes,
		AuditSpoolOnFull:    auditSpoolOnFull,
		AuditMetaBlinding:   auditMetaBlinding,
	}
	b.restorePublication, err = acquireBootPublication(ctx, storeCfg, b.cfg.DataDir)
	if err != nil {
		return err
	}
	// Deferred against every early failure between here and the explicit release
	// below; Close is idempotent, so both paths are safe.
	b.cleanup = append(b.cleanup, b.restorePublication.Close)

	if b.cfg.ServeMode && !b.cfg.ReadOnly && b.cfg.upgradeSnapshotRequest != "" {
		if err := snapshotPackageUpgrade(ctx, b.cfg.upgradeSnapshotRequest, b.cfg.DataDir, storeCfg); err != nil {
			return fmt.Errorf("package upgrade: pre-migration snapshot failed; no migrations applied: %w", err)
		}
	}

	// WHAT THE DESTINATION DEMANDS OF THE THREE LOADERS, asked BEFORE any of them runs.
	//
	// This is the point of the whole sublot. On a destination that carries a COMPLETED
	// restore control the custody was already chosen by that operation, so the loaders
	// must LOAD exactly it — no minting, no falling back from a configured source to a
	// local key, no CMEK plaintext written anywhere. On an unenrolled destination the
	// requirement is empty and the ordinary first boot mints its keys exactly as before.
	//
	// The admission has already refused every other verdict: pending, indeterminate,
	// quarantined, malformed, unreadable, and absent-with-a-bound-witness never reach
	// this line.
	b.custody = b.restorePublication.CustodyRequirement()
	keyOpts := b.cfg.keyLoadOptions()
	if b.custody.CustodyRequired() {
		keyOpts = append(keyOpts, withEnrolledCustody())
		b.log.Info("this destination carries a completed restore control, so its signing keys are LOADED under the custody that operation published and none is minted",
			"destination", b.custody.Destination(), "operation", b.custody.OperationID(), "keyset", b.custody.KeysetSHA256())
	}

	// Load the audit signing key UP FRONT (it only touches a file/env/KEK, not the
	// store) so a module can take its public key at construction time — the security
	// module verifies audit checkpoints against it. In HA the key is SHARED (a
	// mounted Secret / env) and loaded fail-closed so every replica signs with the
	// same key (the ledger does not fork at failover); under CMEK custody it
	// is unwrapped IN MEMORY through the customer's KMS KEK and never exists in
	// clear at rest; single-node/dev mints it on first boot.
	b.auditKey, err = loadAuditSigningKey(b.cfg.DataDir, b.log, keyOpts...)
	if err != nil {
		return err
	}
	if b.auditKey.created {
		b.log.Warn("generated a new audit signing key; back it up", "path", filepath.Join(b.cfg.DataDir, "audit-signing.key"))
	}
	// Load the catalog signing key UP FRONT too — an INDEPENDENT artifact-signing
	// key, NEVER the audit key — so module XIV ships SIGNING its approved registry
	// entries by default (the posture a governed internal marketplace needs; docs/SECURITY-HARDENING.md
	// §5). Same shared-or-local resolution as the audit key (shareable in HA so every
	// replica verifies pinned entries identically); a node without one keeps the
	// honest unpinned posture.
	b.catalogKey, err = loadCatalogSigningKey(b.cfg.DataDir, b.log, keyOpts...)
	if err != nil {
		return err
	}
	if b.catalogKey.created {
		b.log.Warn("generated a new catalog signing key; back it up", "path", filepath.Join(b.cfg.DataDir, "catalog-signing.key"))
	}
	// the policy-artifact signing key — INDEPENDENT of both the audit and
	// catalog keys — backing the claude-policy distribution truth loop: publish
	// signs the exact distributed bytes, pull agents verify against its pinned
	// fingerprint. Same shared-or-local resolution as the catalog key.
	b.policyKey, err = loadPolicySigningKey(b.cfg.DataDir, b.log, keyOpts...)
	if err != nil {
		return err
	}
	if b.policyKey.created {
		b.log.Warn("generated a new policy signing key; back it up and pin its fingerprint in the pull agents", "path", filepath.Join(b.cfg.DataDir, "policy-signing.key"))
	}
	memoryKeyOpts := append([]keyLoadOption(nil), keyOpts...)
	if b.auditKey.mode != custodyModeMinted {
		// Shared audit custody identifies a replicated installation. Provision the
		// dedicated portability file on every replica before enabling portability.
		memoryKeyOpts = append(memoryKeyOpts, withoutMinting())
	}
	b.memoryKey, b.memoryKeyErr = loadMemoryPortabilityKey(b.cfg.DataDir, memoryKeyOpts...)
	if b.memoryKeyErr != nil {
		b.log.Warn("memory portability unavailable; restore the installation's memory-portability.key with owner-only permissions (0600), then restart", "err", b.memoryKeyErr)
	} else if b.memoryKey.created {
		b.log.Info("generated a dedicated memory portability key; include it in installation backups", "path", filepath.Join(b.cfg.DataDir, memoryPortabilityKeyFile))
	}
	// THE ONE OBSERVATION, BUILT FROM THE KEY OBJECTS THEMSELVES.
	//
	// The three values passed here are the SAME loadedSigningKey objects the signers
	// below are constructed from — auditKey.priv becomes the audit signer, and
	// catalogKey.priv/policyKey.priv are handed to buildModules. The observation
	// derives each public half from the private key it will actually sign with and
	// fingerprints that, so there is no path by which a durable record could be read
	// back into a value called "observed": the measurement is of this process's keys or
	// it does not exist.
	b.custodyObserved, err = observeSelectedCustody(b.auditKey, b.catalogKey, b.policyKey)
	if err != nil {
		return fmt.Errorf("observe the selected signing custody: %w", err)
	}
	// Optional off-box (KMS/HSM) checkpoint signer (R5). With none
	// configured, the default on-box Ed25519 signer is used unchanged. Per-event
	// signing always stays on-box (the hot path is never routed off-box).
	var signerOpts []audit.Option
	ck, ckErr := buildCheckpointKey(b.log)
	if ckErr != nil {
		return fmt.Errorf("ledger checkpoint signer: %w", ckErr)
	}
	if ck != nil {
		signerOpts = append(signerOpts, audit.WithCheckpointKey(ck))
	}
	b.signer, err = audit.NewSigner(b.auditKey.priv, signerOpts...)
	if err != nil {
		return err
	}
	// Custody governance: validate the DECLARED posture against the ACTUAL
	// wiring and fail closed on any mismatch — a buyer that mandated BYOK/CMEK/HYOK
	// must get a refused boot on a config regression, never a silent downgrade. Then
	// log the effective posture once, so every boot states its custody plainly.
	assertions, err := loadCustodyAssertions()
	if err != nil {
		return err
	}
	if err := assertions.verify(b.auditKey.mode, ck != nil); err != nil {
		return fmt.Errorf("key custody: %w", err)
	}
	checkpointPosture := "on-box ed25519"
	if ck != nil {
		checkpointPosture = "off-box " + string(ck.Algorithm()) + " " + ck.KeyID()
	}
	b.log.Info("key custody posture",
		"audit_key", b.auditKey.mode, "audit_kek", orDash(b.auditKey.kek),
		"audit_prior_generations", len(b.auditKey.priors),
		"catalog_key", b.catalogKey.mode, "checkpoints", checkpointPosture,
		"declared_key_custody", orDash(assertions.auditKey), "declared_ledger_custody", orDash(assertions.ledger))
	// A migration source left declared in the environment is inert HERE — the boot
	// path resolves custody through the Business signing-key adapter and never consults it,
	// so the runtime keeps exactly one custody root. It is still said out loud
	// rather than ignored: the operator's CLI ceremonies WILL open with it, and a
	// variable nobody remembers setting is how the next rewrap becomes a mystery.
	// Only its presence and kind are logged; nothing else in that namespace is read.
	if kind := strings.TrimSpace(envconfig.Get(envKeyWrapOld)); kind != "" {
		b.log.Warn("a KEK migration source is declared in the environment and this engine is IGNORING it — the boot path has one custody root by design; it changes which KEK `olivares keys` ceremonies OPEN with, so unset it once the migration window is closed",
			"env", envKeyWrapOld, "kind", kind)
	}

	return nil
}

func (b *bootState) buildRuntime(ctx context.Context) (err error) {
	// OBS-03: build the W3C Trace Context provider from env (opt-in; a no-op exporter
	// when no collector is configured). It NEVER blocks boot (OTLP connects lazily) and
	// a tracing fault never breaks a request. The instrumented HTTP client it returns
	// is the engine→Claude transport (traceparent injection + gen_ai span/metrics),
	// threaded into the inference seam via buildModules; the same provider's ingress
	// middleware is wired into the API server below.
	b.tracer, err = obstrace.New(ctx, obstrace.FromEnv(b.cfg.Version))
	if err != nil {
		return fmt.Errorf("init tracing: %w", err)
	}
	if b.tracer.Enabled() {
		b.log.Info("observability: W3C Trace Context + OTLP export enabled (OBS-03)")
	}
	// On any boot error AFTER this point the engine struct is never returned, so its
	// Close() (which shuts the tracer down) never runs. Shut the tracer down here on
	// the error paths to avoid leaking the OTLP exporter's background goroutines;
	// cleared on the success path below so Close() owns shutdown thereafter.
	b.bootOK = false
	b.cleanup = append(b.cleanup, func() {
		if !b.bootOK {
			sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = b.tracer.Shutdown(sctx)
			cancel()
		}
	})

	// boot constructs (and owns) the event bus instead of letting the
	// runtime default one, so the serve path can select the distributed NATS
	// bridge (OLIVARES_BUS_CONFIG) and attach the saturation SLIs. ErrNotLeader
	// from a standby's store-writing subscribers is EXPECTED steady state in HA,
	// so it logs at Debug, not Warn (it still counts in the handler-errors SLI).
	// The demotion applies on EVERY topology on purpose: an HA pair without the
	// NATS bridge has the same standby write-gate noise, and on a single node
	// (always-leader elector) ErrNotLeader simply never fires.
	demoteNotLeader := func(err error) bool { return errors.Is(err, store.ErrNotLeader) }
	busCfg, err := loadBusConfig(osGetenv, b.log)
	if err != nil {
		// Fail-boot-closed (the buildCheckpointKey family): a declared distributed
		// bus that cannot be honored must never silently degrade to in-proc — the
		// node would run partitioned from the cluster.
		return fmt.Errorf("event bus: %w", err)
	}
	// the durable backend and the Core-NATS bridge are mutually exclusive —
	// the durable config already carries the NATS connection, so a second
	// OLIVARES_BUS_CONFIG is ambiguous intent. Refuse up front (deny-closed), before
	// either backend is constructed (so no connection leaks on this error path).
	if busCfg != nil && strings.TrimSpace(osGetenv(envDurableBusConfig)) != "" {
		return fmt.Errorf("event bus: set only one of %s or %s", envDurableBusConfig, envBusConfig)
	}

	// gatedBus is the chosen bus when it supports HA leader gating (the NATS
	// bridge or the enterprise durable backend); nil for the single-node in-proc
	// default. boot installs the leader predicate on it once leadership is resolved.

	// the enterprise durable JetStream backend (build-tag gated). In the
	// community durableBus port returns (nil,nil) unconfigured and an ERROR when
	// OLIVARES_DURABLE_BUS_CONFIG is set (fail-boot-closed — durability is enterprise).
	// Identity & Scale returns the declared NATS backend only with a valid entitlement.
	durableBus, derr := thisEdition.durableBus(osGetenv, busPayloadDecoders(), demoteNotLeader, b.log, b.cfg.LicenseFile, b.cfg.DataDir)
	if derr != nil {
		return fmt.Errorf("event bus: %w", derr)
	}
	switch {
	case durableBus != nil:
		b.bus, b.gatedBus = durableBus, durableBus
	case busCfg != nil:
		return fmt.Errorf("event bus: the Core NATS bridge is unavailable in this edition; use Business Identity & Scale")
	default:
		b.bus = eventbus.NewInProc(eventbus.Options{Logger: b.log, DemoteError: demoteNotLeader})
	}
	b.cleanup = append(b.cleanup, func() {
		if !b.bootOK {
			_ = b.bus.Close()
		}
	})

	// The shared metrics registry: built here (not inside api.New) so the bus
	// collectors and the audit checkpointer (cmd_serve) register scrape-time
	// families on the same /metrics exposition the API serves.
	b.reg = metrics.New(b.cfg.Version, time.Now())
	registerBusMetrics(b.reg, b.bus)
	b.busStats, _ = b.bus.(eventbus.StatsProvider)

	// if the operator REQUIRED a semantic embedder (OLIVARES_EMBEDDINGS_REQUIRE)
	// but none is configured, refuse to boot rather than silently serve the lexical
	// local-hash fallback as if it were semantic (docs/SECURITY-HARDENING.md — never a silent gap).
	if err := checkEmbeddingsRequirement(osGetenv); err != nil {
		return fmt.Errorf("embeddings requirement: %w", err)
	}

	// Load the operator's connector-wiring config ONCE (sources + identity roster +
	// knowledge document sources). buildModules consumes its Documents section to wire
	// the knowledge module's pull sources; wireSources/wireRoster (below, after the
	// store) consume Sources/Identity. One read, one place the wiring decision is made
	// (12 §7.3 IDN-06).
	b.srcCfg, err = loadSourcesConfig(b.log)
	if err != nil {
		return fmt.Errorf("load sources operator config: %w", err)
	}

	// Construct the module set through buildModules in wire.go. Register the
	// modules with the runtime before opening the store so rt.RegisterSchema
	// includes their schema at store construction.
	var memoryKnowledgeOpts []knowledge.Option
	if b.memoryKeyErr == nil {
		memoryKnowledgeOpts = append(memoryKnowledgeOpts, knowledge.WithMemoryPortabilityKeys(b.memoryKey.priv, b.memoryKey.priv.Public().(ed25519.PublicKey)))
	}
	b.set, err = buildModules(&sessions.Dependencies{}, b.signer, b.catalogKey.priv, b.policyKey.priv, b.auditKey.priors, b.tracer.AnthropicHTTPClient(nil), b.tracer, b.srcCfg, editionConfigFrom(b.cfg), b.cfg.DataDir, b.log, memoryKnowledgeOpts...)
	if err != nil {
		return fmt.Errorf("load module operator config: %w", err)
	}
	// The modules this node runs (moduleprofile.go), decided before the store opens
	// from this node's copy of the selection; serve reconciles it with the
	// deployment settings before listening. Every module is still built and
	// registered for its schema; one outside the profile is dormant, and `running`
	// is the view of the set that optional wiring and periodic jobs receive.
	// the zero profile runs every module
	if b.cfg.ApplyModuleProfile {
		b.profile = bootModuleProfile(b.cfg.DataDir, b.storeIsRemote || fileExistsAt(b.dsn), b.log)
	}
	b.running = b.set.running(b.profile)
	// Automatic local custody does not change an explicit activation, shared key
	// configuration, encrypted operator custody, or a saved Eventing OFF choice.
	b.localCommunication = b.eng == store.EngineSQLite && b.running.eventing != nil &&
		strings.TrimSpace(osGetenv(envCommunicationActivation)) == "" &&
		osGetenv(envCommunicationContentKeyringFile) == "" && osGetenv(envCommunicationCursorKeyringFile) == "" &&
		strings.TrimSpace(osGetenv(envKeyWrap)) == ""
	if b.localCommunication {
		b.communicationSealer, b.communicationCursorKeyring, b.communicationCursorStatus = prepareLocalCommunicationCustody(ctx, b.cfg.DataDir, false, &b.communicationActivation)
		if !b.cfg.ReadOnly && !b.custody.CustodyRequired() {
			b.prepareFreshCommunication = func() {
				b.communicationSealer, b.communicationCursorKeyring, b.communicationCursorStatus = prepareLocalCommunicationCustody(ctx, b.cfg.DataDir, true, &b.communicationActivation)
			}
		}
	}

	if b.running.recorder == nil {
		b.set.gov.UseRecordingGate(nil) // break-glass stays deny-closed without recording
	}
	if b.communicationSealer != nil {
		if b.set.sessions == nil {
			return fmt.Errorf("bind %s: sessions module is unavailable",
				envCommunicationContentKeyringFile)
		}
		b.set.sessionDependencies.CommunicationSealer = b.communicationSealer
	}
	var sourceAdmission runtime.SourceRegistrationAdmission
	if b.set.sessions != nil {
		sourceAdmission = b.set.sessions.AdmitSourceRegistration
	}
	b.rt = runtime.New(runtime.Options{Logger: b.log, Bus: b.bus, SourceRegistrationAdmission: sourceAdmission})
	for _, m := range b.set.all {
		if consumer, ok := m.(interface{ UseProducerStatus(func(string) bool) }); ok {
			consumer.UseProducerStatus(func(namespace string) bool {
				name := moduleCatalog[namespace].Name
				for _, status := range b.rt.Status() {
					if status.Type == sdk.TypeModule && status.Name == name {
						return status.Status == runtime.StatusRunning
					}
				}
				return false
			})
		}
		sm, ok := m.(sdk.Module)
		if !ok {
			return fmt.Errorf("module %q does not satisfy sdk.Module", m.APINamespace())
		}
		add := b.rt.AddModule
		if !b.profile.Active(m.APINamespace()) {
			add = b.rt.AddDormantModule
		}
		if err := add(sm, modulespec.DefaultConfig(m.APINamespace())); err != nil {
			return fmt.Errorf("register module %q: %w", m.APINamespace(), err)
		}
	}

	return nil
}

func (b *bootState) openStore(ctx context.Context) (err error) {
	// THE ADMISSION OPENS THE STORE, rather than the boot calling a constructor that
	// would have to take the destination all over again.
	//
	// That is the difference this correction makes. The admission already holds this
	// installation's local anchors and already read its LOCAL evidence — which the
	// store constructor cannot see, since it gets only a DSN, and without which a
	// PostgreSQL destination whose control was installed and then dropped is
	// indistinguishable from one that never carried a control. Opening THROUGH it
	// hands the store a child of that live ownership; the previous form left the store
	// to acquire a second, unrelated lease and be granted it by process membership.
	//
	// The destination is bound: this configuration must name the engine and DSN the
	// admission fenced, or the Open is refused rather than re-pointed.
	// THE LAST LOOK AT THE SOURCES, before the store is published.
	//
	// A completed control names a custody generation; the sources that generation
	// lives in are outside this machine's lease. A mounted Secret can be replaced and
	// a KEK grant revoked while this boot prepares, so the selection is measured once
	// more through the same strict load-only path and any difference refuses. It
	// detects; it does not immobilize an external KMS, and it never substitutes a key.
	if b.custody.CustodyRequired() {
		if rerr := recheckSelectedCustody(b.cfg.DataDir, b.cfg.keyLoadOptions(), b.custodyObserved); rerr != nil {
			return rerr
		}
	}
	// The modules this composition and its edition declare to the census, with
	// their retirement steps; their readers are declared with the schema, below.
	contributions := censusContributions()
	b.declared = declaredModules(b.set, contributions)
	// The register func the store opens with. The console restore opens its scratch
	// store with the same one (consoleDRConfig): the guard edition derives from the
	// registered tables, so a scratch registry that held only the core tables would
	// compute an edition the snapshot was never written under.
	declared := b.declared
	b.storeSchema = func(reg store.ExtensionRegistry) error {
		if err := b.rt.RegisterSchema(reg); err != nil {
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
		if err := registerCircuitBreakerSchema(reg); err != nil {
			return err
		}
		// The retirement readers are declared before the registry closes, so the
		// census verdict computed at open covers them. b.declared is replaced after
		// the open (withAuthPartition), so this closure keeps the set the open saw.
		return declareCompositionReaders(reg, declared, contributions)
	}
	// THE ADMISSION OPENS THE STORE with the audit signer bound to it and the
	// measurement of the custody this boot actually loaded.
	//
	// Only SignEvent is supplied here: the destination configuration was frozen when
	// the admission was taken, so there is no second Config for this call to name a
	// different destination with. Signing every audit event at write time is what makes
	// the ledger tamper-evident per event rather than only at the periodic checkpoints
	// (it closes the between-checkpoints tail-rewrite window); the same key signs the
	// checkpoints, and `audit verify --pubkey` checks them off-box (docs/SECURITY-HARDENING.md).
	b.st, err = b.restorePublication.Open(ctx, b.signer.SignEvent, b.custodyObserved, b.storeSchema)
	// THE ADMISSION IS RELEASED HERE, and not earlier or later.
	//
	// Not earlier, because everything above — key loading, minting, and the store's own
	// fenced publication decision — is what it protects. Not later, because the engine
	// this function returns keeps running, and a construction lock held by a serving
	// process would claim an exclusion over already-published stores that the ratified
	// contract explicitly does not promise. Client drain stays the operator's
	// prerequisite.
	if err == nil && b.localCommunication && b.communicationSealer != nil {
		b.set.sessionDependencies.CommunicationSealer = b.communicationSealer
	}
	b.restorePublication.Close()
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	// Retain the concrete pool drain before the residency/suspension wrappers.
	if q, ok := b.st.(interface{ QuiesceForSQLiteRestore(context.Context) error }); ok {
		b.storeQuiesce = q.QuiesceForSQLiteRestore
	}
	if b.cfg.ApplyModuleProfile {
		// SYSTEM identifies an existing installation unless this Open created it.
		if !b.freshDirectoryInitialized {
			b.profile.existingInstallation, err = moduleInstallationExists(ctx, b.st)
			if err != nil {
				_ = b.st.Close()
				return fmt.Errorf("read the module installation state: %w", err)
			}
		} else {
			b.profile.existingInstallation = false
		}
		if !b.profile.existingInstallation {
			if err := b.preserveFreshModuleProfile(); err != nil {
				_ = b.st.Close()
				return err
			}
		}
	}

	// The composition census is read from the store as opened, before the
	// service guards below wrap it and hide its optional capabilities; so is the
	// auth partition's reader, which the store declared itself.
	b.census, _ = b.st.(store.CompositionCensus)
	if b.census != nil {
		if readiness := b.census.CompositionReadiness(); !readiness.Ready() {
			b.log.Warn("retirement: composition census is unready; account retirement is blocked", "cause", readiness.Cause())
		}
	}
	// A database quickstart provisioned in this start gets the tenant inventory now that
	// the engine has migrated it (the routine reads the core tables), before anything
	// lists tenants: retention, legal hold and audit checkpoints then cover every
	// tenant without the admin role. A refusal (managed PostgreSQL without a
	// superuser) is a warning; those jobs stay off and say so.
	if b.quickstartFresh != nil {
		if err := installTenantInventory(ctx, b.quickstartFresh.maintenance, b.quickstartFresh.spec); err != nil {
			b.log.Warn(tenantInventoryNotInstalled(err))
		}
	}
	b.declared = withAuthPartition(b.st, b.declared)
	// in a region-scoped deployment wrap the store with the deny-closed
	// residency guard, so every tenant-scoped unit of work for a tenant pinned to
	// another region is refused (store.ErrResidencyViolation) rather than silently
	// served an empty set. In single-region mode Guard returns st untouched (zero
	// overhead). System/Auth (including the leadership bootstrap below) pass through;
	// everything downstream (module data, authenticator, API) uses the wrapped store.
	if b.residencyReg.Enforces() {
		b.st = residency.Guard(b.st, b.residencyReg, b.log)
		b.log.Info("residency: region-scoped instance active — serving only this region's tenants, cross-region denied",
			"home_region", b.residencyReg.Home().String(), "known_regions", b.residencyReg.Known())
	}
	// Promotion recovery is custody, not service: it must be able to withdraw
	// credentials from a suspended local tenant, but it must retain this residency
	// guard and therefore cannot touch a tenant pinned to another region.
	b.sessionRuntimeRecoveryStore = b.st
	b.sessionRuntimeRecoveryData = api.NewModuleData(b.sessionRuntimeRecoveryStore)
	// wrap with the deny-closed service-withdrawal guard, so a tenant whose
	// org is suspended is not served by ANY path — REST, gRPC, or the in-process
	// background pumps, which all re-enter through View/Mutate. It is wrapped
	// OUTSIDE the residency guard on purpose: the innermost decorator's check runs
	// FIRST, so a cross-region request keeps reporting the precise residency
	// violation instead of being masked by "this instance has no org for you".
	// Always armed (any deployment can suspend a tenant); System/Auth pass through
	// so the operator can always restore, and a suspended tenant's users can still
	// authenticate and be told why they are refused.
	b.st = suspension.Guard(b.st, b.log)
	// Whether this store can enumerate every tenant: a job that must cover every
	// tenant and cannot is listed in server-info (jobsnotrunning.go).
	b.enumerable = estateEnumerable(ctx, b.st, b.log)
	b.notRunning = &jobsNotRunning{log: b.log}

	return nil
}

func (b *bootState) bindIdentityAndLeadership(ctx context.Context) (err error) {
	// Bind module data and the two private runtime credential issuers before the
	// first leadership acquisition. OnPromote must recover every durable runtime
	// handle before IsLeader becomes visible; wiring these after Leader.Run would
	// leave the initial leader with a post-promotion revocation window.
	b.data = api.NewModuleData(b.st)
	finData := finopsData{ModuleData: b.data, st: b.st}
	for _, m := range b.set.all {
		if dc, ok := m.(api.DataConsumer); ok {
			if b.set.finops != nil && m == b.set.finops {
				dc.UseData(finData)
				continue
			}
			dc.UseData(b.data)
		}
	}
	// Bind the inventory's durable sweep scope alongside UseData, and
	// well before rt.Start — the module reads it only inside a sweep, and Start is
	// what begins the ticker that calls one.
	//
	// It is given the COMPOSED store (residency- and suspension-wrapped, above),
	// so the adapter's own enumeration and every tenant turn it leads to pass
	// through the same guards as the rest of the engine. Without this binding the
	// sweep refuses explicitly and says so; it does NOT fall back to the tenants
	// this process happened to observe, and it does not make Start fail — failing
	// Start would unsubscribe C1 ingestion too (core/runtime/lifecycle.go:124-126),
	// which would turn "freshness cannot advance" into "nothing is discovered".
	if b.set.inventory != nil {
		b.set.inventory.UseSweepScopeSource(inventorySweepScopeSource{st: b.st, reg: b.residencyReg})
	}
	b.authr = auth.NewAuthenticator(b.st, nil)
	b.authr.SetTrustedLoginProxies(*b.loginProxies)
	// the TOTP seed sealer. A key failure leaves the factor unwired:
	// enrolment refuses (503 totp_unavailable) and a stored factor's challenges
	// fail closed — loud, never a boot abort, and never a cleartext seed.
	if sealer, serr := newTOTPSeedSealer(b.cfg.DataDir, osGetenv); serr != nil {
		b.log.Error("totp: seed sealer unavailable; TOTP enrolment and verification are disabled until fixed", "err", serr)
	} else {
		b.authr.WithTOTPSeedSealer(sealer)
	}
	// The guards above hide the store's census, so the authenticator is handed
	// the one read at open for the retirement's absence proof.
	if b.census != nil {
		b.authr.UseCompositionCensus(b.census)
	}
	// One standing port for every fenced writer: routes read it from their
	// module context, and modules that write outside a request receive it here,
	// before Start.
	b.standing = &standingPort{reader: b.authr}
	for _, m := range b.set.all {
		if sc, ok := m.(auth.StandingConsumer); ok {
			sc.UseStanding(b.standing)
		}
	}
	// The retirement pump: an offboard in this process wakes it, and it is
	// started below, once boot cannot fail, on the active writer only.
	b.retirement = newRetirementPump(b.authr, b.declared, b.reg, b.log)
	b.authr.SetRetirementWaker(b.retirement.wake)
	var communicationStoreWitness *communicationGuardStoreWitness

	if b.set.sessions != nil {
		b.set.sessionDependencies.RecoveryData = b.sessionRuntimeRecoveryData
		recoveryAuthr := auth.NewAuthenticator(b.sessionRuntimeRecoveryStore, nil)
		b.set.sessionDependencies.RecoveryWorkSessionCreds, b.set.sessionDependencies.RecoveryCommunicationSessionCreds = sessionWorkCredentialSource{authenticator: recoveryAuthr}, sessionCommunicationCredentialSource{authenticator: recoveryAuthr}
		b.set.sessionDependencies.OrchestrationScopes = sessionOrchestrationWorkScope{st: b.st, module: b.set.sessions}
		b.set.sessionDependencies.WorkSessionCreds = sessionWorkCredentialSource{authenticator: b.authr, module: b.set.sessions}
		b.set.sessionDependencies.CommunicationSessionCreds = sessionCommunicationCredentialSource{authenticator: b.authr}
		// K3 guard repair is a leadership bootstrap, not tenant service. Bind its
		// closure-only, guard-scoped adapter over the pre-suspension store. The
		// residency wrapper remains load-bearing authority on every tenant mutation;
		// however the global witness below refuses CLEAN entirely when residency is
		// enforcing until a region-move ceremony can prove both sides of a repin.
		b.set.sessionDependencies.CommunicationGuardData = sessions.NewCommunicationGuardReconciliationData(b.sessionRuntimeRecoveryData)
		communicationStoreWitness = newCommunicationGuardStoreWitness(
			func(ctx context.Context) ([]model.Org, error) {
				var orgs []model.Org
				err := b.st.System(ctx, func(sys store.SystemScope) error {
					var err error
					orgs, err = sys.ListOrgs(ctx)
					return err
				})
				return orgs, err
			},
			b.residencyReg,
			b.set.sessions,
			b.st.Leader().IsLeader,
		)
		b.set.sessionDependencies.CommunicationStoreReadiness = communicationStoreWitness
		// K3 lot A composition: real directory resolver, publication attestor,
		// grant closure and the composite store proof are bound on every boot;
		// the pump witness and the dual credential posture only when activation
		// was requested. It runs BEFORE Leader.Run so the promotion recovery
		// below observes the enabled posture on the very first election.
		b.communicationComposition, err = bindCommunicationComposition(
			ctx, b.communicationActivation, b.st, b.set.sessions, b.set.gov, communicationStoreWitness,
			b.communicationCursorKeyring, b.communicationCursorStatus, b.running.eventing != nil, b.log,
		)
		if err != nil {
			_ = b.st.Close()
			return fmt.Errorf("bind communication composition: %w", err)
		}
	}
	// Only serve owns session runtimes. An offline command booted against the
	// same SQLite file holds no runtime handle, so recovering there would mark
	// every session the running engine still drives as stopped and revoke its
	// credentials while the process keeps running (always-leader SQLite promotes
	// every process that opens the file).
	listSessionOrgs := func(ctx context.Context) ([]model.Org, error) {
		var orgs []model.Org
		err := b.st.System(ctx, func(sys store.SystemScope) error {
			var err error
			orgs, err = sys.ListOrgs(ctx)
			return err
		})
		return orgs, err
	}
	recoverSessionRuntimeCredentials := func(ctx context.Context) error {
		if !b.cfg.ServeMode {
			return nil
		}
		return recoverSessionRuntimeCredentialsForPromotion(ctx, listSessionOrgs, b.residencyReg, b.set.sessions)
	}
	// Build the Cedar reactivation barrier before registering OnPromote. The callback
	// runs before IsLeader becomes visible, including each later HA promotion; doing
	// this only after the first Run would let a promoted standby enforce its stale
	// in-memory G while durable authority has already reached G+1 elsewhere.
	pdpListOrgs := b.cfg.pdpListOrgs
	if pdpListOrgs == nil {
		pdpListOrgs = func(ctx context.Context, st store.Store) ([]model.Org, error) {
			var orgs []model.Org
			err := st.System(ctx, func(sys store.SystemScope) error {
				var listErr error
				orgs, listErr = pdpVisibleTenantInventory(ctx, sys, b.log)
				return listErr
			})
			return orgs, err
		}
	}
	pdpReload := b.cfg.pdpReload
	if pdpReload == nil {
		pdpReload = b.set.gov.ReloadActivePDP
	}
	reactivatePDPForPromotion := func(ctx context.Context) error {
		if !b.cfg.ServeMode {
			return nil
		}
		return reloadPDPForPromotion(ctx, func(ctx context.Context) ([]model.Org, error) {
			return pdpListOrgs(ctx, b.st)
		}, pdpReload, b.log)
	}

	// K1 durable work ports are composed only after the store exists and its
	// residency and suspension wrappers are installed. Sessions receives narrow adapters,
	// never another module's concrete implementation.
	if b.set.sessions != nil {
		b.set.sessionDependencies.WorkIdentity = workIdentityResolver{
			st: b.st, sessions: b.set.sessions, agentLifecycle: b.set.gov,
		}
		b.set.sessionDependencies.ProtocolLocalResourceResolver = protocolLocalResourceResolver{
			store: b.st, channels: b.set.sessions,
		}
		b.set.sessionDependencies.WorkContent = workContentGuard{}
		b.set.sessionDependencies.WorkEventSink = workEventSink{eventing: b.running.eventing, notRunning: b.running.eventing == nil}
	}
	// Login-enforcement capability (R5). Classified from the host selection and this
	// artifact's compiled capability before the election, so promotion and the later
	// policy assertion share one decision. An absent artifact reconciles against the
	// stored observation and the configured demand in a single read-only snapshot.
	b.loginCap = newLoginCapabilityBoot(osGetenv, b.cfg.Version)
	if err := b.loginCap.observeFollower(ctx, b.st); err != nil {
		_ = b.st.Close()
		return fmt.Errorf("login enforcement: capability snapshot: %w", err)
	}
	if b.loginCap.refusesStartup() {
		_ = b.st.Close()
		return fmt.Errorf("login enforcement: this deployment recorded the component and a global/default posture is configured, but this artifact does not link it; install an artifact that carries it or clear the posture")
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
	b.haCfg = haCfg
	if haErr != nil {
		_ = b.st.Close()
		return fmt.Errorf("ha leader-routing config: %w", haErr)
	}

	if b.haCfg.PublishLabel {
		b.haPublisher, haErr = newHALeaderPublisher(b.haCfg, b.log)
		if haErr != nil {
			_ = b.st.Close()
			return fmt.Errorf("ha leader-label publisher: %w", haErr)
		}
		// Publish STANDBY before the election. A pod object outlives its container:
		// after a crash-restart this pod may still carry the `leader` label of its
		// previous incarnation, and the leader Service would route writes to a node
		// that no longer holds the lock (they would fail closed, but the outage would
		// be silent). Refusing to boot when this cannot be published is deliberate:
		// an HA pod that cannot manage its own label is not safe to route to.
		if err := b.haPublisher.publishRole(ctx, haRoleStandby); err != nil {
			_ = b.st.Close()
			return fmt.Errorf("ha: could not publish the initial standby role label (a stale leader label from a previous incarnation would misroute traffic): %w", err)
		}
	}
	// The naming of legacy provider profiles outlives a promotion, so it runs on a
	// context the engine owns: Close cancels it, as does a boot that fails.
	adoptCtx, adoptStop := context.WithCancel(context.Background())
	b.legacyAdoptStop = adoptStop
	promote := func(ctx context.Context) error {
		if err := b.st.System(ctx, func(sys store.SystemScope) error {
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
		// The capability record belongs to its own AuthMutate, after the system
		// bootstrap has committed: it takes the store's capability lock first, which
		// must precede every directory, user-authority and audit lock. A refusal here
		// refuses the promotion; it never closes the store, because a later promotion
		// of a running follower must leave the node serving as a follower.
		if err := b.loginCap.recordAtPromotion(ctx, b.st); err != nil {
			return err
		}
		if b.communicationComposition != nil {
			// The composite proof runs the guard estate ceremony and then proves
			// the directory status, schema and epochs. A failed or incomplete
			// proof keeps K3 store readiness OFF, with blockers logged when activation
			// is requested, but must not take unrelated product surfaces down.
			//
			// ⛔ AND AN ORDINARY FIRST BOOT IS NOT AN ERROR, which is what it said
			// until 2026-09-18. Measured on a clean `quickstart`: this
			// line fired at ERROR with a 700-character `%+v` of a Go struct, and its
			// three blockers were `writer_control_not_enforced: mode="staged"`,
			// `directory_epoch_coverage_incomplete` and
			// `expected_generation_below_activation: 1` — every one of them "nobody
			// has run `olivares db activate-directory-writer` yet". K3 is a rollout
			// an operator ACTIVATES; a posture waiting for a ceremony is a posture,
			// and printing it as the engine's only red line on a correct first boot
			// is how a clean start gets read as a broken product.
			//
			// The level therefore follows the CLASSIFICATION (AwaitingActivation),
			// not the mere presence of an error: INFO with the command that changes
			// it when the proof is only un-activated, WARN when requested activation
			// has another blocker. An unrequested communication plane is not a boot
			// failure.
			if err := b.communicationComposition.reconcileAndVerify(ctx); err != nil {
				proof := b.communicationComposition.store.Proof()
				if proof.AwaitingActivation {
					b.log.Info("sessions: the communication store is staged and not activated, so K3 store readiness is off",
						"remedy", "olivares db activate-directory-writer, then restart the engine",
						"blockers", strings.Join(proof.Blockers, "; "),
						"unaffected", "sessions, providers and every other surface of this engine")
				} else if b.communicationComposition.activation.Requested {
					b.log.Warn("sessions: communication store proof incomplete; K3 store readiness remains off",
						"err", err, "proof", proof)
				}
			} else {
				b.log.Info("sessions: communication store proof established",
					"proof", b.communicationComposition.store.Proof())
			}
		} else if communicationStoreWitness != nil {
			if err := communicationStoreWitness.ReconcileAndVerify(ctx); err != nil {
				b.log.Error("sessions: communication guard bootstrap incomplete; K3 store readiness remains off",
					"err", err)
			} else {
				b.log.Info("sessions: communication guard estate reconciled and verified")
			}
		}
		if err := recoverSessionRuntimeCredentials(ctx); err != nil {
			return err
		}
		if b.cfg.ServeMode && !b.cfg.ReadOnly && b.set.sessions != nil {
			// Off the promotion path: it waits for leadership to be visible, which
			// only happens after this hook returns.
			startLegacyProfileAdoption(adoptCtx, listSessionOrgs, b.residencyReg, b.set.sessions, b.st.Leader().IsLeader, b.log)
		}
		if err := reactivatePDPForPromotion(ctx); err != nil {
			return fmt.Errorf("pdp: cannot establish a complete tenant inventory for authored Cedar reactivation: %w", err)
		}
		if b.cfg.ServeMode && b.set.sessions != nil {
			// Initial election precedes module composition and Start, which owns
			// the first recovery. Later promotions re-arm waits created while this
			// node was a standby, using the same best-effort inventory and workers.
			for _, status := range b.rt.Status() {
				if status.Type == sdk.TypeModule && status.Name == sessions.Name && status.Status == runtime.StatusRunning {
					b.set.sessions.RecoverWaitingLaunchesForAllTenants(ctx)
					break
				}
			}
		}
		// Stage-2: the leader label is NOT published from here. This callback
		// runs while the elector is still `promoting` — leadership is not yet
		// established (the fencing epoch has not been bumped and IsLeader is still
		// false), so advertising the label here could route client traffic to a pod
		// whose promotion later fails. The resync loop publishes it the moment
		// IsLeader() flips, which is the same predicate the request gates use.
		return nil
	}
	if b.cfg.pdpPromotionRegistered != nil {
		b.cfg.pdpPromotionRegistered(promote)
	}
	b.st.Leader().OnPromote(promote)
	if err := b.st.Leader().Run(ctx); err != nil {
		_ = b.st.Close()
		return fmt.Errorf("leader election / provision system tenant: %w", err)
	}
	// The resync loop that converges the label onto live leadership is started at the
	// very END of boot (just before the engine is returned), so no failure path
	// between here and there leaks a goroutine that keeps patching the pod of an
	// engine that never came up.
	// E2: compatibility TOFU is allowed only after the enterprise tool-pin
	// store durably appends its HIGH auto-pin event. The community build binds
	// nothing; the enterprise overlay attaches this store-backed recorder before
	// any MCP resource server can accept traffic.
	if thisEdition.bindToolPinAudit != nil {
		thisEdition.bindToolPinAudit(b.set.pinVerifier, b.st, b.log)
	}
	// rebuild pins/drifts from the tenant-scoped table and write through
	// from here on — a restart no longer clears pins, so compatibility TOFU can
	// no longer re-legitimate a rug-pull across restarts. Community binds
	// nothing (nil verifier).
	if thisEdition.bindToolPinPersistence != nil {
		thisEdition.bindToolPinPersistence(ctx, b.set.pinVerifier, b.st, b.log)
	}
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
	if b.gatedBus != nil {
		b.gatedBus.SetInjectGate(b.st.Leader().Active)
	}
	// liveingest's observed-ref derivation is leader-gated for the same
	// reason — a stateless republisher has no store write to fence it, and the
	// leader already sees every node's edges over the bridge (exactly-once
	// derivation cluster-wide). Single-node (always-leader elector) unchanged.
	b.set.live.UseLeadership(b.st.Leader().Active)

	// Honest posture (R2): without a dedicated BYPASSRLS admin pool, authoritative
	// cross-tenant ceremonies on a properly secured Postgres app role fail with
	// ErrEnumerationNotAuthoritative. Explicit best-effort visible reads may still
	// be partial; neither outcome is silently treated as a complete org list.
	// SQLite needs no admin pool (no roles).
	if b.eng == store.EnginePostgres && b.cfg.AdminDSN == "" {
		b.log.Warn("no --admin-dsn configured: authoritative cross-tenant ceremonies fail closed with ErrEnumerationNotAuthoritative; explicitly best-effort visible reads may be partial; provision a NOSUPERUSER BYPASSRLS admin role (deploy/postgres/01-app-role.sql) and set --admin-dsn for full coverage")
	}

	return nil
}

func (b *bootState) bindServices(ctx context.Context) (err error) {
	// Module data was bound before leader election so session credential recovery
	// participates in the promotion barrier. The remaining late-bound consumers
	// below reuse that same accessor before the runtime starts.
	// late-bind the same tenant-scoped data handle into the knowledge retrieval
	// guard, which was constructed in buildModules before the store existed (the
	// approval-bridge late-binding pattern). It resolves agent→identity→groups/
	// clearance/region from the governed identity plane on each retrieval.
	if b.set.knowledgeGuard != nil {
		b.set.knowledgeGuard.useData(b.data)
	}
	if b.set.knowledgeEmbedder != nil {
		b.set.knowledgeEmbedder.UseData(b.data)
	}
	if b.set.knowledgeStatus != nil {
		b.set.knowledgeStatus.useGuardPostureStore(b.st, b.set.sourceScopeResolver)
	}
	// late-bind the raw store into the account eraser — it needs the AUTH
	// partition (Store.AuthMutate), which no tenant-scoped data handle can reach —
	// and the engine's authenticator, whose offboard wakes the retirement pump.
	if b.set.accountEraser != nil {
		b.set.accountEraser.use(b.st, b.authr)
	}
	// late-bind the same data handle into the policy truth-loop seams, both
	// constructed in buildModules before the store existed. Until bound, the
	// distributor is deny-closed (publish reports enqueue-failed, never
	// "distributed") and the observed provider reports "could not be read".
	if b.set.policyDist != nil {
		b.set.policyDist.UseData(b.data)
	}
	if b.set.policyObserved != nil {
		b.set.policyObserved.UseData(b.data)
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
	b.policyEval = b.set.gov.Evaluator()
	// the MAIN request authorizer additionally consults the per-tenant SCOPED-GRANT
	// engine (Cedar) BESIDE the deny-overlay, so authorization is no longer flat RBAC ∩
	// deny: a permit GRANTS within the scope tree, a forbid still RESTRICTS, default-
	// deny stands. RequestEvaluator() is the deny-overlay WITHOUT the authored policy (the
	// scoped seam evaluates that policy once — grants and forbids together). A tenant with
	// no authored grants makes the scoped engine abstain before any store read.
	b.authz = auth.NewAuthorizer(b.set.gov.RequestEvaluator(), auth.WithScopedGrants(b.set.gov.ScopedGrants()))
	if b.set.sessions != nil {
		b.set.sessionDependencies.WorkspaceSnapshotAuthority = workspaceSnapshotAuthority{data: b.st, principals: b.authr, authz: b.authz, scopes: b.set.sourceScopeResolver}.Check
	}
	if b.set.skills != nil && b.set.sessions != nil {
		b.set.skills.UseTargets(skillsTargetAuthority{principals: b.authr, st: b.st, authz: b.authz, sessions: b.set.sessions}, b.set.sessions.RefuseReferencedSkillsPack)
	}
	if b.running.skills != nil && b.running.sessions != nil {
		if err := b.set.sessions.UseSessionSkills(sessionSkillsSource{st: b.st, sessions: b.set.sessions, catalog: b.set.skills}, filepath.Join(b.cfg.DataDir, "session-skill-homes")); err != nil {
			return err
		}
	}
	b.setupTok = secure.NewSetupToken(filepath.Join(b.cfg.DataDir, "setup.token"))

	// late-bind the eventing platform's two seams. The SAME composed
	// authorizer (RBAC ∩ governance ABAC) that gates live requests gates every
	// outbound event delivery (deny-closed per event); the secret sealer holds
	// subscription signing secrets encrypted at rest under an engine-held key.
	// A sealer failure keeps the module's fail-closed default (no subscriptions
	// can be created; existing ones consume their retry ladder recording
	// secret_unavailable and dead-letter if the sealer never returns — visible
	// in the DLQ, recoverable by redeliver) — loud, never a cleartext downgrade
	// and never a boot abort.
	b.set.eventing.UseAuthorizer(b.authz)
	if b.set.sessions != nil {
		b.set.sessionDependencies.CommunicationAuthority = sessions.NewCommunicationRequestAuthority(b.authr, b.authz)
		b.set.sessionDependencies.WorkAuthorizer = b.authz
		b.set.sessionDependencies.QueuedCredentialCapture = b.authr.BindQueuedCredential
		b.set.sessionDependencies.QueuedLaunchAuthorization = func(ctx context.Context, tenant model.TenantID, credential auth.QueuedCredential, runID string, workspace model.ID) (auth.Principal, error) {
			return authorizeQueuedSessionLaunch(ctx, b.authr, b.authz, tenant, credential, runID, workspace)
		}
		// P2/W3: the managed Stop's composition ports. The resolver reconstructs a
		// caller's evidence from its credential reference, the authorizer is the same
		// composed request authorizer that gates live routes, and the elector is the
		// store's own durable leader fence. No private route calls StopManagedRun yet
		// (W4); binding the ports makes the module's own admission answerable.
		b.set.sessionDependencies.ManagedStopAuthority = sessions.NewManagedStopAuthority(b.authr, b.authz, b.st.Leader())
	}
	// CM-10: a workflow run binds to the exact credential that starts it, and its
	// reauthorization binds the successor, through the serving Authenticator. The
	// binding is resolved at each communication effect by the pair bound above;
	// orchestration only keeps the opaque handle.
	if b.set.orchestration != nil {
		b.set.orchestration.UseWorkflowCredentialBinder(b.authr)
	}
	// J10-S3: each publication effect rebuilds its caller from the exact
	// credential through the serving Authenticator and is authorized by the
	// same composed Authorizer that gates live routes. Unbound is deny-closed.
	if b.set.gitpublish != nil {
		b.set.gitpublish.UseAuthority(b.authr, b.authz)
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
	if rollout, ok := newEventingEgressRollout(b.st); ok {
		b.set.eventing.UseEgressRollout(rollout)
		logEventingEgressRollout(ctx, b.log, rollout, osGetenv(envEventingEgressPolicy) != "")
	} else {
		return fmt.Errorf("eventing: the store does not expose durable rollout state, so the egress destination control cannot establish whether it is in force")
	}
	// Unit H: the writer fence's own durable disposition. Its own key, its own epoch —
	// deriving it from the destination control's classification is the critical defect an
	// adversarial review of this design found, in both directions (see
	// eventing.EgressWriterFenceControlKey). Boot-fatal for the same reason as above: a control
	// present in the code and absent from the binary's behavior is what this campaign has
	// already shipped once.
	if fence, ok := newEventingWriterFence(b.st); ok {
		b.set.eventing.UseEgressWriterFence(fence)
		logEventingWriterFence(ctx, b.log, fence)
	} else {
		return fmt.Errorf("eventing: the store does not expose durable rollout state, so the egress writer fence cannot establish whether it is armed")
	}
	b.eventingSealerPresent = false
	if sealer, serr := newEventingSealer(b.cfg.DataDir, osGetenv); serr != nil {
		b.log.Error("eventing: secret sealer unavailable; subscriptions are disabled until fixed", "err", serr)
	} else {
		b.set.eventing.UseSecretSealer(sealer)
		b.eventingSealerPresent = true
	}

	// the managed SSO config service. It backs the console SSO endpoints
	// and resolves the live federation provider from a store-backed, SEALED config —
	// so SSO is configurable from the console, not env-only. Since the
	// single-IdP provider BUILDER is OPEN-CORE (newFederationBuilder is non-nil in
	// BOTH builds), so the base AGPL build does real single-IdP login; the
	// env-configured provider is the FALLBACK used only when no managed config row
	// exists. The reserved MULTI-IdP capability (the federationMultiIDP port) is nil in the
	// base build — that nil is what enforces the single-IdP cap (a second active IdP
	// returns multi_idp_requires_enterprise) and limits Resolve to the global config.
	// A sealer failure leaves the service without a sealer: config WRITES fail closed
	// (never cleartext), reads/login still work — loud, never a boot abort, never a
	// cleartext downgrade.
	var fedSealer auth.FederationSealer
	b.federationSealerPresent = false
	if sealer, serr := newFederationSealer(b.cfg.DataDir, osGetenv); serr != nil {
		b.log.Error("federation: SSO secret sealer unavailable; SSO config writes are disabled until fixed", "err", serr)
	} else {
		fedSealer = sealer
		b.federationSealerPresent = true
	}
	b.fedSvc = auth.NewFederationService(b.st, fedSealer, newFederationBuilder(), newFederation(osGetenv, b.log), thisEdition.federationMultiIDP.get())
	// U8: converge the derived home-realm domain index (federation_domain_claims) from the
	// authoritative ClaimedDomains on every config, so an UPGRADED deployment's existing domains
	// route via the indexed path and any legacy cross-config duplicate is quarantined (deny-closed
	// to the global IdP) instead of mis-routing. Idempotent + collision-safe; loud on failure but
	// never a boot abort — a lagging index only degrades home-realm routing to the global fallback.
	// late-bind the SSO posture the identity console reports at
	// /v1/m/identity/sso. It happens HERE because the federation service needs the
	// store and the config sealer, so it does not exist when buildModules runs.
	if b.set.identityConsole != nil {
		b.set.identityConsole.UseSsoPosture(fedSsoPosture{svc: b.fedSvc})
	}
	if err := b.fedSvc.ReconcileDomainClaims(ctx); errors.Is(err, store.ErrNotLeader) {
		// A standby never writes: the active writer converges the index at its own boot,
		// and a `dr backup` run beside a serving node is always a standby.
		b.log.Info("federation: home-realm domain index not reconciled; this node is a standby and the active writer owns it")
	} else if err != nil {
		b.log.Error("federation: home-realm domain index reconcile failed; home-realm routing degraded to the global IdP until fixed", "err", err)
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
	b.secretStoreSealerPresent = false
	if sealer, serr := newSecretSealer(b.cfg.DataDir, osGetenv); serr != nil {
		b.log.Error("secret-store: sealer unavailable; secret writes and store: references are disabled until fixed", "err", serr)
	} else {
		secretSealer = sealer
		b.secretStoreSealerPresent = true
	}
	b.secretStore = auth.NewSecretStore(b.st, secretSealer)
	if b.set.deploySetup != nil {
		b.set.deploySetup.secrets = b.secretStore
	}
	b.set.workspaceSecrets.vault = b.secretStore
	if b.set.knowledge != nil {
		b.set.knowledge.UseSourceOpener(workspaceKnowledgeSources{principals: b.authr, scopes: b.set.sourceScopeResolver, authz: b.authz, vault: b.secretStore}.Open)
	}
	b.secretResolver = newSecretResolver(b.secretStore, osGetenv, b.log)
	if err := bindManagedSCIMModules(b.set.all, b.st, b.authr, b.secretStore, b.authz); err != nil {
		return err
	}

	// One deployment settings writer for the edition, the activation routes and the
	// module selection. It reads which modules hold data, so an add-on that is
	// disabled keeps those of its modules selected (productSettings.write).
	b.settings = newProductSettings(b.st, b.cfg.DataDir)
	b.settings.used = func(ctx context.Context) ([]string, error) { return usedModules(ctx, b.st, b.census) }
	b.tracingSettings = newTracingSettingsService(b.settings, b.tracer, b.cfg.Version)
	if err := b.tracingSettings.applyStored(ctx); err != nil {
		// An unreadable optional tracing choice must not stop the core or enable export.
		disabled := obstrace.FromEnv(b.cfg.Version)
		disabled.Enabled = false
		off, _ := obstrace.New(ctx, disabled)
		swapCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = b.tracer.Replace(swapCtx, off)
		cancel()
		b.log.Warn("tracing settings could not be applied; export is disabled")
	}

	// Live edition ports are bound only after the real Store, sessions data and
	// request authorizer and secret resolver exist, before api.New mounts routes.
	if thisEdition.bindModuleDependencies != nil {
		b.editionResources, err = thisEdition.bindModuleDependencies(ctx, editionConfigFrom(b.cfg), b.set.all,
			EditionDependencies{
				modules: &b.set,
				Store:   b.st, Sessions: b.set.sessions, RuntimeLaunches: b.set.sessions,
				Rows: api.NewReadRowAuthorizationPort(b.authz, b.authr), Mutations: b.authz,
				Principals: b.authr, Governance: b.set.gov, Secrets: b.secretResolver,
				Authenticator: b.authr, FederationService: b.fedSvc, SecretStore: b.secretStore,
				RequestRestart: restartRequester(b.cfg.Restart), ProductSettings: b.settings,
				RegisterJobNotRunning: b.notRunning.register,
			}, b.log)
		if err != nil {
			_ = b.st.Close()
			return fmt.Errorf("bind edition module dependencies: %w", err)
		}
	}
	b.cleanup = append(b.cleanup, func() {
		if !b.bootOK {
			closeEditionResources(b.editionResources, b.log)
		}
	})

	return nil
}

func (b *bootState) bindSources(ctx context.Context) (err error) {
	// the durable source roster (the store-backed successor to the file's
	// `sources[]`) and the live reconciler that wires it into the running runtime
	// without a restart. The connector scratch dir is created here (rather than just
	// before Start) so the reconciler can extract a first-party plugin binary on a
	// LIVE add too; it is removed on Close (or if the boot aborts). ConnectorTrust
	// for external (third-party) plugins stays operator-file config (a restart-time
	// trust decision), read once and handed to the reconciler.
	b.sourceStore = auth.NewSourceStore(b.st)
	connectorDir, derr := newConnectorScratchDir(b.cfg.DataDir)
	b.connectorDir = connectorDir
	if derr != nil {
		_ = b.st.Close()
		return fmt.Errorf("connector scratch dir: %w", derr)
	}
	b.cleanup = append(b.cleanup, func() {
		if !b.bootOK {
			_ = os.RemoveAll(b.connectorDir)
		}
	})
	b.sourceReconcilerSvc = newSourceReconciler(b.rt, b.sourceStore, b.secretResolver, b.secretStore, b.connectorDir, b.srcCfg.ConnectorTrust, b.log)
	// B1: this node's persistent execution-environment identity, resolved from
	// node-local state (or the explicit override) — never from the license, a
	// region, a tenant or a shared database row. Reconciled sources are registered
	// with it so their events carry the applied roster snapshot; the sessions
	// module launches only profiles that name it and validates bindings through the
	// narrow port below. Unavailable ⇒ new launches are deny-closed. Historical
	// legacy reads and stops remain available without inventing a profile.
	executionEnvRef, envErr := resolveExecutionEnvironmentRef(b.cfg.DataDir, !b.cfg.ReadOnly || !b.storeIsRemote, osGetenv, b.log)
	if envErr != nil {
		_ = b.st.Close()
		return envErr
	}
	if executionEnvRef != "" {
		b.sourceReconcilerSvc.useEnvironmentRef(executionEnvRef)
	}
	if b.set.sessions != nil {
		b.set.sessions.UseExecutionEnvironmentRef(executionEnvRef)
		// Where this node keeps the provider account homes it creates. The module
		// holds no data directory and may import nothing from here, so the root is
		// resolved once and handed over; a deployment that keeps the homes outside
		// the data directory supplies its own root to the same seam. Unresolvable
		// leaves the seam empty, and creating an account home stays deny-closed.
		if accountsRoot, arErr := accounthome.Root(b.cfg.DataDir, ""); arErr == nil {
			b.set.sessions.UseAccountsRoot(accountsRoot)
		} else {
			b.log.Warn("sessions: no accounts root on this node; creating a provider account home is deny-closed",
				"error", arErr.Error())
		}
		// A profile that signs in with the tool's own login gets a HOME the product
		// creates under the data directory, never the engine user's own home, and the
		// login itself lives in the tenant's own home there too (FH 036).
		if b.cfg.DataDir != "" {
			if dataDir, absErr := filepath.Abs(b.cfg.DataDir); absErr == nil {
				b.set.sessions.UseProfileHomesRoot(filepath.Join(dataDir, "profile-homes"))
				b.set.sessions.UseToolLoginsRoot(filepath.Join(dataDir, toolLoginsDir))
			}
		}
		b.set.sessionDependencies.ProviderSources = &providerSourceResolver{
			store: b.sourceStore, sr: b.sourceReconcilerSvc, authz: b.authz, env: executionEnvRef,
		}
		// The provider-record plane. The VAULT is wired only when the engine has
		// a sealer — with none, registering a provider is refused with the wiring
		// named, which is the honest answer: the engine never stores a credential it
		// cannot protect. The PROBE is always available; it is an outbound model-list
		// call and it sends no completion.
		if b.secretStoreSealerPresent {
			b.set.sessionDependencies.ProviderVault = providerSecretVault{store: b.secretStore}
		} else {
			b.log.Warn("sessions: provider registration is disabled; no secret sealer is available",
				"effect", "the console and the CLI refuse to register a provider and say so",
				"unchanged", "launching with the host credential variables")
		}
		b.set.sessionDependencies.ProviderProbe = newProviderProbe()
	}
	// J10-S3: the publication module's custody and closed git executor. Its
	// approved bindings come from the operator's source roster, and each
	// credential only from the sealed secret store under git-host/
	// (gitpublishcustody.go). Without a sealer or a git executable, both stay
	// unbound and every publication effect answers unavailable.
	if b.set.gitpublish != nil {
		if custody, executor, gerr := newGitPublication(b.cfg.DataDir, b.sourceStore, b.secretStore, b.secretStoreSealerPresent); gerr != nil {
			b.log.Warn("gitpublish: Git-host publication is unavailable until fixed; every push, pull request and merge is refused, and so is a session's GitHub read credential (git_read)", "err", gerr)
		} else {
			b.set.gitpublish.UseCustody(custody)
			b.set.gitpublish.UseGit(executor)
			// The same custody mints a session's GitHub read credential (git_read).
			if dir, derr := filepath.Abs(b.cfg.DataDir); derr != nil {
				b.log.Warn("sessions: a session's GitHub read credential (git_read) is unavailable; such launches are refused", "err", derr)
			} else if b.set.sessions != nil {
				b.set.sessionDependencies.GitRead, b.set.sessionDependencies.GitReadDataDir = custody, dir
			}
		}
		// A push that names a session run fetches its commit from the folder the
		// sessions module recorded for that run. Without sessions, such a push is
		// refused; a push that names no run is unchanged.
		if b.set.sessions != nil {
			b.set.gitpublish.UseSessions(b.set.sessions)
		}
	}

	return nil
}

// departmentService builds the Business department surface, or nil when this edition
// has none: then the department move and the group placement answer 501.
func (b *bootState) departmentService() api.DepartmentService {
	if thisEdition.departmentService == nil {
		return nil
	}
	return thisEdition.departmentService(b.st, b.authr)
}

func (b *bootState) buildAPI(ctx context.Context) (err error) {
	// In-place edition: resolve the commercial license by precedence (explicit
	// --license > OLIVARES_LICENSE_PATH > OLIVARES_LICENSE > the data-dir default
	// file) and hold it in a LIVE, swappable holder so `license install` / the console
	// / a reload hot-apply a renewal with ZERO downtime, and an expiry degrades back
	// to the community edition — the only restart left is the binary swap
	// open→enterprise (the Grafana model). It never costs a user account: self-hosted
	// accounts are unlimited in every tier (B10). A configured-but-unreadable source fails
	// loudly (explicit operator intent); the absent data-dir default is just "none".
	licPub := license.DefaultPublicKey()
	licSrc, lerr := resolveLicense(b.cfg.LicenseFile, b.cfg.DataDir, osGetenv)
	if lerr != nil {
		_ = b.st.Close()
		return fmt.Errorf("resolve license: %w", lerr)
	}
	// The holder verifies with the data directory's license trust keyring (the embedded key plus
	// <data-dir>/license-trust.json), the same keyring install, upgrade and reload use.
	licHolder := newDataDirLicenseHolder(b.cfg.DataDir, licSrc, time.Now, b.log)
	// Boot posture: WARN if the engine starts already expired/invalid (honest
	// degradation — the enterprise add-ons are off, but the engine never crashes,
	// never loses data and never caps user accounts).
	switch d := licHolder.display(); d.status {
	case "grace":
		b.log.Warn("license: the installed commercial license is past expiry but inside its grace window at boot — enterprise entitlements are maintained for now; renew before the grace ends (data and user accounts intact)", "licensee", d.licensee, "source", d.source.Kind)
	case "expired":
		b.log.Warn("license: the installed commercial license is EXPIRED at boot — running the community edition until renewed (data intact)", "licensee", d.licensee, "source", d.source.Kind)
	case "invalid":
		// The reason, not a guess at it: this line used to name the KEY unconditionally, and a
		// signed container this build cannot read verifies against the key perfectly well.
		b.log.Warn("license: the installed license could not be read — running the community edition",
			"reason", errText(d.reason), "source", d.source.Kind)
	case "valid", "perpetual":
		b.log.Info("license: commercial license loaded", "status", d.status, "licensee", d.licensee, "source", d.source.Kind)
	}

	// Seat seam (retained, display-only since B10). Self-hosted user accounts are
	// UNLIMITED in every tier: the community policy reports unlimited, no policy can
	// refuse an account (core/auth.enforceSeatCapTx is an unconditional no-op), and
	// no license state — valid, expired or absent — changes that. The wiring stays so
	// the enterprise overlay keeps its injection point and the console keeps its
	// (usage-only) figure. Set once here, before the server is built — race-free.
	// The overlay binds all license consumers here, from the same live holder.
	b.authr.WithSeatPolicy(thisEdition.seatPolicy(licHolder, crlViewFromDataDir(b.cfg.DataDir)))

	// Login enforcement: require-SSO + network/IP allow-list over the login
	// surface. The default (AGPL) build wires nil (no loginPolicy port → no enforcement,
	// login byte-identical to today); the enterprise build injects the closed engine
	// (enterprise/ssoenforce), which reads the stored posture via fedSvc.Posture and
	// decides — the open binary never links the enforcement code. Set once here,
	// before the server is built — race-free, like WithSeatPolicy.
	var loginPolicy auth.LoginPolicy
	if thisEdition.loginPolicy != nil {
		loginPolicy = thisEdition.loginPolicy(osGetenv, b.fedSvc, b.log)
	}
	b.authr.WithLoginPolicy(loginPolicy)
	// The declared state and the policy actually wired must agree before any surface is
	// exposed: Wired without a policy, or a non-Wired state with one, is a node whose
	// own record contradicts what it serves.
	if err := b.loginCap.installAndAssert(b.authr, b.fedSvc); err != nil {
		_ = b.st.Close()
		return fmt.Errorf("login enforcement: %w", err)
	}

	// Login-time group mapping: the default (AGPL) build wires nil
	// (no groupMapper port → asserted IdP groups are extracted but never mapped to
	// grants); the enterprise build injects the reserved GroupMapper
	// (enterprise/federation), which resolves the groups an IdP asserts at login to
	// the tenant's directory groups so the existing MappedRole/group-subject grants
	// fire. The open binary never links the mapping code. Set once here, before the
	// server is built — race-free, like WithSeatPolicy/WithLoginPolicy.
	b.authr.WithGroupMapper(thisEdition.groupMapper.get())

	// Agent-OBO lifecycle check: the governance module's
	// CheckAgentForExchange method validates that a named agent exists with
	// kind=agent, is not blocked/orphaned, and the exchange subject IS the
	// agent's registered human sponsor. Without this wiring, any token-exchange
	// request with requested_actor is rejected (deny-closed). Set once here,
	// before the server is built — race-free, like the seat/login policies.
	b.authr.SetAgentLifecycleChecker(b.set.gov)

	// CAEP transmitter: emit agent-risk events to external SSF receivers.
	// The community (AGPL) build wires nil — no events are pushed outbound and the
	// open receiver (core/auth/caep_events.go) is unaffected (no rug-pull). The
	// enterprise build (caeptransmit_enterprise.go) reads
	// OLIVARES_CAEP_TRANSMITTER_CONFIG and signs + HTTP-pushes SETs to configured
	// endpoints (RFC 8935). Set once here before the server is built. Bus
	// subscription over circuit-breaker and session-revoke events is wired in a
	// follow-up task.
	if thisEdition.caepTransmitter != nil {
		// The transmitter is built to validate its operator config; its bus
		// subscription is wired in a follow-up (Task 7).
		if _, err := thisEdition.caepTransmitter(osGetenv, b.log); err != nil {
			return fmt.Errorf("load CAEP transmitter operator config: %w", err)
		}
	}

	// The engine-side edition/license service backs the console/CLI install/status +
	// the live server-info status. Pure edition plumbing — it never gates a feature
	// (LICENSING.md); it persists/observes/hot-applies the artifact and enforces the
	// downgrade-acknowledge guard against the LIVE active estate.
	b.licSvc = newLicenseService(licHolder, b.authr, b.cfg.DataDir, b.cfg.LicenseFile, osGetenv, thisEdition.name, b.log)

	// the enterprise activation service backs the console /v1/console/activation
	// surface (per-add-on state + enable/disable/promote). It is enterprise-only — the
	// community build has no activationService port, so the routes 501. It
	// reads/writes the SAME governed activation manifest the CLI does and audits changes
	// through the live store (SystemTenantID scope).
	var activation api.ActivationService
	if thisEdition.activationService != nil {
		activation = thisEdition.activationService(b.cfg.DataDir, b.st, thisEdition.name, b.log)
	}
	actSvc := recordingActivation(activation, b.settings, restartRequester(b.cfg.Restart), b.log)

	// Privileged login: the PIV/CAC route config is loaded once and shared
	// between the API (verification) and the serve command (the HTTP listener
	// must request the optional client certificate — engine.pivConfig).
	if b.cfg.ServeMode {
		b.pivCfg, err = loadPIVConfig(osGetenv, b.log)
		if err != nil {
			return fmt.Errorf("load PIV operator config: %w", err)
		}
	}

	// OPS-5 inbound rate limiting, always non-nil (secure-by-default), with
	// the shared-store selection on top: in HA the buckets must be GLOBAL
	// (per-node shards multiply every quota by the replica count — the limit
	// PR #33 documented). OLIVARES_RATELIMIT_STORE=postgres opts in (the Helm
	// chart sets it when replicaCount > 1); single-node stays in-proc and pays
	// no per-request round trip. Store outages degrade to per-node enforcement
	// behind a circuit breaker — bounded, counted, alertable, never unlimited.
	rlCfg, err := loadRateLimitConfig(osGetenv, b.log)
	if err != nil {
		return fmt.Errorf("load rate limit operator config: %w", err)
	}
	rlCfg.LogWarn = func(msg string, err error) { b.log.Warn(msg, "err", err) }

	if useShared, serr := resolveRateLimitStore(osGetenv, b.eng); serr != nil {
		return fmt.Errorf("rate limit store: %w", serr)
	} else if useShared {
		ps, perr := pgstore.Open(ctx, b.dsn, pgstore.Options{
			IdleTTL:  ratelimit.IdleTTLFor(rlCfg.Tiers),
			Registry: b.reg,
			Logger:   b.log,
			// Owner/app split: the bucket-table + take-function DDL needs
			// CREATE on the schema, which the app role deliberately lacks.
			DDLDSN: b.cfg.OwnerDSN,
		})
		if perr != nil {
			return fmt.Errorf("rate limit store: %w", perr)
		}
		b.rlStore = ps
		rlCfg.Store = ps
		b.log.Info("rate limit: shared Postgres bucket store active (global quotas across nodes)")
	}
	b.cleanup = append(b.cleanup, func() {
		if !b.bootOK && b.rlStore != nil {
			_ = b.rlStore.Close()
		}
	})

	// OTA update indicator: OPT-IN. With OLIVARES_UPDATE_ENDPOINT set AND a
	// release key embedded, run a cached background check the console reads via the
	// health summary. Unset (air-gapped) ⇒ nil ⇒ the console shows no indicator, no
	// error, and NO outbound calls — silence is the honest air-gap default.
	var (
		updateStatusFn  func() updatecheck.Status
		updateRefreshFn func(context.Context) updatecheck.Status
	)
	if ep := strings.TrimSpace(envconfig.Get("OLIVARES_UPDATE_ENDPOINT")); b.cfg.ServeMode && ep != "" {
		if pk := release.EmbeddedKey(); pk != nil {
			ch := strings.TrimSpace(envconfig.Get("OLIVARES_UPDATE_CHANNEL"))
			if ch == "" {
				ch = release.ChannelStable
			}
			checker := updatecheck.NewChecker(updatecheck.Config{
				Endpoint: ep, Channel: ch, CurrentVersion: b.cfg.Version,
				InstallID: resolveInstallID(b.cfg.DataDir), PubKey: pk,
			}, 6*time.Hour)
			go checker.Run(ctx)
			updateStatusFn = checker.Latest
			updateRefreshFn = checker.Refresh
			b.log.Info("update check enabled (console indicator)", "endpoint", ep, "channel", ch)
		} else {
			b.log.Warn("OLIVARES_UPDATE_ENDPOINT set but this build embeds no release key; update checking disabled")
		}
	}

	memoryKeyInfo := b.memoryKey.custodyInfo("memory-portability")
	memoryKeyInfo.Present = b.memoryKeyErr == nil
	keyCustody := api.KeyCustodyInfo{Keys: []api.KeyInfo{
		b.auditKey.custodyInfo("audit"),
		b.catalogKey.custodyInfo("catalog"),
		b.policyKey.custodyInfo("policy"),
		memoryKeyInfo,
		{
			Purpose:     "license",
			Algorithm:   "ed25519",
			Origin:      license.KeyOrigin(),
			Fingerprint: license.KeyFingerprint(),
		},
		sealerCustodyInfo("eventing", osGetenv(eventingSecretKeyEnv), b.eventingSealerPresent),
		sealerCustodyInfo("sso", osGetenv(federationSecretKeyEnv), b.federationSealerPresent),
		sealerCustodyInfo("secret-store", osGetenv(secretStoreKeyEnv), b.secretStoreSealerPresent),
	}}

	b.agentTools, err = agenttoolsapi.New(ctx, toolInstallEngine(ctx), filepath.Join(absOrSame(b.cfg.DataDir), "tools"), b.cfg.ReadOnly)
	if err != nil {
		return fmt.Errorf("agent tools API: %w", err)
	}
	// Sign-in runs the same executable a session launch runs (sessionruntime.go).
	toolObserver := newHostToolObserverForDataDir(b.cfg.DataDir, osGetenv)
	b.agentTools.SetProgramResolver(func(driver string) string {
		facts, _ := driverfacts.Lookup(driver)
		if bin := strings.TrimSpace(osGetenv(facts.RuntimeBinEnv)); facts.RuntimeBinEnv != "" && bin != "" {
			return bin
		}
		return installedSessionProgram(toolObserver, driver, exec.LookPath)
	})
	// FH 036: the tools' own logins live in a home the product creates per tenant
	// under the data directory (<data>/tool-logins/<tenant>/<driver>), never in the
	// engine user's own home; a tenant this node does not serve has none.
	if dataDir, absErr := filepath.Abs(b.cfg.DataDir); b.cfg.DataDir != "" && absErr == nil {
		loginsRoot := filepath.Join(dataDir, toolLoginsDir)
		b.agentTools.SetLoginHome(func(ctx context.Context, tenant model.TenantID, driver, accountRef string) (string, string, error) {
			served, err := servesTenant(ctx, b.st, tenant)
			if err != nil {
				return "", "", err
			}
			if !served {
				return "", "", errors.New("that organization is not served by this node")
			}
			if accountRef != "" {
				if b.set.sessions == nil {
					return "", "", errors.New("provider accounts are not available on this node")
				}
				home, configDir, err := b.set.sessions.ProviderLoginHome(ctx, tenant, driver, accountRef)
				return home, configDir, err
			}
			if driver == "opencode" && b.set.sessions != nil {
				return b.set.sessions.DefaultToolLoginHome(ctx, tenant, driver)
			}
			home, configDir, ok := sessions.ToolLoginHome(loginsRoot, tenant, driver)
			if !ok {
				return "", "", errors.New("this tool has no login home on this node")
			}
			return home, configDir, nil
		})
	}
	if b.set.sessions != nil {
		// The rule that picks a new session's profile reads the same sign-in
		// status the AI tools page shows (POST provider-profiles/resolve).
		b.set.sessionDependencies.ToolLogin = b.agentTools.LoginStatus
		b.set.sessionDependencies.ProfileLogin = b.agentTools.LoginStatusForProfile
	}
	wireModelAvailability(b.set, b.st, b.sourceReconcilerSvc)
	// A confirmed product sign-in queues a model-list refresh for that tenant
	// (MODELS CONSUMERS.md: FH owns the sign-in callback; it does no I/O itself).
	if b.set.models != nil {
		b.agentTools.OnSignedIn(b.set.models.WakeAvailability)
	}
	// HU-R17: the installed Ollama, run by the engine on request, with its models under
	// the data directory and its endpoint registered as a provider once it answers.
	ollamaDir := filepath.Join(absOrSame(b.cfg.DataDir), "ollama")
	b.agentTools.UseOllama(agenttoolsapi.OllamaConfig{
		ModelsDir: filepath.Join(ollamaDir, "models"),
		HomeDir:   filepath.Join(ollamaDir, "home"),
		Command:   confinedOllamaCommand(b.cfg.DataDir),
		Register:  registerLocalOllama(b.set.sessions, b.st),
		StateFile: filepath.Join(ollamaDir, "started.json"),
		Audit: func(ctx context.Context, draft model.AuditDraft) error {
			return b.st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
				_, err := sc.Audit().Append(ctx, draft)
				return err
			})
		},
	})
	// The Ollama a person started comes back with the engine.
	b.agentTools.RestartOllama()
	b.cleanup = append(b.cleanup, func() {
		if !b.bootOK {
			b.agentTools.Close()
		}
	})
	b.set.all = b.set.apiModules(b.agentTools)
	b.gatewayCfg, err = loadAgentGatewayConfig(b.log)
	if err != nil {
		return err
	}
	b.gatewayManagement, err = newMCPManagement(b.st, b.secretStore, b.gatewayCfg, osGetenv("OLIVARES_AGENT_GATEWAY_CONFIG") != "")
	if err != nil {
		return err
	}
	// server-info's communication_ready, sampled at most once per ttl.
	communicationReadiness := newCommunicationReady(func(ctx context.Context) (bool, error) {
		r, err := b.set.sessions.EvaluateCommunicationReadiness(ctx)
		return r.Effective, err
	})
	b.apiSrv, err = api.New(api.Options{
		Store: b.st, Authenticator: b.authr, Authorizer: b.authz, Signer: b.signer, Standing: b.standing,
		// The guards hide the store's registry; the workspace contents route reads it.
		Census: b.census, AuthorizationRecorder: b.set.gov,
		// Invitations are mailed only through the deployment's own destination,
		// with links to the console address the operator declared.
		InviteSender: newInviteSender(osGetenv, notifyDispatcherOf(b.set), b.cfg.PublicAddr, b.log),
		// ⛔ EL PRODUCTOR DE EVIDENCIA ES EL AUTENTICADOR, Y SIN ESTA LÍNEA UNA RUTA GOBERNADA
		// IMPIDE ARRANCAR. checkGovernedRoutes se niega a montar un módulo que registre rutas
		// gobernadas sin productor, así que mientras esto faltara, el día que un módulo real
		// registrase una gobernada el servidor no levantaba. Hoy ninguno lo hace: por eso el
		// hueco era invisible, y por eso se cierra ANTES de que el cockpit monte las suyas.
		//
		// Es `authr` y NO `recoveryAuthr`: el de recuperación se construye sobre otro runtime
		// de sesión y no es el que resuelve el alcance del principal de una petición normal.
		PrincipalEvidenceProducer: b.authr,
		NotEnabledModules:         notEnabledNamespaces(b.set.all, b.profile),
		CommunicationReady:        communicationReadiness.Ready,
		SetupToken:                b.setupTok, Logger: b.log, LogBroker: b.logBroker, Version: b.cfg.Version,
		// The build edition for server-info: the console shows only what this build serves.
		Edition: thisEdition.name,
		// the boot-owned registry, shared with the bus collectors and the
		// audit checkpointer so /metrics is one exposition.
		Metrics:          b.reg,
		LicensePublicKey: licPub,
		KeyCustody:       keyCustody,
		BusStats:         b.busStats,
		TLSCertNotAfter:  b.cfg.TLSCertNotAfter,
		// The jobs this node composes but cannot run (server-info jobs_not_running).
		JobsNotRunning: b.notRunning.list,
		// the LIVE edition/license service supersedes the static boot blob — it
		// backs /v1/console/license and the live server-info status (hot-applied
		// install/renewal/expiry reflects without a restart). Pure edition plumbing.
		License: b.licSvc,
		// enterprise activation surface (nil in the community build ⇒ 501).
		Activation: actSvc,
		// Departments and group places (nil in the community build ⇒ 501).
		Departments:     b.departmentService(),
		Modules:         b.set.all,
		TracingSettings: b.tracingSettings,
		// Which optional modules the engine runs (/v1/console/modules).
		ModuleSelection: moduleSelectionService{settings: b.settings, running: b.profile,
			restart: restartRequester(b.cfg.Restart), log: b.log, used: b.settings.used,
			sessions: b.set.sessions.RunningSessions},
		// whoami reports authority a tenant-scoped grant confers, not just what the
		// ROLE confers. Same module that DECIDES the request through the ScopedAuthorizer
		// above, in its reporting capacity — one source, two capabilities, so the set the
		// console is handed cannot drift from the engine that produces the 403.
		UnconditionalGrants: b.set.gov.UnconditionalGrants(),
		// SSO federation: the managed config service backs the console SSO
		// endpoints AND resolves the live provider (store-driven, with the
		// env-configured provider as the no-config fallback). The default build has
		// no provider builder, so login answers 501 until rebuilt -tags enterprise.
		FederationService: b.fedSvc,
		// the runtime secret store backs the console/CLI secret CRUD endpoints
		// (/v1/console/secrets). Superadmin + AAL3 gated; secrets are sealed at rest
		// and only a non-secret hint is ever returned.
		SecretStore: b.secretStore,
		MCPGateway:  b.gatewayManagement, MCPGatewayRuntime: b.gatewayManagement,
		// A browser preview of a port a live session listens on (an internal design note (not shipped)).
		SessionPreview: sessionPreviewHandler(b.set.sessions),
		// the live source-reconfiguration surface backs the console/CLI source
		// CRUD and POST /v1/console/runtime/reload — add/remove/rotate connectors in
		// the running engine without a restart. Superadmin + AAL3 gated.
		SourceRoster: b.sourceReconcilerSvc,
		ContentDiff:  githostdiff.New(b.sourceReconcilerSvc, b.secretResolver), // J10-S1: reads the roster's running GitHub/GitLab sources.
		// the console connector-onboarding surface (same reconciler) backs the
		// descriptor catalog + sealed-credential CRUD + test under
		// /v1/console/connectors — it seals an inline credential into the secret store
		// and persists a reference-only source it applies live.
		ConnectorOnboarding: b.sourceReconcilerSvc,
		KnowledgeStatus:     b.set.knowledgeStatus,
		// OTA update-availability indicator (nil unless OLIVARES_UPDATE_ENDPOINT set).
		UpdateStatus:  updateStatusFn,
		UpdateRefresh: updateRefreshFn,
		// Wave 2: live effective-config projection and the shared support-bundle
		// safety policy. Both config callbacks are evaluated per request so
		// activation overlay changes and unknown env keys stay current.
		EffectiveConfig: func() []api.EffectiveConfigEntry {
			// The declared console address is reported from the resolution THIS
			// process performed at startup, not re-read from the environment: an
			// explicit empty flag clears a variable that is still set, and a flag
			// overrides one, so the environment is not what this engine is running
			// on. Every other row keeps its live activation behaviour.
			return effectiveConfigEntriesFor(os.Environ(), osGetenv, resolvedPublicAddr{
				addr: b.cfg.PublicAddr, source: b.cfg.PublicAddrSource, known: true,
			})
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
		LeaderRouteGate: b.haCfg.Gate,
		// enable console DR; dr_handler resolves an empty BackupDir to
		// <DataDir>/backups. The passphrase file — the CLI DR commands' own
		// $OLIVARES_DR_PASSPHRASE_FILE — is what lets the backup schedule run
		// unattended; without it the schedule runner refuses loudly instead of
		// writing a bundle it could not seal.
		DR: consoleDRConfig(b.cfg, b.eng, b.storeSchema, b.storeQuiesce),
		// Enable the collector→core ingest endpoint on the gRPC server (CB-1 option
		// C): pushed observations are authorized (ingest:write) and lifted onto the
		// bus through the runtime. Remote use requires --grpc-client-ca (mTLS).
		Ingest: b.rt,
		// OBS-03: the ingress trace-context extractor (continues the caller/mesh trace;
		// no-op when no collector — never breaks a request).
		Tracing: b.tracer,
		// OPS-5: inbound per-tenant/per-endpoint-class rate limiting. Always
		// non-nil (secure-by-default: production is rate-limited even unconfigured);
		// OLIVARES_RATELIMIT_CONFIG overlays tiers/quotas/mode/per-tenant assignment;
		// OLIVARES_RATELIMIT_STORE selects the shared Postgres buckets (above).
		RateLimit: rlCfg,
		// the residency registry, so org provisioning validates a tenant's
		// region pin against the configured regions (deny-closed).
		Residency: b.residencyReg,
		// privileged-session recording — every module route is gated and
		// captured through the recording module (deny-closed on recorded surfaces).
		Recorder: sessionRecorderOf(b.running.recorder),
		// V269 / COCKPIT-02 §3: the typed, sealed reader a module route uses when it
		// declares EntityRef.CoreKind. It never leaves the composition root as anything
		// wider than five facts.
		CoreEntityResolver: coreEntityResolver{st: b.st},
		// Privileged login: pinned WebAuthn relying party
		// (zero value = per-request derivation) and the PIV/CAC client-cert
		// route (nil = honest 501 seam, no elevation).
		WebAuthn:         b.webAuthn.RP,
		WebAuthnUnusable: b.webAuthn.Unusable,
		PIV:              b.pivCfg,
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
		_ = b.st.Close()
		return err
	}

	return nil
}

func (b *bootState) bindJobs(ctx context.Context) (err error) {
	if b.set.catalogDeploy != nil {
		b.set.catalogDeploy.handler = b.apiSrv.Handler()
	}
	// Provider permissions use the local service with their session principal.
	// Operator-configured bridge calls use the authenticated handler instead.
	if b.set.approvalBridge != nil {
		b.set.approvalBridge.UseLocalProposer(b.set.gov.EngineApprovals())
		b.set.approvalBridge.UseHandler(b.apiSrv.Handler())
		if b.set.sessions != nil && b.cfg.ServeMode {
			// The tenants every install can enumerate, this node's region and
			// service guards applied (servedWorkTenants): the default PostgreSQL
			// install has no BYPASSRLS admin pool for ListOrgs. Only serve owns
			// the launches waiting for an approval: an offline command would
			// start an approved one inside the CLI and end the others.
			b.set.sessionDependencies.ApprovalRecoveryTenants = func(c context.Context) ([]model.TenantID, error) {
				return servedWorkTenants(c, b.st)
			}
		}
		if b.set.gov != nil {
			b.set.gov.UseApprovalCapacity(b.authr.ApprovalCapacity)
			b.set.gov.UseApprovalAuthority(b.authr, b.authz)
		}
	}

	// subscribe the FinOps→upstream-cap backstop to the bus, AFTER the approval
	// bridge's handler is bound (its governed actuator gates through that bridge). nil
	// (opt-in OFF / unprovisioned) is a no-op. A subscribe error leaves the backstop
	// inactive — never a boot failure — and the finops_budget_cap finding still stands.
	if b.set.finopsBackstop != nil {
		if err := b.set.finopsBackstop.subscribe(b.bus); err != nil {
			b.log.Warn("finops-backstop: bus subscription failed; upstream-cap backstop inactive", "err", err)
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
	if thisEdition.circuitBreakerEngine != nil {
		b.circuitBreaker = thisEdition.circuitBreakerEngine(osGetenv, circuitBreakerDeps{Data: b.data, Gov: b.set.gov}, b.log)
	}
	subscribeCircuitBreaker(b.circuitBreaker, b.bus, b.log)

	// Late-bind the governed Chat execution adapter, here because this is the
	// first point at which every dependency it must not run without exists — the store,
	// the proxy governance policy, the context policy, the residency registry, the secret
	// resolver, the approval bridge, the bus and the SAME circuit-breaker instance the
	// finding rail drives. It is bound BEFORE HTTP serving and in ONE call, so there is no
	// window in which the port is reachable with half its gates wired. Nil unless the
	// explicit development activation selected it; a false result keeps Chat refused with
	// chat_execution_not_ready rather than failing the whole boot, which is the right
	// trade for a storeless or collector process that legitimately cannot serve it.
	if b.set.chatExecutor != nil {
		if b.set.chatExecutor.bind(chatExecutorDeps{
			Providers: b.set.sessions,
			Store:     b.st, Policy: b.set.inferenceProxy, ContextPolicy: b.set.knowledge,
			Residency: b.residencyReg, Secrets: b.secretResolver, Approvals: b.set.approvalBridge,
			Bus: b.bus, CircuitBreaker: b.circuitBreaker,
		}) {
			b.log.Info("models: governed Chat execution adapter bound (development_precheck)")
		} else {
			b.log.Error("models: governed Chat execution adapter could not be bound; POST /routing-policies/{id}/execute stays deny-closed for pinned Chat profiles")
		}
	}
	subscribeIncidentCloseLoop(ctx, osGetenv, b.bus, b.log)

	// subscribe the enterprise AI threat-intel feed engine (build-tag gated;
	// nil and a no-op in the default AGPL build). It turns curated signed-feed
	// content into ADDITIVE findings (agentic signatures over guardrail.observed,
	// MCP reputation over finding.reported, model-lifecycle over cost.sampled). Opt-in
	// + fail-inert; it never alters the open engine's decisions.
	subscribeThreatIntel(ctx, osGetenv, b.bus, b.log)

	// late-bind the engine handler into the deploy IdentityBinder (the firm
	// per-agent NHI binding via in-process) and the drift loop, and register the
	// drift loop on the runtime's OWN periodic scheduler (before Start, like the roster
	// sync). Both were constructed in buildModules; nil when unconfigured (the module
	// keeps its degraded-attribution / no-loop defaults). A drift call before binding
	// fails closed (nil handler => the next tick runs).
	if b.set.deployBinder != nil {
		b.set.deployBinder.useHandler(b.apiSrv.Handler())
	}
	// C2 item 2: the drift loop registers only when deploy runs on this node
	// (module profile). The binder wiring is harmless either way.
	if deployDriftRegistrationEnabled(b.profile, b.set.deployDrift) {
		b.set.deployDrift.useHandler(b.apiSrv.Handler())
		if err := b.set.deployDrift.register(b.rt); err != nil {
			b.log.Warn("deploy-drift: could not register the drift loop on the scheduler; drift detection disabled", "err", err)
		}
	}

	// The continuous ledger archival (contract §8.5) and its operator config are
	// built BEFORE the job table: a config error fails the boot (unchanged), and
	// the sink is wired to the crypto-shred coordinator below. The table only
	// registers what was built.
	// Offline reads and data exports do not construct or run archive sinks.
	if !b.cfg.NoIngest && !b.cfg.ReadOnly {
		b.archCfg = loadAuditArchiveConfig(osGetenv, b.log)
		b.arch, err = newAuditArchiveLoop(b.archCfg, b.st, b.signer, b.auditKey.priors, b.log)
		if err != nil {
			return fmt.Errorf("load audit archive operator config: %w", err)
		}
	}
	// communicationPump is the registered local outbox pump (K1/K2 lanes plus the
	// J lane's own authority, used by the engine assembly below).

	// C2 item 1: one job table owned per module. Boot schedules only the
	// entries whose module this node's profile runs; every closure performs the
	// same constructor nil-check, coverage registration and warn the inline
	// block did.
	scheduleModuleJobs(b.profile, []moduleJob{
		{"compliance", "retention-sweep", func() {
			// the retention sweep (contract §6). nil = the operator disabled it
			// (warned); leader-gated per tick so a promoted standby picks it up.
			if sweep := newRetentionSweepLoop(osGetenv, b.st, b.running.compliance, b.log); sweep != nil {
				b.notRunning.coverageJob(b.enumerable, api.JobRetention, b.log)
				if err := sweep.register(b.rt); err != nil {
					b.log.Warn("retention-sweep: could not register the sweep loop on the scheduler; periodic retention disposition disabled", "err", err)
				}
			}
		}},
		{"finops", "finops-admission-recover", func() {
			// The FinOps admission recovery: every minute it retires stale claims and
			// releases their holds; every five it sweeps lapsed holds (admissionreconcile.go).
			if recon := newAdmissionReconciler(b.st, b.running.finops, b.log); recon != nil {
				if err := recon.register(b.rt); err != nil {
					b.log.Warn("finops-admission: could not register the recovery jobs on the scheduler; a stale claim is retired only when its key is retried, and a lapsed hold only expires by its TTL", "err", err)
				}
			}
		}},
		{"reporting", "reporting-schedule", func() {
			if thisEdition.reportScheduleJob != nil {
				thisEdition.reportScheduleJob(b)
			}
		}},
		{"", "dr-backup-schedule", func() {
			// the console backup schedule pump — evaluates the PERSISTED DR
			// schedule each tick and runs a due backup through the exact runBackup path.
			if pump := newDRSchedulePump(osGetenv, b.st, b.apiSrv, b.log); pump != nil {
				if err := pump.register(b.rt); err != nil {
					b.log.Warn("dr-schedule: could not register the backup schedule pump on the scheduler; scheduled backups disabled", "err", err)
				}
			}
		}},
		{"", "audit-archive", func() {
			if b.arch != nil {
				b.notRunning.coverageJob(b.enumerable, api.JobAuditArchive, b.log)
				if err := b.arch.register(b.rt); err != nil {
					b.log.Warn("audit-archive: could not register the archival loop on the scheduler; continuous ledger archival disabled", "err", err)
				}
				// the long-horizon legal-hold orchestrator reconciles object-lock
				// legal holds on the SAME archive sink (nil in the community build).
				if hold := thisEdition.longHorizonHold; hold != nil {
					if loop := newLongHorizonHoldLoop(osGetenv, b.st, hold(osGetenv, b.arch.sink, b.set.compliance, b.log), b.log); loop != nil {
						b.notRunning.coverageJob(b.enumerable, api.JobLegalHoldArchive, b.log)
						if err := loop.register(b.rt); err != nil {
							b.log.Warn("audit-legalhold: could not register the reconciliation loop on the scheduler; archive legal-hold reconciliation disabled", "err", err)
						}
					}
				}
			}
		}},
		{"eventing", "eventing-dispatch", func() {
			// the eventing dispatch pump (retry cadence, crash recovery,
			// retention pruning), leader-gated per tick. nil = disabled (warned loudly).
			if pump := newEventingPump(osGetenv, b.st, b.running.eventing, b.log); pump != nil {
				if err := pump.register(b.rt); err != nil {
					b.log.Warn("eventing-dispatch: could not register the pump on the scheduler; webhook retries and pruning disabled", "err", err)
				}
			}
		}},
		{"sessions", "sessions-work-outbox", func() {
			// K1/K2: recover committed WorkOutbox rows and expired WorkLease owners
			// after a process restart.
			if pump := newWorkOutboxPump(osGetenv, b.st, b.set.sessions, b.log); pump != nil {
				pump.useCommunication(b.communicationComposition.pumpWitness(), b.communicationComposition.outboxAuthority())
				if b.cfg.workOutboxPumpPrepared != nil {
					b.cfg.workOutboxPumpPrepared(pump)
				}
				if err := pump.register(b.rt); err != nil {
					b.log.Warn("sessions-work-outbox: could not register the pump; durable work events remain pending (K3 lane stays OFF)", "err", err)
				}
				b.communicationPump = pump
			}
		}},
		{"sessions", "run-lineage-repair", func() {
			// P2/W3: the bounded run-lineage repair, leader-gated per tick and per
			// page. nil only when the sessions module is not composed.
			if loop := newRunLineageRepairLoop(b.st, b.set.sessions, b.log); loop != nil {
				if err := loop.register(b.rt); err != nil {
					b.log.Warn("run-lineage-repair: could not register the repair loop on the scheduler; runs without authorization lineage stay hidden from confined readers", "err", err)
				}
			}
		}},
		{"orchestration", "orchestration-cadence", func() {
			// the orchestration cadence pump — the cadence-miss anti-evasion
			// scan per business tenant. nil = the operator disabled it (warned).
			if pump := newOrchCadencePump(osGetenv, b.st, b.running.orchestration, b.log); pump != nil {
				if err := pump.register(b.rt); err != nil {
					b.log.Warn("orchestration-cadence: could not register the pump on the scheduler; unattended cadence-miss detection disabled", "err", err)
				}
			}
		}},
		{"orchestration", "orchestration-workflow", func() {
			// the workflow-run pump — advances running DAG workflows per
			// business tenant. nil = the operator disabled it (warned).
			if pump := newOrchWorkflowPump(osGetenv, b.st, b.running.orchestration, b.log); pump != nil {
				if err := pump.register(b.rt); err != nil {
					b.log.Warn("orchestration-workflow: could not register the pump on the scheduler; wait/approval-gate workflow steps will not advance in the background", "err", err)
				}
			}
		}},
		{"notify", "notify-dispatch", func() {
			// the durable notification-outbox pump. With routing enqueue-only,
			// this pump is what actually delivers, so a disable warns.
			if pump := newNotifyPump(osGetenv, b.st, b.running.notify, b.log); pump != nil {
				if err := pump.register(b.rt); err != nil {
					b.log.Warn("notify-dispatch: could not register the pump on the scheduler; notifications will NOT be delivered", "err", err)
				}
			}
		}},
		{"gitpublish", "gitpublish-sweep", func() {
			// J10-S3: the publication sweep settles stale dispatches and re-observes
			// uncertain intents; it never dispatches. 0 disables it (warned).
			if b.running.gitpublish != nil {
				if interval, ok := gitpublishSweepInterval(osGetenv(gitpublishSweepIntervalEnv), b.log); ok {
					if err := b.rt.SchedulePeriodic(gitpublishSweepJobName, interval, false, b.running.gitpublish.SweepPump(b.st.Leader(), gitpublishSweepTenants(b.st, b.log))); err != nil {
						b.log.Warn("gitpublish-sweep: could not register the sweep on the scheduler; stale publications are not settled", "err", err)
					}
				}
			}
		}},
		{"siemforward", "siem-ledger-forward", func() {
			// the SIEM ledger-forward pump — walks each tenant's audit ledger
			// from a cursor and forwards to SIEM towers over the eventing engine.
			if pump := newLedgerForwardPump(osGetenv, b.st, b.running.siemforward, b.log); pump != nil {
				if err := pump.register(b.rt); err != nil {
					b.log.Warn("siem-ledger-forward: could not register the pump on the scheduler; the audit ledger will not reach SIEM towers", "err", err)
				}
			}
		}},
	})

	// SR2C P2 (round 2): the work-outbox pump's nudge goroutine and the global
	// sessions callback went live inside register() above. On any boot failure
	// after this point (the hook-config refusal below is the reachable one) the
	// bootOK unwind must stop the pump — cancel + join the goroutine and
	// withdraw the callback — or a failed boot leaks both, pointing at a dead
	// store. On success the engine's Close() owns stop.
	b.cleanup = append(b.cleanup, func() {
		if !b.bootOK && b.communicationPump != nil {
			b.communicationPump.stop()
		}
	})

	return nil
}

func (b *bootState) bindSessionGovernance(ctx context.Context) (err error) {
	// bind the enterprise RTBF crypto-shred coordinator's evidence ports —
	// the legal-hold checker over the compliance module and the SAME WORM archive
	// sink the ledger archival writes to (nil when archival is off; the coordinator
	// then blocks WORM-coordinated shreds, deny-closed). A no-op in the default
	// build (wire_noenterprise.go) and when the coordinator is not configured.
	var archSink audit.ArchiveSink
	if b.arch != nil {
		archSink = b.arch.sink
	}
	if thisEdition.bindCryptoShredPorts != nil {
		thisEdition.bindCryptoShredPorts(b.set.rtbfCoordinator, archSink, b.archCfg.sink, b.set.compliance, b.log)
	}

	// late-bind the OPERATE governance onto module II (sessions) before Start —
	// the kill-switch StopGate + active-termination sweep, the budget/HITL/PEP
	// LaunchGate, and the I/O Recorder. Module II was constructed first (its live
	// read-model backs the evals monitor), so its governance gates late-bind here, now
	// that the store, the approval bridge and FinOps are all live. stopDeny is the
	// shared throttled deny recorder (the engine reuses the same instance below).
	b.stopDeny = newStopDenyRecorder(b.st, b.log)
	b.sessionHooks = newSessionHookCredentials(b.authr, b.st, b.set.sessions, b.set.gov)
	// Bind ordinary turn costs to the same current session principal as hooks
	// and provider approvals. The owning port publishes the cost event itself.
	b.set.sessionDependencies.CostSink = &sessionCostSink{credentials: b.sessionHooks.SessionCredentials, sessions: b.set.sessions}
	// A turn its tool metered and did not price (Codex) is priced at list price.
	b.set.sessionDependencies.ListPricer = sessionListPrice
	b.set.sessionDependencies.ProviderApprovalPrincipal = func(c context.Context, tenant model.TenantID, runRef string) (auth.Principal, string, error) {
		p, scope, err := b.sessionHooks.ResolveRun(c, tenant, runRef)
		return p, scope.SessionRef, err
	}
	providerPolicy := sessionProviderPolicy{credentials: b.sessionHooks.SessionCredentials, eval: b.policyEval, scoped: b.set.gov.ScopedGrants(), authz: b.authz, approvals: b.set.gov.EngineApprovals(), store: b.st, redactSecrets: b.set.sessions.RedactSessionSecretsWithSpans}
	b.set.sessionDependencies.ProviderApprovalPolicy = providerPolicy.Decide
	b.set.sessionDependencies.ApprovalGate = providerApprovalAdapter{bridge: b.set.approvalBridge, approvalWait: b.set.sessions.BeginApprovalWait, reviewFacts: providerPolicy.reviewFacts, reviewReason: providerPolicy.reviewReason}
	_, hookErr := loadHookPEPConfig(b.log)
	if hookErr != nil {
		return hookErr
	}
	wireSessionGovernance(b.running, b.st, b.stopDeny, b.bus, osGetenv, b.log, b.sessionHooks.provisioner())

	// the guardian sweep pump — executes guardian containments whose HITL
	// approval landed (the human's click takes effect within one tick). Same
	// posture as the eventing pump: runtime scheduler, leader-gated per tick.
	if pump := newGuardianPump(osGetenv, b.st, b.set.gov, b.log); pump != nil {
		if err := pump.register(b.rt); err != nil {
			b.log.Warn("guardian-sweep: could not register the pump on the scheduler; approved containments will not auto-execute", "err", err)
		}
	}

	// Optionally provision a synthetic demo estate (org + agents + a seed source)
	// BEFORE Start, so its agent-origin edges attribute and its observations flow
	// through the real bus. Demo-only; never touches a real estate's data.

	if b.cfg.DemoSeed {
		t, derr := seedDemoEstate(ctx, b.st, b.rt, time.Now())
		if derr != nil {
			_ = b.st.Close()
			return fmt.Errorf("seed demo estate: %w", derr)
		}
		b.demoTenant = t
	}

	// CB-1 — wire the real ingestion as a PRODUCTION caller. Identity roster providers
	// are resolved from OLIVARES_SOURCES_CONFIG BEFORE rt.Start (unchanged: the roster
	// is read-once boot config, requires-restart to change). The OBSERVATION SOURCES,
	// since live in the DURABLE ROSTER (the sealed store), not the file: the
	// file's `sources[]` is imported once as a bootstrap seed (below), then the table
	// is authoritative and the live reconciler — run just after Start — wires it. An
	// unconfigured/un-embedded source or an empty roster warns honestly.
	if b.set.deferredSecrets != nil {
		b.set.deferredSecrets.rt = b.rt
		b.set.deferredSecrets.connectorDir = b.connectorDir
	}
	b.set.deferredSecrets.openAll(ctx, b.secretResolver, b.log)
	seedSourceRosterIfEmpty(ctx, b.sourceStore, b.srcCfg, b.log)
	wireRoster(ctx, b.rt, b.set.gov, b.set.wif, b.srcCfg, b.secretResolver, b.log)

	return nil
}

func (b *bootState) startRuntime(ctx context.Context) (err error) {
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
	if b.cfg.NoIngest {
		b.log.Debug("boot: roster-read boot — the runtime was not started and no source was wired")
	} else {
		if err := b.rt.Start(ctx); err != nil {
			_ = b.st.Close()
			return fmt.Errorf("start runtime: %w", err)
		}

		// the initial source reconcile — wire every enabled roster row into the
		// running engine. Identical to a later live reload, so boot and reload converge
		// (the ReloadActivePDP precedent). Deny-closed per source; a per-source failure
		// is logged, never aborts the boot.
		if report, rerr := b.sourceReconcilerSvc.reconcile(ctx); rerr != nil {
			b.log.Warn("ingest: initial source reconcile failed; the engine starts with no live sources until a reload succeeds", "err", rerr)
		} else {
			b.log.Info("ingest: sources wired from the durable roster",
				"added", len(report.Added), "rejected", len(report.Rejected))
			for _, rej := range report.Rejected {
				b.log.Warn("ingest: source not wired (deny-closed)", "name", rej.Name, "reason", rej.Reason)
			}
		}
	}

	// a SEALED operator config that could not be opened (revoked KEK, KMS
	// outage, custody typo) is a custody failure, not a missing config — the
	// loaders degraded honestly above, but the boot must fail closed rather than
	// run those subsystems silently unconfigured (docs/SECURITY-HARDENING.md: never a silent gap).
	if scErr := sealedConfigFailure(); scErr != nil {
		return fmt.Errorf("key custody: %w (a sealed config that cannot be opened fails the boot; unseal or fix the KEK to proceed)", scErr)
	}

	// advisory — surface any operator config that carried CLEARTEXT secrets
	// unsealed (one WARN per file; never fatal). See warnUnsealedSecretConfigs.
	warnUnsealedSecretConfigs(b.log)

	// a successful Postgres Open above proves every registered tenant table
	// has ENABLE + FORCE RLS + tenant policy (the store conformance self-test). With
	// the privileged-role opt-out disabled, it also proves the app role cannot bypass
	// RLS. Replica count and load-balancer state are external and cannot be attested
	// by this process, so this consolidated boot-report hook checks only the locally
	// observable posture. A single node is expected unless HA was requested.
	warnUnsupportedProductionPosture(b.log, b.eng, b.eng == store.EnginePostgres && !b.cfg.AllowPrivilegedDBRole, b.haCfg.Gate)

	return nil
}

func (b *bootState) finish(ctx context.Context) (*engine, error) {
	b.bootOK = true // success: the engine's Close() now owns tracer/bus shutdown
	// Stage-2: start the HA label resync loop now that boot cannot fail. It
	// converges the pod label onto live leadership every tick: it republishes after a
	// transient apiserver failure, restores a label edited out of band, and — since
	// the store's elector seam has no demotion callback (and giving a plain Postgres
	// elector Kubernetes-shaped callbacks would couple the cross-platform leadership
	// contract to the orchestrator) — demotes the label when this node loses the
	// lock. Its context is owned by the engine and canceled by Close.
	var haStop context.CancelFunc
	if b.haPublisher != nil {
		haCtx, cancel := context.WithCancel(context.Background())
		haStop = cancel
		go b.haPublisher.run(haCtx, b.st.Leader().IsLeader)
	}
	// The retirement pump runs for the life of the engine, a pass at a time and
	// only while this node is the active writer; a new leader resumes from the
	// records. A boot that does not start the runtime does not start it either.
	var retirementStop context.CancelFunc
	if !b.cfg.NoIngest {
		retirementCtx, cancel := context.WithCancel(context.Background())
		retirementStop = cancel
		go b.retirement.run(retirementCtx, b.st.Leader().Active)
	}

	b.runtimeEngine.haStop = haStop
	b.runtimeEngine.retirementStop = retirementStop
	b.runtimeEngine.legacyAdoptStop = b.legacyAdoptStop
	return b.runtimeEngine, nil
}

func warnUnsupportedProductionPosture(log *slog.Logger, eng store.Engine, effectiveRLSAttested, haRequested bool) {
	if eng == store.EnginePostgres && effectiveRLSAttested {
		return
	}

	if eng == store.EngineSQLite {
		if haRequested {
			log.Warn("HA requested with SQLite single-node; active-passive HA requires Postgres with RLS FORCE",
				"topology", "T1", "store", eng, "rls_force", false)
		} else {
			log.Info("running on SQLite single-node", "topology", "T1", "store", eng, "rls_force", false)
		}
		return
	}

	log.Warn("Postgres privileged-role opt-out enabled — effective RLS FORCE is not attested",
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
	if e.agentTools != nil {
		e.agentTools.Close()
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Stage-2: stop the HA label resync loop and hand the leader role back
	// before the store (and with it the election lock) closes, so the leader Service
	// drops this pod immediately rather than after the endpoint controller notices.
	if e.haStop != nil {
		e.haStop()
	}
	if e.retirementStop != nil {
		e.retirementStop()
	}
	if e.legacyAdoptStop != nil {
		e.legacyAdoptStop()
	}
	if e.haPublisher != nil {
		e.haPublisher.haShutdownLabel()
	}
	// K3: withdraw pump readiness before the scheduler stops, so a readiness
	// sample racing shutdown never reports a pump that will not tick again.
	if e.communicationPump != nil {
		e.communicationPump.stop()
	}
	_ = e.rt.Stop(stopCtx)
	closeEditionResources(e.editionResources, slog.Default())
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
	if d := envconfig.Get("OLIVARES_DATA_DIR"); d != "" {
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

// toolLoginsDir is where the tools' own logins live under the data directory, one
// home per tenant and tool (sessions.ToolLoginHome, FH 036).
const toolLoginsDir = "tool-logins"

// composeEngine resolves the engine/session cycle before runtime startup. A
// waiting launch recovered by sessions.Start sees the same MCP adapter and
// engine instance that serves subsequent API requests.
func (b *bootState) composeEngine() {
	b.runtimeEngine = &engine{
		store: b.st, rt: b.rt, signer: b.signer, authr: b.authr, authz: b.authz,
		editionResources: b.editionResources,
		agentTools:       b.agentTools, setupTok: b.setupTok, api: b.apiSrv, tracer: b.tracer, dataDir: b.cfg.DataDir, log: b.log,
		webAuthn:   b.webAuthn,
		logBroker:  b.logBroker,
		demoTenant: b.demoTenant, connectorDir: b.connectorDir, vectorIndex: b.set.vectorIndex, knowledgeMod: b.set.knowledge, sessionsMod: b.set.sessions, communicationPump: b.communicationPump,
		communicationComposition:  b.communicationComposition,
		workSink:                  workEventSink{eventing: b.running.eventing, notRunning: b.running.eventing == nil},
		moduleProfile:             b.profile,
		protocolBindingReconciler: b.set.protocolBindingReconciler,
		sessionHooks:              b.sessionHooks,
		engineApprovals:           b.set.gov.EngineApprovals(),
		gitpublish:                b.running.gitpublish,
		approvalBridge:            b.set.approvalBridge, policyEval: b.policyEval, scopedGrants: b.set.gov.ScopedGrants(), nhiEnforcer: b.set.gov,
		killSwitch: b.set.gov, stopDeny: b.stopDeny, pinVerifier: b.set.pinVerifier,
		circuitBreaker: b.circuitBreaker,
		models:         b.set.models, finops: b.set.finops, inferenceProxy: b.set.inferenceProxy, residencyReg: b.residencyReg, contentFirewall: b.set.contentFirewall,
		auditPriors: b.auditKey.priors, pivConfig: b.pivCfg, fedSvc: b.fedSvc, secretStore: b.secretStore,
		sourceStore: b.sourceStore, sourceReconciler: b.sourceReconcilerSvc, licenseService: b.licSvc,
		notifyDispatcher: b.set.deferredSecrets.notify, secretResolver: b.secretResolver,
		bus: b.bus, metrics: b.reg, rlStore: b.rlStore, wifBroker: b.set.wifBroker,
		haPublisher: b.haPublisher, haGate: b.haCfg.Gate,
		census: b.census, declared: b.declared, retirementPump: b.retirement,
		standing: b.standing,
		// server-info's jobs_not_running (jobsnotrunning.go).
		jobsNotRunning: b.notRunning, estateEnumerable: b.enumerable,
	}
	b.gatewayManagement.eng = b.runtimeEngine
	b.gatewayManagement.UseSessionCredentials(b.runtimeEngine.sessionHooks.SessionCredentials)
	b.runtimeEngine.sessionMCP = b.gatewayManagement
	b.set.sessionDependencies.SessionMCP = b.gatewayManagement
	b.runtimeEngine.gatewayConfig = &b.gatewayCfg
}

// sessionPreviewHandler is the sessions module's preview server, or nil when the
// module is not running, which leaves the preview prefix unmounted.
func sessionPreviewHandler(m *sessions.Module) http.Handler {
	if m == nil {
		return nil
	}
	return http.HandlerFunc(m.ServePreviewHTTP)
}
