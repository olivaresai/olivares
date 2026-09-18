// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B1 — provider profiles.
//
// A provider profile is the durable identity of ONE configured provider instance
// on ONE execution environment: which driver serves it, which environment owns
// its paths, and the canonical configuration home and user home the launched
// child runs under. It is configuration and storage identity, never an
// authenticated provider account: a home can change login without the plane
// knowing, and one account can be used from several homes. The plane therefore
// never reads, compares or shows credential material to guess an account
// (an internal design note (not shipped) §1–§2).
//
// What is immutable after creation is exactly what makes the profile an identity:
// driver, environment_ref, config_home and user_home. A visible name may change
// without changing identity; a retired profile keeps its id forever and a new
// incarnation of the same home is a NEW id. Two active labels for one home cannot
// exist, and the DATABASE says so through the home_slot unique index — not the
// writer's care.

// Registered kind and physical table.
const (
	providerProfileKind  model.Kind = "sessions.provider_profile"
	providerProfileTable            = "sessions_provider_profile"
)

// sessions.provider_profile columns.
const (
	colPPRef         = "profile_ref"
	colPPDriver      = "driver"
	colPPEnvRef      = "environment_ref"
	colPPConfigHome  = "config_home"
	colPPUserHome    = "user_home"
	colPPDisplayName = "display_name"
	colPPState       = "state"
	colPPHomeSlot    = "home_slot"
	colPPRetiredAt   = "retired_at"
	// colPPAuthSource is the EXPLICITLY AUTHORIZED authentication source of this
	// profile: `provider_account_home` or `managed_injection`. It is an
	// authorization, not identity — it can be changed by an operator with profile
	// write authority without creating a new profile id, and it is NOT part of the
	// home slot. Empty means no source was authorized, which is a refusal for
	// every driver except the historical Claude path it predates.
	colPPAuthSource = "auth_source"
)

// Profile lifecycle states.
const (
	// ProfileActive accepts launches, resumes and bindings.
	ProfileActive = "active"
	// ProfileDisabled refuses NEW launches/resumes; a live process it governs keeps
	// the authority its own gates gave it. Reversible: enabling keeps id and home.
	ProfileDisabled = "disabled"
	// ProfileRetired is irreversible for that id. It frees the home for a NEW
	// profile id and never revives old aliases or history.
	ProfileRetired = "retired"
)

// profileRefPrefix marks a provider profile id at a glance, the way sidPrefix
// marks a canonical session id.
const profileRefPrefix = "ppf_"

// homeSlotVersion and the unit-separator encoding of the active home slot. The
// separator is a control character neither a driver key nor an environment ref
// can contain (validated), and one a validated path cannot contain either, so no
// concatenation of three components can equal another triple's encoding.
const (
	homeSlotVersion   = "v1"
	homeSlotSep       = "\x1f"
	retiredSlotPrefix = "retired:"
)

// Bounds on the free-text fields.
const (
	maxProfileDisplayName = 200
	maxDriverKeyLen       = 64
	maxEnvironmentRefLen  = 256
	maxProfilePathLen     = 4096
)

// Profile-plane errors. Each carries the HTTP status the API answers with; the
// message never includes a path or a credential.
var (
	// ErrProfileNotFound is the typed not-found.
	ErrProfileNotFound = &runErr{http.StatusNotFound, "provider profile not found"}
	// ErrProfileImmutable refuses a change to driver/environment/home on an
	// existing profile: those ARE the identity, so changing them is creating
	// another profile.
	ErrProfileImmutable = &runErr{http.StatusConflict, "driver, environment and homes are immutable; create another profile"}
	// ErrProfileHomeTaken refuses a second active profile on one home.
	ErrProfileHomeTaken = &runErr{http.StatusConflict, "an active or disabled profile already owns this home on this environment"}
	// ErrProfileRetired refuses any lifecycle change or use of a retired id.
	ErrProfileRetired = &runErr{http.StatusConflict, "provider profile is retired"}
	// ErrProfileDisabled refuses a launch/resume/binding on a disabled profile.
	ErrProfileDisabled = &runErr{http.StatusConflict, "provider profile is disabled"}
	// ErrProfileForeignEnvironment refuses to validate, launch or bind a profile
	// whose environment is not this node's.
	ErrProfileForeignEnvironment = &runErr{http.StatusConflict, "provider profile belongs to another execution environment"}
	// ErrEnvironmentUnavailable is the deny-closed answer when the node has no
	// persistent execution-environment identity: the profiled path cannot be used,
	// the legacy path stays as documented.
	ErrEnvironmentUnavailable = &runErr{http.StatusServiceUnavailable, "execution environment identity is not available on this node; profiled launches are deny-closed"}
	// ErrProfileDriverNotOperable refuses to LAUNCH a profile whose driver has no
	// operated runner here. Such a profile may still be observed and bound.
	ErrProfileDriverNotOperable = &runErr{http.StatusUnprocessableEntity, "this driver has no operated runner on this node; the profile can be observed but not launched"}
)

// ProviderProfile is the module's own view of one profile row.
type ProviderProfile struct {
	ID             model.ID
	Ref            string
	Driver         string
	EnvironmentRef string
	ConfigHome     string
	UserHome       string
	DisplayName    string
	State          string
	// AuthSource is the authorized authentication source ("" = none authorized).
	AuthSource string
	Version    int64
	CreatedAt  string
	UpdatedAt  string
	RetiredAt  string
}

// ProviderHomeSnapshot is the non-secret launch snapshot resolved server-side from a
// profile: what the run row persists BEFORE the spawn and what the child is
// started with. It never carries a token, a secret reference or a prompt. The
// JSON names are part of the K4 launch digest of a PROFILED dispatch
// (runtime_work_launch.go): a different home under the same dispatch key is a
// different request and therefore a conflict.
type ProviderHomeSnapshot struct {
	ProfileID      string `json:"profile_id"`
	Driver         string `json:"driver"`
	EnvironmentRef string `json:"environment_ref"`
	ConfigHome     string `json:"config_home"`
	UserHome       string `json:"user_home"`
	// AuthSource is the AUTHORIZED authentication source resolved with the rest of
	// this snapshot, before intent, gates and reservation. `omitempty` is
	// load-bearing exactly as it is on the two fields above: a profile that
	// authorizes no source (every profile that predates this) encodes to the same
	// bytes as before, so a legacy or already-profiled dispatch keeps its EXACT K4
	// digest, while a launch that does carry a source digests it and a second
	// dispatch under another source is a conflict rather than a replay.
	AuthSource string `json:"auth_source,omitempty"`
}

// CreateProfileInput is the validated create request.
type CreateProfileInput struct {
	Driver      string
	ConfigHome  string
	UserHome    string
	DisplayName string
	// AuthSource is the authorized authentication source. Empty authorizes none,
	// which refuses every launch whose driver requires one.
	AuthSource string
	// EnvironmentRef may be empty (this node's environment) or equal to it. A
	// foreign environment cannot have its paths validated here, so it is refused.
	EnvironmentRef string
}

// registerProviderProfileSchema declares the three B1 entities: the profile, the
// source→profile binding and the profile-scoped provider alias. Declaring a new
// descriptor IS its migration (reconcileColumns creates a missing module table);
// the expression/partial indexes the live plane needs are NOT expressible here and
// live in the module's SQL migrations (schema.go, migrations/).
func (m *Module) registerProviderProfileSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  providerProfileKind,
		Table: providerProfileTable,
		Fields: []model.FieldSpec{
			{Name: colPPRef, Kind: model.KindText},
			{Name: colPPDriver, Kind: model.KindText},
			{Name: colPPEnvRef, Kind: model.KindText, Indexed: true},
			{Name: colPPConfigHome, Kind: model.KindText},
			{Name: colPPUserHome, Kind: model.KindText},
			{Name: colPPDisplayName, Kind: model.KindText},
			{Name: colPPState, Kind: model.KindText, Indexed: true},
			{Name: colPPHomeSlot, Kind: model.KindText},
			{Name: colPPRetiredAt, Kind: model.KindTimestamp, Nullable: true},
			// Nullable: an existing profile gains the column on the next boot and reads
			// as "no authorized source", which is the deny-closed value.
			{Name: colPPAuthSource, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{
			{Name: "sessions_provider_profile_ref_uniq", Columns: []string{model.ColTenantID, colPPRef}, Unique: true},
			// THE guarantee: one active/disabled profile per (environment, driver,
			// config_home). Concurrent creates of one home converge on ONE id because
			// the losing insert fails here, not because two writers agreed.
			{Name: "sessions_provider_profile_home_uniq", Columns: []string{model.ColTenantID, colPPHomeSlot}, Unique: true},
		},
	}); err != nil {
		return err
	}
	if err := m.registerProviderBindingSchema(reg); err != nil {
		return err
	}
	return m.registerProviderAliasSchema(reg)
}

// --- validation ---------------------------------------------------------------

// normalizeDriverKey lower-cases and validates a provider driver key. The set is
// OPEN on purpose (the same property SessionBinding.Provider has): a new engine
// must not need an edit here. What is closed is the SHAPE, so a key can be used
// as a scope component and compared byte for byte.
func normalizeDriverKey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", badRequest("driver is required")
	}
	if len(s) > maxDriverKeyLen {
		return "", badRequest("driver is too long")
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '-' || r == '_' || r == '.'):
		default:
			return "", badRequest("driver may use only lowercase letters, digits and - _ .")
		}
	}
	return s, nil
}

// validEnvironmentRef bounds an execution-environment reference: printable,
// no whitespace, no separator the home slot or a scope string uses.
func validEnvironmentRef(s string) bool {
	if s == "" || len(s) > maxEnvironmentRefLen || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r == 0x7f || r == ':' || r == '|' {
			return false
		}
	}
	return true
}

// canonicalHome validates a profile home ON THIS NODE: absolute, cleaned, its
// symlinks resolved to the real directory, existing and a directory. It never
// creates a missing home — an empty fallback home would silently give a session
// a fresh identity that nobody configured. The returned path is what the row
// stores and what the child receives; the child never re-resolves an operator's
// alias to some other destination.
func canonicalHome(field, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", badRequest(field + " is required")
	}
	if len(p) > maxProfilePathLen {
		return "", badRequest(field + " is too long")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", badRequest(field + " contains a control character")
		}
	}
	if !filepath.IsAbs(p) {
		return "", badRequest(field + " must be an absolute path")
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", &runErr{http.StatusUnprocessableEntity, field + " does not exist on this execution environment (a missing home is never created as a fallback)"}
		}
		return "", &runErr{http.StatusUnprocessableEntity, field + " is not accessible on this execution environment"}
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", &runErr{http.StatusUnprocessableEntity, field + " is not accessible on this execution environment"}
	}
	if !st.IsDir() {
		return "", &runErr{http.StatusUnprocessableEntity, field + " is not a directory"}
	}
	return real, nil
}

// revalidateHome re-checks an already-canonical stored home before a launch or a
// resume: it must still exist, still be a directory and still resolve to itself.
// A home that moved (an alias now pointing elsewhere) is a different location,
// and a launch into it would be a launch into an identity nobody registered.
func revalidateHome(field, stored string) error {
	real, err := canonicalHome(field, stored)
	if err != nil {
		return err
	}
	if real != stored {
		return &runErr{http.StatusConflict, field + " no longer resolves to the registered location"}
	}
	return nil
}

func validDisplayName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > maxProfileDisplayName {
		return "", badRequest("display_name is too long")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", badRequest("display_name contains a control character")
		}
	}
	return s, nil
}

// activeHomeSlot encodes the uniqueness key of a live (active or disabled) home.
func activeHomeSlot(envRef, driver, configHome string) string {
	return homeSlotVersion + homeSlotSep + envRef + homeSlotSep + driver + homeSlotSep + configHome
}

func retiredHomeSlot(ref string) string { return retiredSlotPrefix + ref }

func newProfileRef() string { return profileRefPrefix + string(model.NewID()) }

// validProfileRef bounds the opaque id a caller presents so a lookup never
// scans on garbage.
func validProfileRef(ref string) bool {
	if !strings.HasPrefix(ref, profileRefPrefix) || len(ref) > 128 {
		return false
	}
	id, err := model.ParseID(strings.TrimPrefix(ref, profileRefPrefix))
	return err == nil && !id.IsZero()
}

// --- local execution environment ---------------------------------------------

// UseExecutionEnvironmentRef late-binds this node's PERSISTENT local execution
// environment identity, resolved by the composition root from node-local state
// (never from the license, a region, a tenant or a shared database row). Empty
// keeps the profiled path deny-closed: nothing here derives a host id from a
// hostname, a cwd or a user name.
func (m *Module) UseExecutionEnvironmentRef(ref string) {
	ref = strings.TrimSpace(ref)
	if ref != "" && !validEnvironmentRef(ref) {
		if m.log != nil {
			m.log.Warn("sessions: execution environment ref is not usable; profiled launches stay deny-closed")
		}
		return
	}
	m.rt.environmentRef = ref
}

// ExecutionEnvironmentRef reports the bound local environment ("" = none).
func (m *Module) ExecutionEnvironmentRef() string { return m.rt.environmentRef }

func (m *Module) localEnvironment() (string, error) {
	if m.rt.environmentRef == "" {
		return "", ErrEnvironmentUnavailable
	}
	return m.rt.environmentRef, nil
}

// --- store operations ---------------------------------------------------------

func profileFromRecord(rec model.Record) ProviderProfile {
	return ProviderProfile{
		ID:             model.ID(rec.String(model.ColID)),
		Ref:            rec.String(colPPRef),
		Driver:         rec.String(colPPDriver),
		EnvironmentRef: rec.String(colPPEnvRef),
		ConfigHome:     rec.String(colPPConfigHome),
		UserHome:       rec.String(colPPUserHome),
		DisplayName:    rec.String(colPPDisplayName),
		State:          rec.String(colPPState),
		AuthSource:     rec.String(colPPAuthSource),
		Version:        rec.Int(model.ColVersion),
		CreatedAt:      rec.String(model.ColCreatedAt),
		UpdatedAt:      rec.String(model.ColUpdatedAt),
		RetiredAt:      rec.String(colPPRetiredAt),
	}
}

// findProfileRec reads one profile row by its opaque ref within a scope.
func findProfileRec(ctx context.Context, sc store.Scope, ref string) (model.Record, error) {
	repo, err := sc.Ext(providerProfileKind)
	if err != nil {
		return nil, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colPPRef, ref)}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrProfileNotFound
	}
	return recs[0], nil
}

// CreateProfile registers a new profile for a home on THIS environment. The
// paths are validated here, on the node that owns them, never trusted from the
// request; the request carries no environment, home slot, state or credential
// the server did not compute. A concurrent create of the same home loses on the
// unique index and is answered as the conflict it is.
func (m *Module) CreateProfile(ctx context.Context, tenant model.TenantID, in CreateProfileInput) (ProviderProfile, error) {
	if m.data == nil {
		return ProviderProfile{}, errNoData
	}
	env, err := m.localEnvironment()
	if err != nil {
		return ProviderProfile{}, err
	}
	if want := strings.TrimSpace(in.EnvironmentRef); want != "" && want != env {
		return ProviderProfile{}, &runErr{http.StatusUnprocessableEntity, "a profile for another execution environment must be created on that environment; this node cannot validate its homes"}
	}
	driver, err := normalizeDriverKey(in.Driver)
	if err != nil {
		return ProviderProfile{}, err
	}
	configHome, err := canonicalHome("config_home", in.ConfigHome)
	if err != nil {
		return ProviderProfile{}, err
	}
	userHome, err := canonicalHome("user_home", in.UserHome)
	if err != nil {
		return ProviderProfile{}, err
	}
	name, err := validDisplayName(in.DisplayName)
	if err != nil {
		return ProviderProfile{}, err
	}
	authSource, err := normalizeAuthSource(in.AuthSource)
	if err != nil {
		return ProviderProfile{}, err
	}
	ref := newProfileRef()
	var out ProviderProfile
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colPPRef:         ref,
			colPPDriver:      driver,
			colPPEnvRef:      env,
			colPPConfigHome:  configHome,
			colPPUserHome:    userHome,
			colPPDisplayName: name,
			colPPState:       ProfileActive,
			colPPHomeSlot:    activeHomeSlot(env, driver, configHome),
			colPPAuthSource:  authSource,
		}
		created, err := repo.Create(ctx, rec)
		if err != nil {
			return err
		}
		out = profileFromRecord(created)
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		return ProviderProfile{}, ErrProfileHomeTaken
	}
	if err != nil {
		return ProviderProfile{}, err
	}
	return out, nil
}

// GetProfile reads one profile by ref.
func (m *Module) GetProfile(ctx context.Context, tenant model.TenantID, ref string) (ProviderProfile, error) {
	if m.data == nil {
		return ProviderProfile{}, errNoData
	}
	if !validProfileRef(ref) {
		return ProviderProfile{}, ErrProfileNotFound
	}
	var out ProviderProfile
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, err := findProfileRec(ctx, sc, ref)
		if err != nil {
			return err
		}
		out = profileFromRecord(rec)
		return nil
	})
	return out, err
}

// ListProfiles returns the tenant's profiles, newest first, optionally narrowed
// to one state. It pages by the store's keyset cursor.
func (m *Module) ListProfiles(ctx context.Context, tenant model.TenantID, state string, q model.Query) ([]ProviderProfile, model.Page, error) {
	if m.data == nil {
		return nil, model.Page{}, errNoData
	}
	if state != "" {
		q.Filters = append(q.Filters, eq(colPPState, state))
	}
	var out []ProviderProfile
	var page model.Page
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(ctx, q)
		if err != nil {
			return err
		}
		out = make([]ProviderProfile, 0, len(recs))
		for _, rec := range recs {
			out = append(out, profileFromRecord(rec))
		}
		page = p
		return nil
	})
	return out, page, err
}

// ProfilePatch is the ONLY mutation a profile accepts after creation: a new
// label and/or a lifecycle transition. Everything else is identity.
type ProfilePatch struct {
	DisplayName *string
	State       *string
	// AuthSource re-authorizes (or withdraws, with "") the profile's
	// authentication source. It is not identity, so it does not create a new
	// profile; it does not reach a LIVE process either — a running child keeps the
	// source its own launch was authorized under, and the next launch resolves the
	// current one.
	AuthSource *string
}

// PatchProfile renames and/or transitions a profile in ONE validated transaction.
// Rename keeps id and home. active↔disabled is reversible. Retire is final: it
// frees the home slot for a NEW id and stamps retired_at; nothing else is rewritten
// — runs, aliases and history keep pointing at this id.
func (m *Module) PatchProfile(ctx context.Context, tenant model.TenantID, ref string, p ProfilePatch) (ProviderProfile, error) {
	if m.data == nil {
		return ProviderProfile{}, errNoData
	}
	if !validProfileRef(ref) {
		return ProviderProfile{}, ErrProfileNotFound
	}
	var name string
	if p.DisplayName != nil {
		n, err := validDisplayName(*p.DisplayName)
		if err != nil {
			return ProviderProfile{}, err
		}
		name = n
	}
	if p.State != nil {
		switch *p.State {
		case ProfileActive, ProfileDisabled, ProfileRetired:
		default:
			return ProviderProfile{}, badRequest("state must be active, disabled or retired")
		}
	}
	var authSource string
	if p.AuthSource != nil {
		a, err := normalizeAuthSource(*p.AuthSource)
		if err != nil {
			return ProviderProfile{}, err
		}
		authSource = a
	}
	var out ProviderProfile
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		rec, err := findProfileRec(ctx, sc, ref)
		if err != nil {
			return err
		}
		if rec.String(colPPState) == ProfileRetired {
			// A retired id accepts nothing, not even a rename: the history it
			// explains must keep reading as it was.
			return ErrProfileRetired
		}
		if p.DisplayName != nil {
			rec[colPPDisplayName] = name
		}
		if p.AuthSource != nil {
			rec[colPPAuthSource] = authSource
		}
		if p.State != nil {
			switch *p.State {
			case ProfileRetired:
				rec[colPPState] = ProfileRetired
				rec[colPPHomeSlot] = retiredHomeSlot(ref)
				rec[colPPRetiredAt] = model.NewTimestamp(m.now()).String()
			default:
				rec[colPPState] = *p.State
			}
		}
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		out = profileFromRecord(updated)
		return nil
	})
	if err != nil {
		return ProviderProfile{}, err
	}
	return out, nil
}

// resolveLaunchProfile turns a profile ref named by a launch into the snapshot
// the run persists and the child is started with. It is the SERVER's resolution:
// the request only names the profile. It refuses, deny-closed and BEFORE any
// gate, claim or credential: an unknown ref, a disabled or retired profile, a
// profile owned by another environment, a driver this runner cannot operate, and
// a home that no longer resolves to its registered location.
func (m *Module) resolveLaunchProfile(ctx context.Context, tenant model.TenantID, ref string) (ProviderHomeSnapshot, error) {
	env, err := m.localEnvironment()
	if err != nil {
		return ProviderHomeSnapshot{}, err
	}
	prof, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return ProviderHomeSnapshot{}, err
	}
	return m.snapshotForLaunch(prof, env)
}

// snapshotForLaunch is the profile→snapshot check shared by create and resume.
func (m *Module) snapshotForLaunch(prof ProviderProfile, env string) (ProviderHomeSnapshot, error) {
	switch prof.State {
	case ProfileActive:
	case ProfileDisabled:
		return ProviderHomeSnapshot{}, ErrProfileDisabled
	default:
		return ProviderHomeSnapshot{}, ErrProfileRetired
	}
	if prof.EnvironmentRef != env {
		return ProviderHomeSnapshot{}, ErrProfileForeignEnvironment
	}
	if !m.driverOperable(prof.Driver) {
		return ProviderHomeSnapshot{}, ErrProfileDriverNotOperable
	}
	if err := revalidateHome("config_home", prof.ConfigHome); err != nil {
		return ProviderHomeSnapshot{}, err
	}
	if err := revalidateHome("user_home", prof.UserHome); err != nil {
		return ProviderHomeSnapshot{}, err
	}
	if err := requireAuthSourceForDriver(prof.Driver, prof.AuthSource); err != nil {
		// Deny-closed and BEFORE anything durable: a driver that needs an authorized
		// authentication source does not get one by default, and the refusal names
		// what is missing instead of failing later inside a handshake.
		return ProviderHomeSnapshot{}, err
	}
	return ProviderHomeSnapshot{
		ProfileID: prof.Ref, Driver: prof.Driver, EnvironmentRef: prof.EnvironmentRef,
		ConfigHome: prof.ConfigHome, UserHome: prof.UserHome, AuthSource: prof.AuthSource,
	}, nil
}

// driverOperable reports whether this runtime has a driver PATH for the given
// key: the historical Claude runner, or a provider driver REGISTERED with this
// runtime. A profile whose driver nobody registered stays honestly observable and
// honestly not launchable — readiness is per driver, and nothing here invents a
// runner for a provider that has none.
//
// ⛔ IT IS A REGISTRATION ANSWER, NOT A LAUNCH VERDICT, and this comment used to
// read as the second ("whether this runtime can LAUNCH"). The Claude arm returns
// true WITHOUT consulting the Runner, the executable, the homes or any credential
// wiring, so on a node with no Runner and no credential source it still answers
// true — correctly, for what it measures. What it measures is the D of a
// conjunction the launch completes afterwards (snapshotForLaunch re-validates the
// homes, mintLaunchAuthority the credential, the Runner the process), and what
// reports the rest of that conjunction to an operator is the launch-readiness
// read, never this predicate widened.
func (m *Module) driverOperable(driver string) bool {
	if driver == providerDriverClaude {
		return true
	}
	_, ok := m.driverFor(driver)
	return ok
}

// providerDriverClaude is the driver key of the operated Claude runner. It is the
// same string SessionBinding uses for Claude's provider ids.
const providerDriverClaude = "claude"

// revalidateStoredProfile re-resolves the profile a run was persisted with, for a
// resume: same id, same environment, same homes, state still launchable. The
// comparison is over the immutable identity and the current state — a rename in
// between is fine; a moved home, a retire or a foreign environment is not.
func (m *Module) revalidateStoredProfile(ctx context.Context, tenant model.TenantID, rec model.Record) (ProviderHomeSnapshot, error) {
	stored := ProviderHomeSnapshot{
		ProfileID: rec.String(colRunProfileID), Driver: rec.String(colRunProfileDriver),
		EnvironmentRef: rec.String(colRunProfileEnvRef),
		ConfigHome:     rec.String(colRunProfileConfigHome), UserHome: rec.String(colRunProfileUserHome),
		AuthSource: rec.String(colRunProviderAuthSource),
	}
	if stored.ProfileID == "" {
		if m.rt.profiledLaunchesEnabled {
			return ProviderHomeSnapshot{}, conflictErr("legacy session has no proven provider home and cannot be continued")
		}
		return ProviderHomeSnapshot{}, nil // a legacy run: no profile was ever persisted
	}
	env, err := m.localEnvironment()
	if err != nil {
		return ProviderHomeSnapshot{}, err
	}
	prof, err := m.GetProfile(ctx, tenant, stored.ProfileID)
	if err != nil {
		return ProviderHomeSnapshot{}, err
	}
	if stored.AuthSource != "" && prof.AuthSource != stored.AuthSource {
		// The authentication source this run was launched under is an AUTHORIZATION,
		// and changing it is a new launch decision, not a continuation. Refusing is
		// what "resume retains the authorized selection" means when the selection has
		// moved underneath: the alternative is to silently continue a session under a
		// credential path nobody approved for it.
		//
		// A run whose stored source is EMPTY predates the authorization entirely, so
		// there is nothing to retain; it adopts the profile's current decision, the
		// same way the resume already re-resolves the template and re-runs the gates.
		return ProviderHomeSnapshot{}, &runErr{
			http.StatusConflict,
			"the profile's authorized authentication source changed since this session was launched; launch a new session under the current authorization",
		}
	}
	if prof.EnvironmentRef != stored.EnvironmentRef || prof.ConfigHome != stored.ConfigHome ||
		prof.UserHome != stored.UserHome || prof.Driver != stored.Driver {
		// The row says the profile is not what this run was launched under. A
		// profile row cannot change these, so this is corruption or a foreign
		// write; either way it is not proof of the previous home.
		return ProviderHomeSnapshot{}, &runErr{http.StatusConflict, "the run's persisted home does not match its profile; refusing to continue on an unproven home"}
	}
	return m.snapshotForLaunch(prof, env)
}
