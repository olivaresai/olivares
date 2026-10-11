// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"sync"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ONE RULE FOR A NEW SESSION'S PROFILE (Root, 2026-10-01; HU 030, FH 026/028).
//
// "Which profile does a new session use" was web code in the console (first-hour
// api.ts) and a different rule in the CLI, so the CLI said "Not logged in" where the
// console worked. The rule lives here now, and both clients ask for it:
//
//  1. The tool is not installed on this node: refused, with the step.
//  2. It is signed in with its own login: the profile that uses that login.
//  3. Otherwise a record in Providers it can use, in one fixed order: the driver's
//     own vendor kind, then a local model if the driver can use one, then any other
//     compatible key; the oldest first within a kind. The profile is a managed
//     credential bound to that record.
//  4. Neither: refused, with one sentence that names the tool and what to add.
//
// A matching active profile is reused (the oldest), otherwise one is created with
// the server's own homes and no tool list: the engine's default surface applies
// (effectiveTools), so no profile stores a policy nobody chose. The lookup and the
// create run in ONE transaction under the per-tenant account-home lock, so two
// concurrent resolves leave one profile. Finding a profile that exists reads
// first and takes no lock.

// Why a profile was resolved.
const (
	ResolveOwnLogin = "own_login"
	ResolveAPIKey   = "api_key"
	// ResolveAccount: the caller named the account, so the rule did not choose.
	ResolveAccount = "account"
)

// ToolLoginStatus reports, for this node, whether a driver's tool is installed and
// signed in with its own login. It is the engine's sign-in status (the agent tools
// module), late-bound by the composition.
type ToolLoginStatus func(ctx context.Context, tenant model.TenantID, driver string) (installed, signedIn bool, err error)

// ProfileLoginStatus asks the native tool about the selected profile's own home.
// An empty profile reference has the same meaning as ToolLoginStatus.
type ProfileLoginStatus func(ctx context.Context, tenant model.TenantID, driver, profileRef string) (installed, signedIn bool, err error)

// ResolvedProfile is the profile a new session uses, and why.
type ResolvedProfile struct {
	Profile  ProviderProfile
	Reason   string
	Provider *ProviderRecord // set when Reason is ResolveAPIKey
	Created  bool
}

// resolveTool is what the rule says about one driver, in the person's words.
type resolveTool struct {
	name      string // the tool's own name
	ownKind   string // the vendor's provider kind ("" when the tool has none)
	keyPhrase string // what to add in Providers
	signsIn   bool   // whether the tool has its own login the engine relays
	bindable  bool   // whether a provider from Providers can be bound to the tool
}

func resolveToolFor(driver string) (resolveTool, bool) {
	facts, ok := driverfacts.Lookup(driver)
	return resolveTool{name: facts.Name, ownKind: facts.OwnKind, keyPhrase: facts.KeyPhrase, signsIn: facts.SignIn != "", bindable: len(facts.Bindings) > 0}, ok && facts.Session
}

// Stable readiness codes distinguish an unusable tool from an operational
// refusal, without reading the tool's sentence.
const (
	resolveCodeToolNotInstalled = "tool_not_installed"
	resolveCodeNothingToRunOn   = "nothing_to_run_on"
	resolveCodeSignInUnreadable = "tool_signin_unreadable"
)

// codedRunErr is a runErr with the stable code its route declares for it. It
// unwraps to the runErr, so its status is read the same way everywhere.
type codedRunErr struct {
	*runErr
	code string
}

func (e *codedRunErr) Unwrap() error { return e.runErr }

func (tool resolveTool) nothingToRunOn() error {
	if !tool.bindable {
		// No key or model from Providers can reach this tool: it runs on the key or
		// login its own profile holds, and the profile is named.
		return &codedRunErr{conflictErr(tool.name + " has nothing to run on from here: no provider from Providers can be bound to it. " +
			"Create a profile for it and start the session with --profile."), resolveCodeNothingToRunOn}
	}
	if !tool.signsIn {
		return &codedRunErr{conflictErr(tool.name + " has nothing to run on yet. Add a key or a local model (Ollama) in Providers."),
			resolveCodeNothingToRunOn}
	}
	return &codedRunErr{conflictErr(tool.name + " is not signed in and Providers has nothing it can use. " +
		"Sign it in under AI tools, or add " + tool.keyPhrase + " in Providers."), resolveCodeNothingToRunOn}
}

// resolveSkips is a record the rule never picks by itself for a tool, though a profile
// may still name it: a local Ollama for Codex. Codex 0.160 sends additional_tools, which
// the Ollama the product installs (0.35) rejects, so every turn failed while the
// console said "Codex is ready". OpenCode is the local default; Codex is
// refused with its sign-in or OpenAI-key sentence.
func resolveSkips(driver, kind string) bool {
	return driver == providerDriverCodex && kind == ProviderKindOllama
}

// pickRecord applies the fixed order to the active records, oldest first.
func pickRecord(driver string, tool resolveTool, records []ProviderRecord) *ProviderRecord {
	rank := func(kind string) int {
		switch {
		case tool.ownKind != "" && kind == tool.ownKind:
			return 0
		case kind == ProviderKindOllama:
			return 1
		default:
			return 2
		}
	}
	var usable []ProviderRecord
	for _, rec := range records {
		if rec.State == ProviderRecordActive && recordServesDriver(rec.Kind, rec.BaseURL, driver) && !resolveSkips(driver, rec.Kind) {
			usable = append(usable, rec)
		}
	}
	// A key whose last provider test was refused comes after every other option
	// (Root on FH 099): it is the answer only when nothing else can run the tool,
	// and the launch then says the key was refused.
	refused := func(rec ProviderRecord) bool { return rec.ProbeState == ProbeRefused }
	sort.SliceStable(usable, func(a, b int) bool {
		if refused(usable[a]) != refused(usable[b]) {
			return !refused(usable[a])
		}
		return rank(usable[a].Kind) < rank(usable[b].Kind)
	})
	if len(usable) == 0 {
		return nil
	}
	return &usable[0]
}

// resolveChoice is the rule's answer before any profile is read or made: the
// tool, why, and the credential source with its record.
type resolveChoice struct {
	driver    string
	tool      resolveTool
	source    string
	recordRef string
	out       ResolvedProfile
}

// resolveDriver is the opening every resolve shares: the driver as typed, made a
// key, and the tool it names. The tools a session can run are the driver facts'.
func resolveDriver(driverIn string) (string, resolveTool, error) {
	driver := strings.ToLower(strings.TrimSpace(driverIn))
	tool, ok := resolveToolFor(driver)
	if !ok {
		return "", resolveTool{}, badRequest("driver must be " + driverfacts.JoinList(driverfacts.SessionKeys(), "or"))
	}
	return driver, tool, nil
}

// chooseProfileSource applies steps 1-4 of the rule. It reads the tool's sign-in
// status and the active records and writes nothing, so a page can show the answer
// (PreviewProfile) without making a profile by being viewed.
func (m *Module) chooseProfileSource(ctx context.Context, tenant model.TenantID, driverIn string) (resolveChoice, error) {
	driver, tool, err := resolveDriver(driverIn)
	if err != nil {
		return resolveChoice{}, err
	}
	if m.ToolLogin == nil {
		return resolveChoice{}, &codedRunErr{&runErr{http.StatusServiceUnavailable,
			"this node cannot tell whether " + tool.name + " is signed in, so it cannot choose a profile for it"}, resolveCodeSignInUnreadable}
	}
	installed, signedIn, err := m.ToolLogin(ctx, tenant, driver)
	if err != nil {
		return resolveChoice{}, &codedRunErr{&runErr{http.StatusServiceUnavailable,
			"the sign-in status of " + tool.name + " could not be read on this node"}, resolveCodeSignInUnreadable}
	}
	if !installed {
		return resolveChoice{}, &codedRunErr{conflictErr("Install " + tool.name + " first, under AI tools."), resolveCodeToolNotInstalled}
	}
	if driver == providerDriverGrok && grokSandboxUnavailable(PresetAsk) {
		// A new session starts on the ask preset, which runs Grok's sandbox.
		return resolveChoice{}, &codedRunErr{conflictErr(grokSandboxMissing + "."), resolveCodeToolNotInstalled}
	}
	c := resolveChoice{driver: driver, tool: tool, source: AuthSourceAccountHome, out: ResolvedProfile{Reason: ResolveOwnLogin}}
	if signedIn && tool.signsIn {
		return c, nil
	}
	records, _, err := m.ListProviderRecords(ctx, tenant, ProviderRecordActive, "",
		model.Query{Limit: 500, Sort: []model.Sort{{Column: model.ColCreatedAt}}})
	if err != nil {
		return resolveChoice{}, err
	}
	rec := pickRecord(driver, tool, records)
	if rec == nil {
		if driver == providerDriverOpenCode && len(records) > 0 {
			facts, _ := driverfacts.Lookup(driver)
			return resolveChoice{}, &codedRunErr{conflictErr(facts.BindingDescription + "."), resolveCodeNothingToRunOn}
		}
		return resolveChoice{}, tool.nothingToRunOn()
	}
	c.source, c.recordRef = AuthSourceManagedInjection, rec.Ref
	c.out.Reason, c.out.Provider = ResolveAPIKey, rec
	return c, nil
}

// PreviewProfile is the rule's answer without its write: why, and for a key which
// record. Profile is empty and Created false; a refusal is the one ResolveProfile
// would give. It is what a page shows before the person starts a session.
func (m *Module) PreviewProfile(ctx context.Context, tenant model.TenantID, driver string) (ResolvedProfile, error) {
	if m.Data == nil {
		return ResolvedProfile{}, errNoData
	}
	c, err := m.chooseProfileSource(ctx, tenant, driver)
	if err != nil {
		return ResolvedProfile{}, err
	}
	return c.out, nil
}

// resolveCodeKeyRefused: the rule's only answer is a key its provider refused at the
// last connection test (pickRecord puts such a key after every other option).
const resolveCodeKeyRefused = "key_refused"

// ToolReadiness is one tool's answer to "can a new session start on it now": what it
// would run on (the preview), or the code and the one sentence that say why not.
type ToolReadiness struct {
	Driver        string
	Ready         bool
	Reason        string          // ResolveOwnLogin or ResolveAPIKey; "" when nothing was found
	Provider      *ProviderRecord // the key or local model, also when it was refused
	ModelRequired bool            // the tool cannot start on that key without a model
	Code          string          // set when not ready
	Message       string          // set when not ready
}

// ToolsReadiness answers, for every tool a session can run, whether a new session can
// start on it now and, when not, why in one sentence (HU 043, HU2-17): the preview's
// rule plus the verdict of the key it picked. The console and the CLI render this one
// answer; a refusal is part of the answer, not an error. The tools are asked at once:
// each asks its own program for its sign-in.
func (m *Module) ToolsReadiness(ctx context.Context, tenant model.TenantID) ([]ToolReadiness, error) {
	var drivers []string
	for _, f := range driverfacts.All() {
		if f.Session {
			drivers = append(drivers, f.Key)
		}
	}
	out := make([]ToolReadiness, len(drivers))
	errs := make([]error, len(drivers))
	var wg sync.WaitGroup
	for i, driver := range drivers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A panic here is outside the request's own recovery: it fails this read, and
			// the log says which tool and where.
			defer func() {
				if p := recover(); p != nil {
					if m.log != nil {
						m.log.Error("sessions: tool readiness panicked", "driver", driver, "panic", p, "stack", string(debug.Stack()))
					}
					errs[i] = errors.New("the readiness of " + driver + " could not be read")
				}
			}()
			out[i], errs[i] = m.toolReadiness(ctx, tenant, driver)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *Module) toolReadiness(ctx context.Context, tenant model.TenantID, driver string) (ToolReadiness, error) {
	out := ToolReadiness{Driver: driver}
	got, err := m.PreviewProfile(ctx, tenant, driver)
	var coded *codedRunErr
	if errors.As(err, &coded) {
		out.Code, out.Message = coded.code, coded.msg
		return out, nil
	}
	if err != nil {
		return out, err
	}
	terms, _ := m.LaunchTermsFor(driver)
	out.Reason, out.Provider = got.Reason, got.Provider
	out.ModelRequired = got.Provider != nil && terms.BoundModelRequired
	if got.Provider != nil && got.Provider.ProbeState == ProbeRefused {
		tool, _ := resolveToolFor(driver)
		label := got.Provider.DisplayName
		if label == "" {
			label = got.Provider.Kind
		}
		out.Code = resolveCodeKeyRefused
		out.Message = tool.name + " would run on the API key " + label +
			", which its provider refused at the last test. Replace it in Providers, or add another key."
		return out, nil
	}
	out.Ready = true
	return out, nil
}

// DefaultToolLoginHome keeps OpenCode's native XDG login with the profile that
// automatic resolution reuses. Earlier profiles may have a separate immutable
// user home; signing in does not move that identity or copy its credentials.
func (m *Module) DefaultToolLoginHome(ctx context.Context, tenant model.TenantID, driver string) (home, configDir string, err error) {
	home, configDir, ok := ToolLoginHome(m.toolLoginsRoot, tenant, driver)
	if !ok {
		return "", "", badRequest("this tool has no login home on this node")
	}
	if driver != providerDriverOpenCode {
		return home, configDir, nil
	}
	if m.Data == nil {
		return "", "", errNoData
	}
	env, err := m.localEnvironment()
	if err != nil {
		return "", "", err
	}
	configDir, err = m.ownLoginConfigHome(tenant, driver)
	if err != nil {
		return "", "", err
	}
	if configDir, err = canonicalHome("config_home", configDir); err != nil {
		return "", "", err
	}
	err = m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		row, err := findResolvedProfile(ctx, repo, resolveChoice{driver: driver, source: AuthSourceAccountHome}, env, configDir)
		if err != nil || row == nil {
			return err
		}
		snap, err := m.snapshotForLaunch(tenant, profileFromRecord(row), env)
		if err != nil {
			return err
		}
		home, configDir = snap.UserHome, snap.ConfigHome
		return nil
	})
	return home, configDir, err
}

// Both default native login and automatic resolution select the same oldest
// active profile on this environment and authentication source.
func findResolvedProfile(ctx context.Context, repo store.GenericRepo, c resolveChoice, env, loginHome string) (model.Record, error) {
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colPPDriver, c.driver), eq(colPPState, ProfileActive), eq(colPPEnvRef, env), eq(colPPAuthSource, c.source)},
		Sort:    []model.Sort{{Column: model.ColCreatedAt}},
		Limit:   200,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		// An own-login profile carrying a key is never the answer.
		if row.String(colPPProviderRecordRef) == c.recordRef &&
			(loginHome == "" || row.String(colPPConfigHome) == loginHome) {
			return row, nil
		}
	}
	return nil, nil
}

// ResolveProfile answers which profile a new session of this driver uses on this
// node, reusing or creating it (see the rule above).
func (m *Module) ResolveProfile(ctx context.Context, tenant model.TenantID, driverIn string) (ResolvedProfile, error) {
	if m.Data == nil {
		return ResolvedProfile{}, errNoData
	}
	c, err := m.chooseProfileSource(ctx, tenant, driverIn)
	if err != nil {
		return ResolvedProfile{}, err
	}
	driver, tool, source, recordRef, out := c.driver, c.tool, c.source, c.recordRef, c.out
	env, err := m.localEnvironment()
	if err != nil {
		return ResolvedProfile{}, err
	}
	// The own login is the product's (ToolLoginHome): an own-login profile with any
	// other configuration home is never the answer (FH 036 — profiles made before
	// that pointed at the engine user's own ~/.claude).
	loginHome := ""
	if source == AuthSourceAccountHome {
		raw, err := m.ownLoginConfigHome(tenant, driver)
		if err != nil {
			return ResolvedProfile{}, err
		}
		if loginHome, err = canonicalHome("config_home", raw); err != nil {
			return ResolvedProfile{}, err
		}
	}
	// The profile that already exists is the usual answer: find it in a read
	// transaction, and take the write lock below only to create one.
	var found model.Record
	err = m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		found, err = findResolvedProfile(ctx, repo, c, env, loginHome)
		return err
	})
	if err != nil {
		return ResolvedProfile{}, err
	}
	if found != nil {
		out.Profile = profileFromRecord(found)
		return out, nil
	}
	err = m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, _, err := accountHomeAdmission(ctx, sc, tenant) // the per-tenant lock
		if err != nil {
			return err
		}
		// Looked up again under the lock: a concurrent resolve may have made it.
		existing, err := findResolvedProfile(ctx, repo, c, env, loginHome)
		if err != nil {
			return err
		}
		if existing != nil {
			out.Profile = profileFromRecord(existing)
			return nil
		}
		name := tool.name
		if out.Provider != nil {
			label := out.Provider.DisplayName
			if label == "" {
				label = out.Provider.Kind
			}
			name = tool.name + " (" + label + ")"
		}
		row, err := m.serverHomedProfileRow(tenant, env, driver, source, recordRef, name)
		if err != nil {
			return err
		}
		if err := validateRecordBinding(ctx, sc, driver, recordRef); err != nil {
			return err
		}
		created, err := repo.Create(ctx, row)
		if err != nil {
			return err
		}
		out.Profile, out.Created = profileFromRecord(created), true
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		// Another active profile already holds this tool's login folder (for
		// example one registered by hand): the rule does not take it over.
		return ResolvedProfile{}, ErrProfileHomeTaken
	}
	if err != nil {
		return ResolvedProfile{}, err
	}
	return out, nil
}

// ResolveAccountProfile answers a request that names its account (a name, or a
// profile reference): that account's profile, and nothing else. The rule above
// does not run: the caller chose, so there is no fallback to another profile,
// nothing is created, and the tool's sign-in is not read. The account must be of
// the driver asked for, on this node, and active; the launch checks the rest.
func (m *Module) ResolveAccountProfile(ctx context.Context, tenant model.TenantID, driverIn, account string) (ResolvedProfile, error) {
	if m.Data == nil {
		return ResolvedProfile{}, errNoData
	}
	driver, tool, err := resolveDriver(driverIn)
	if err != nil {
		return ResolvedProfile{}, err
	}
	env, err := m.localEnvironment()
	if err != nil {
		return ResolvedProfile{}, err
	}
	var rec model.Record
	err = m.Data.View(ctx, tenant, func(sc store.Scope) error {
		if validProfileRef(account) {
			found, err := findProfileRec(ctx, sc, account)
			rec = found
			if errors.Is(err, ErrProfileNotFound) {
				return ErrAccountNotFound
			}
			return err
		}
		repo, err := sc.Ext(providerProfileKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colPPEnvRef, env), eq(colPPAccountName, account)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return ErrAccountNotFound
		}
		rec = recs[0]
		return nil
	})
	if err != nil {
		return ResolvedProfile{}, err
	}
	profile := profileFromRecord(rec)
	switch {
	case profile.Driver != driver:
		return ResolvedProfile{}, conflictErr("this account is a " + profile.Driver + " account, not a " + tool.name + " one")
	case profile.EnvironmentRef != env:
		return ResolvedProfile{}, ErrProfileForeignEnvironment
	case profile.State == ProfileRetired:
		return ResolvedProfile{}, ErrProfileRetired
	case profile.State == ProfileDisabled:
		return ResolvedProfile{}, ErrProfileDisabled
	}
	return ResolvedProfile{Profile: profile, Reason: ResolveAccount}, nil
}

// serverHomedProfileRow is a new profile row whose homes the server makes: the
// tool's own login folder (account home) or the profile's own folders (managed).
func (m *Module) serverHomedProfileRow(tenant model.TenantID, env, driver, source, recordRef, displayName string) (model.Record, error) {
	ref := newProfileRef()
	var configHome, userHome string
	var err error
	if source == AuthSourceAccountHome {
		configHome, userHome, err = m.standardAccountHomes(tenant, driver, ref)
	} else {
		configHome, userHome, err = m.managedProfileHomes(driver, ref)
	}
	if err != nil {
		return nil, err
	}
	if configHome, err = canonicalHome("config_home", configHome); err != nil {
		return nil, err
	}
	if userHome, err = canonicalHome("user_home", userHome); err != nil {
		return nil, err
	}
	if err := m.rejectManagedRegistration(configHome, userHome); err != nil {
		return nil, err
	}
	name, err := validDisplayName(displayName)
	if err != nil {
		return nil, err
	}
	return model.Record{
		colPPRef:               ref,
		colPPDriver:            driver,
		colPPEnvRef:            env,
		colPPConfigHome:        configHome,
		colPPUserHome:          userHome,
		colPPDisplayName:       name,
		colPPState:             ProfileActive,
		colPPHomeSlot:          activeHomeSlot(env, driver, configHome),
		colPPAuthSource:        source,
		colPPProviderRecordRef: recordRef,
	}, nil
}
