// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

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
// concurrent resolves leave one profile.

// Why a profile was resolved.
const (
	ResolveOwnLogin = "own_login"
	ResolveAPIKey   = "api_key"
)

// ToolLoginStatus reports, for this node, whether a driver's tool is installed and
// signed in with its own login. It is the engine's sign-in status (the agent tools
// module), late-bound by the composition.
type ToolLoginStatus func(ctx context.Context, tenant model.TenantID, driver string) (installed, signedIn bool, err error)

// UseToolLoginStatus wires the tool sign-in status the resolve rule reads.
func (m *Module) UseToolLoginStatus(f ToolLoginStatus) {
	if f != nil {
		m.toolLogin = f
	}
}

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
}

var resolveTools = map[string]resolveTool{
	providerDriverClaude:   {name: "Claude Code", ownKind: ProviderKindAnthropic, keyPhrase: "an Anthropic key", signsIn: true},
	providerDriverCodex:    {name: "Codex", ownKind: ProviderKindOpenAI, keyPhrase: "an OpenAI key", signsIn: true},
	"grok":                 {name: "Grok Build", ownKind: ProviderKindXAI, keyPhrase: "an xAI key", signsIn: true},
	providerDriverOpenCode: {name: "OpenCode"},
}

// The stable codes of the rule's two refusals, beside their sentences: a page or the
// CLI (-o json) tells "nothing to run on yet" from any other conflict without
// reading the words, which stay the tool's own.
const (
	resolveCodeToolNotInstalled = "tool_not_installed"
	resolveCodeNothingToRunOn   = "nothing_to_run_on"
)

// codedRunErr is a runErr with the stable code its route declares for it. It
// unwraps to the runErr, so its status is read the same way everywhere.
type codedRunErr struct {
	*runErr
	code string
}

func (e *codedRunErr) Unwrap() error { return e.runErr }

func (tool resolveTool) nothingToRunOn() error {
	if !tool.signsIn {
		return &codedRunErr{conflictErr(tool.name + " has nothing to run on yet. Add a key or a local model (Ollama) in Providers."),
			resolveCodeNothingToRunOn}
	}
	return &codedRunErr{conflictErr(tool.name + " is not signed in and Providers has nothing it can use. " +
		"Sign it in under AI tools, or add " + tool.keyPhrase + " in Providers."), resolveCodeNothingToRunOn}
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
		if rec.State == ProviderRecordActive && recordServesDriver(rec.Kind, driver) {
			usable = append(usable, rec)
		}
	}
	sort.SliceStable(usable, func(a, b int) bool { return rank(usable[a].Kind) < rank(usable[b].Kind) })
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

// chooseProfileSource applies steps 1-4 of the rule. It reads the tool's sign-in
// status and the active records and writes nothing, so a page can show the answer
// (PreviewProfile) without making a profile by being viewed.
func (m *Module) chooseProfileSource(ctx context.Context, tenant model.TenantID, driverIn string) (resolveChoice, error) {
	driver := strings.ToLower(strings.TrimSpace(driverIn))
	tool, ok := resolveTools[driver]
	if !ok {
		return resolveChoice{}, badRequest("driver must be claude, codex, grok or opencode")
	}
	if m.toolLogin == nil {
		return resolveChoice{}, &runErr{http.StatusServiceUnavailable,
			"this node cannot tell whether " + tool.name + " is signed in, so it cannot choose a profile for it"}
	}
	installed, signedIn, err := m.toolLogin(ctx, tenant, driver)
	if err != nil {
		return resolveChoice{}, &runErr{http.StatusServiceUnavailable, "the sign-in status of " + tool.name + " could not be read on this node"}
	}
	if !installed {
		return resolveChoice{}, &codedRunErr{conflictErr("Install " + tool.name + " first, under AI tools."), resolveCodeToolNotInstalled}
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
	if m.data == nil {
		return ResolvedProfile{}, errNoData
	}
	c, err := m.chooseProfileSource(ctx, tenant, driver)
	if err != nil {
		return ResolvedProfile{}, err
	}
	return c.out, nil
}

// ResolveProfile answers which profile a new session of this driver uses on this
// node, reusing or creating it (see the rule above).
func (m *Module) ResolveProfile(ctx context.Context, tenant model.TenantID, driverIn string) (ResolvedProfile, error) {
	if m.data == nil {
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
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, _, err := accountHomeAdmission(ctx, sc, tenant) // the per-tenant lock
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colPPDriver, driver), eq(colPPState, ProfileActive), eq(colPPEnvRef, env), eq(colPPAuthSource, source)},
			Sort:    []model.Sort{{Column: model.ColCreatedAt}},
			Limit:   200,
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			// An own-login profile that still carries a key is the incoherent shape
			// the create/patch refusal now prevents; it is never the answer.
			if row.String(colPPProviderRecordRef) == recordRef &&
				(loginHome == "" || row.String(colPPConfigHome) == loginHome) {
				out.Profile = profileFromRecord(row)
				return nil
			}
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
