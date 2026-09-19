// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Provider records.
//
// A provider record is the CREDENTIAL half of using an official CLI, and until now
// it did not exist. The plane had two halves of a whole and nothing between them:
//
//   - a provider PROFILE (provider_profile.go) says which home a child runs under.
//     It is storage identity and it is deliberately not an account.
//   - the HOST ENVIRONMENT (cmd/olivares/sessionruntime.go) says which credential
//     every launch on this node receives. It is deployment-wide and it cannot say
//     "this profile uses that account".
//
// The record is the missing middle: a NAMED credential an operator registers once,
// tests, rotates and revokes, and then BINDS to the profiles that should use it.
//
// WHAT A RECORD HOLDS AND WHAT IT NEVER HOLDS. The row carries the non-secret facts
// — kind, name, endpoint, a four-character hint, the last probe's outcome — plus a
// `secret_ref`, which is a LOCATOR and not a value. The value itself is sealed by a
// port the composition root implements over the engine's own secret store
// (provider_secret.go): a module holds no store handle for the auth partition, so a
// module that could seal its own secrets would be a module holding a key. It does
// not, and this file cannot make it.
//
// KIND IS NOT DRIVER, and conflating them is the mistake this file refuses to make.
// `anthropic` is what the credential IS; `claude` is which CLI reads it. One
// anthropic record serves a claude profile AND an opencode profile. The
// compatibility table is explicit (recordServesDriver) rather than derived from a
// name, because a derivation would silently accept every future driver.

// Registered kind and physical table.
const (
	providerRecordKind  model.Kind = "sessions.provider_record"
	providerRecordTable            = "sessions_provider_record"
)

// sessions.provider_record columns.
const (
	colPRRef         = "provider_ref"
	colPRKind        = "kind"
	colPRDisplayName = "display_name"
	colPRBaseURL     = "base_url"
	// colPRSecretRef is the NON-SECRET locator the vault handed back. It is not a
	// value and it is not published: a reader given the locator would hold the name
	// to ask the vault with, which is the whole point of not publishing it.
	colPRSecretRef = "secret_ref"
	// colPRKeyHint is the last four characters of the registered value, and exactly
	// those: enough for an operator to tell two keys apart, never enough to use one.
	colPRKeyHint    = "key_hint"
	colPRState      = "state"
	colPRNameSlot   = "name_slot"
	colPRModels     = "models"
	colPRProbeState = "probe_state"
	// colPRProbeDetail is ONE bounded sentence about the last probe. It never
	// carries the value, a header, or a URL with a credential in it.
	colPRProbeDetail  = "probe_detail"
	colPRProbeLatency = "probe_latency_ms"
	colPRProbedAt     = "probed_at"
	colPRRevokedAt    = "revoked_at"
)

// Record lifecycle states. There are two, and revoke is final for that id: a
// credential that was withdrawn must keep reading as withdrawn in the history that
// explains a launch it once authorized.
const (
	// ProviderRecordActive may be bound and may authorize a launch.
	ProviderRecordActive = "active"
	// ProviderRecordRevoked refuses every future launch. Existing bindings are NOT
	// rewritten: a profile that names a revoked record is refused BY NAME at launch,
	// which is an answer an operator can act on, unlike a binding that vanished.
	ProviderRecordRevoked = "revoked"
)

// The four provider kinds. The set is CLOSED, unlike the driver key set, and the
// difference is deliberate: a driver key only has to be a valid scope component,
// while a kind decides which environment variables the engine will inject into a
// child process. An open kind set would be an open injection set.
const (
	ProviderKindAnthropic        = "anthropic"
	ProviderKindOpenAI           = "openai"
	ProviderKindXAI              = "xai"
	ProviderKindOpenAICompatible = "openai_compatible"
)

// providerRecordRefPrefix marks a provider record id at a glance, the way
// profileRefPrefix marks a profile and sidPrefix marks a canonical session id.
const providerRecordRefPrefix = "prv_"

// Probe outcomes. They are three and not two on purpose: "the provider refused my
// credential" and "I could not reach the provider" have different remedies, and an
// operator handed the first for the second goes looking for a key that is fine.
const (
	// ProbeNever is the state of a record nobody has tested. It is never green.
	ProbeNever = ""
	// ProbeOK means the provider answered and listed its models.
	ProbeOK = "ok"
	// ProbeRefused means the provider answered and rejected the credential.
	ProbeRefused = "refused"
	// ProbeUnreachable means no answer was obtained. It is NOT a verdict on the key.
	ProbeUnreachable = "unreachable"
)

// Bounds on the free-text fields.
const (
	maxProviderName     = 200
	maxProviderBaseURL  = 2048
	maxProviderKey      = 8192
	maxProbeDetail      = 500
	maxProbedModelCount = 200
	// minProviderKey refuses a value too short to be any provider's credential.
	// It is not a format check — a format check on somebody else's credential is a
	// guess that ages — it is a refusal of the empty and near-empty value.
	minProviderKey = 8
)

// nameSlotVersion and the retired-slot prefix reuse the encoding the home slot
// already proves: a unit separator no validated field can contain.
const providerNameSlotVersion = "v1"

// Provider-record errors. Each carries the status the API answers with; none
// carries a value, a locator or an endpoint credential.
var (
	// ErrProviderRecordNotFound is the typed not-found.
	ErrProviderRecordNotFound = &runErr{http.StatusNotFound, "provider not found"}
	// ErrProviderNameTaken refuses a second active record with one name per kind.
	ErrProviderNameTaken = &runErr{http.StatusConflict, "another active provider of this kind already uses this name"}
	// ErrProviderRecordRevoked refuses any use of a revoked record.
	ErrProviderRecordRevoked = &runErr{http.StatusConflict, "this provider is revoked; register another one and bind it"}
	// ErrNoProviderVault is the deny-closed answer when no sealing port is wired:
	// the engine cannot store a credential, and it says so instead of storing one
	// in the clear.
	ErrNoProviderVault = &runErr{
		http.StatusServiceUnavailable,
		"provider credentials cannot be stored on this deployment: no sealed credential vault is wired (the engine never stores a credential in the clear)",
	}
	// ErrNoProviderProbe is the deny-closed answer for the connection test. It says
	// what is unaffected, because an operator who reads "unavailable" about a test
	// must not conclude that launching is unavailable too.
	ErrNoProviderProbe = &runErr{
		http.StatusServiceUnavailable,
		"the provider connection test is not available on this deployment: no probe is wired (launching is unaffected)",
	}
)

// ProviderRecord is the module's own view of one record row. It has no field for
// the credential, and adding one would be the defect.
type ProviderRecord struct {
	ID          model.ID
	Ref         string
	Kind        string
	DisplayName string
	BaseURL     string
	SecretRef   string
	KeyHint     string
	State       string
	Models      []string
	ProbeState  string
	ProbeDetail string
	ProbeMillis int64
	ProbedAt    string
	Version     int64
	CreatedAt   string
	UpdatedAt   string
	RevokedAt   string
}

// CreateProviderRecordInput is the validated create request. APIKey is the ONLY
// field that carries a credential, it lives for the duration of the call, and it is
// handed to the vault and then dropped.
type CreateProviderRecordInput struct {
	Kind        string
	DisplayName string
	BaseURL     string
	APIKey      string
	// Actor is the authenticated caller. It reaches the vault so the sealed write
	// is attributable; the engine's secret store refuses an unattributable one.
	Actor auth.Principal
}

// ProviderRecordPatch is what a record accepts after creation. Kind is absent on
// purpose: a record's kind decides what gets injected into a child, so changing it
// under an existing binding would redefine every launch that binding authorizes.
// Registering another record is the honest way to change a kind.
type ProviderRecordPatch struct {
	DisplayName *string
	BaseURL     *string
	// APIKey rotates the value in place. The locator does not change, so every
	// binding keeps working and the next launch uses the new value.
	APIKey *string
	// Actor is the authenticated caller, for the same reason it is on the create
	// input: a rotation is a sealed write and a sealed write is attributed.
	Actor auth.Principal
}

// registerProviderRecordSchema declares the record entity. As with the profile,
// declaring the descriptor IS the migration; the two unique indexes are the
// guarantee, not the writer's care.
func (m *Module) registerProviderRecordSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  providerRecordKind,
		Table: providerRecordTable,
		Fields: []model.FieldSpec{
			{Name: colPRRef, Kind: model.KindText},
			{Name: colPRKind, Kind: model.KindText, Indexed: true},
			{Name: colPRDisplayName, Kind: model.KindText},
			{Name: colPRBaseURL, Kind: model.KindText, Nullable: true},
			{Name: colPRSecretRef, Kind: model.KindText},
			{Name: colPRKeyHint, Kind: model.KindText, Nullable: true},
			{Name: colPRState, Kind: model.KindText, Indexed: true},
			{Name: colPRNameSlot, Kind: model.KindText},
			{Name: colPRModels, Kind: model.KindJSON, Nullable: true},
			{Name: colPRProbeState, Kind: model.KindText, Nullable: true},
			{Name: colPRProbeDetail, Kind: model.KindText, Nullable: true},
			{Name: colPRProbeLatency, Kind: model.KindInt, Nullable: true},
			{Name: colPRProbedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colPRRevokedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{
			{Name: "sessions_provider_record_ref_uniq", Columns: []string{model.ColTenantID, colPRRef}, Unique: true},
			// One ACTIVE record per (kind, case-folded name). Two operators racing the
			// same name converge on one row because the losing insert fails HERE.
			{Name: "sessions_provider_record_name_uniq", Columns: []string{model.ColTenantID, colPRNameSlot}, Unique: true},
		},
	})
}

// --- validation ---------------------------------------------------------------

// normalizeProviderKind validates the credential kind against the CLOSED set.
func normalizeProviderKind(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case ProviderKindAnthropic:
		return ProviderKindAnthropic, nil
	case ProviderKindOpenAI:
		return ProviderKindOpenAI, nil
	case ProviderKindXAI:
		return ProviderKindXAI, nil
	case ProviderKindOpenAICompatible:
		return ProviderKindOpenAICompatible, nil
	case "":
		return "", badRequest("kind is required (anthropic, openai, xai or openai_compatible)")
	}
	return "", badRequest("kind must be anthropic, openai, xai or openai_compatible")
}

// validProviderName bounds the operator's own label. It is required, unlike a
// profile's display name: a record exists to BE chosen from a list, and an unnamed
// entry in a picker is the screen the provider plane exists to remove.
func validProviderName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", badRequest("name is required")
	}
	if len(s) > maxProviderName {
		return "", badRequest("name is too long")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", badRequest("name contains a control character")
		}
	}
	return s, nil
}

// validProviderBaseURL bounds and checks the optional endpoint override. It refuses
// anything that is not an absolute https URL, and it refuses a URL that carries
// userinfo: `https://user:key@host` is a credential in a field that is published in
// every read, and accepting it would leak one through a door marked "endpoint".
func validProviderBaseURL(kind, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if kind == ProviderKindOpenAICompatible {
			return "", badRequest("base_url is required for an openai_compatible provider: there is no official endpoint to assume")
		}
		return "", nil
	}
	if len(s) > maxProviderBaseURL {
		return "", badRequest("base_url is too long")
	}
	lower := strings.ToLower(s)
	if !strings.HasPrefix(lower, "https://") {
		// http:// is refused rather than warned about: the value that travels over it
		// is a credential, and a warning an operator can click past is not a control.
		return "", badRequest("base_url must be an absolute https:// URL")
	}
	rest := s[len("https://"):]
	if rest == "" || strings.ContainsAny(rest, " \t\r\n") {
		return "", badRequest("base_url is not a usable URL")
	}
	host := rest
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if strings.Contains(host, "@") {
		return "", badRequest("base_url may not carry credentials in the URL; register the credential as the provider key")
	}
	if host == "" {
		return "", badRequest("base_url is not a usable URL")
	}
	return strings.TrimRight(s, "/"), nil
}

// validProviderKey bounds the credential itself. It checks LENGTH and control
// characters and nothing else: a shape check against somebody else's credential
// format is a guess that expires the day they change it, and the failure mode of a
// wrong guess is refusing a valid key.
func validProviderKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", badRequest("api_key is required")
	}
	if len(s) < minProviderKey {
		return "", badRequest("api_key is too short to be a provider credential")
	}
	if len(s) > maxProviderKey {
		return "", badRequest("api_key is too long")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", badRequest("api_key contains a control character")
		}
	}
	return s, nil
}

// providerKeyHint is the only thing derived from a credential that this module ever
// persists or shows: the last four characters. Four is a deliberate ceiling — it
// tells two keys apart and it is useless on its own — and a value shorter than
// twelve characters gets no hint at all rather than a hint that is most of it.
func providerKeyHint(key string) string {
	if len(key) < 12 {
		return ""
	}
	return "…" + key[len(key)-4:]
}

// activeProviderNameSlot encodes the uniqueness key of a live record.
func activeProviderNameSlot(kind, name string) string {
	return providerNameSlotVersion + homeSlotSep + kind + homeSlotSep + strings.ToLower(name)
}

func revokedProviderNameSlot(ref string) string { return "revoked:" + ref }

func newProviderRecordRef() string { return providerRecordRefPrefix + string(model.NewID()) }

// validProviderRecordRef bounds the opaque id a caller presents so a lookup never
// scans on garbage.
func validProviderRecordRef(ref string) bool {
	if !strings.HasPrefix(ref, providerRecordRefPrefix) || len(ref) > 128 {
		return false
	}
	id, err := model.ParseID(strings.TrimPrefix(ref, providerRecordRefPrefix))
	return err == nil && !id.IsZero()
}

// recordServesDriver reports whether a credential of this kind can be read by this
// driver's official CLI.
//
// It is an explicit table and not a derivation, and that is the decision. A
// derivation ("the kind whose name looks like the driver") would silently accept
// every driver added later, including one whose CLI reads a variable nobody here
// has heard of. An unknown driver therefore accepts ONLY openai_compatible, which
// is the one kind that says what it injects in its own name.
func recordServesDriver(kind, driver string) bool {
	if kind == ProviderKindOpenAICompatible {
		return true
	}
	switch driver {
	case providerDriverClaude:
		return kind == ProviderKindAnthropic
	case providerDriverCodex:
		return kind == ProviderKindOpenAI
	case providerDriverGrok:
		return kind == ProviderKindXAI
	case providerDriverOpenCode:
		return kind == ProviderKindAnthropic || kind == ProviderKindOpenAI || kind == ProviderKindXAI
	}
	return false
}

// providerRecordEnv is the environment a record of this kind produces for a child.
// It is the ONLY place that decides what gets injected, and the mapping is the one
// the tree already uses in connectors/openclaw/config.go:1018-1038 — cited so the
// two cannot drift apart without somebody noticing.
func providerRecordEnv(kind, baseURL, key string) []EnvVar {
	switch kind {
	case ProviderKindAnthropic:
		out := []EnvVar{{Name: "ANTHROPIC_API_KEY", Value: key}}
		if baseURL != "" {
			out = append(out, EnvVar{Name: "ANTHROPIC_BASE_URL", Value: baseURL})
		}
		return out
	case ProviderKindOpenAI, ProviderKindOpenAICompatible:
		out := []EnvVar{{Name: "OPENAI_API_KEY", Value: key}}
		if baseURL != "" {
			out = append(out, EnvVar{Name: "OPENAI_BASE_URL", Value: baseURL})
		}
		return out
	case ProviderKindXAI:
		out := []EnvVar{{Name: "XAI_API_KEY", Value: key}}
		if baseURL != "" {
			out = append(out, EnvVar{Name: "XAI_BASE_URL", Value: baseURL})
		}
		return out
	}
	return nil
}

// providerRecordEnvNames is the CLOSED set of variable names a record of this kind
// may name. It exists so the record-backed mint can be validated against its own
// contract instead of against the third-party adapter rule, which bans exactly the
// ANTHROPIC_* names a first-party anthropic record has to set.
func providerRecordEnvNames(kind string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, item := range providerRecordEnv(kind, "https://example.invalid", "x") {
		out[item.Name] = struct{}{}
	}
	return out
}

// --- store operations ---------------------------------------------------------

func providerRecordFromRecord(rec model.Record) ProviderRecord {
	out := ProviderRecord{
		ID:          model.ID(rec.String(model.ColID)),
		Ref:         rec.String(colPRRef),
		Kind:        rec.String(colPRKind),
		DisplayName: rec.String(colPRDisplayName),
		BaseURL:     rec.String(colPRBaseURL),
		SecretRef:   rec.String(colPRSecretRef),
		KeyHint:     rec.String(colPRKeyHint),
		State:       rec.String(colPRState),
		ProbeState:  rec.String(colPRProbeState),
		ProbeDetail: rec.String(colPRProbeDetail),
		ProbeMillis: rec.Int(colPRProbeLatency),
		ProbedAt:    rec.String(colPRProbedAt),
		Version:     rec.Int(model.ColVersion),
		CreatedAt:   rec.String(model.ColCreatedAt),
		UpdatedAt:   rec.String(model.ColUpdatedAt),
		RevokedAt:   rec.String(colPRRevokedAt),
	}
	if raw := strings.TrimSpace(rec.String(colPRModels)); raw != "" {
		var models []string
		if err := json.Unmarshal([]byte(raw), &models); err == nil {
			out.Models = models
		}
	}
	return out
}

// findProviderRecordRec reads one record row by its opaque ref within a scope.
func findProviderRecordRec(ctx context.Context, sc store.Scope, ref string) (model.Record, error) {
	repo, err := sc.Ext(providerRecordKind)
	if err != nil {
		return nil, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colPRRef, ref)}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, ErrProviderRecordNotFound
	}
	return recs[0], nil
}

// CreateProviderRecord registers one provider credential. The value is sealed
// through the vault BEFORE the row is written, so a failure to seal leaves nothing
// behind; and the row that is written carries the locator, never the value.
func (m *Module) CreateProviderRecord(ctx context.Context, tenant model.TenantID, in CreateProviderRecordInput) (ProviderRecord, error) {
	if m.data == nil {
		return ProviderRecord{}, errNoData
	}
	if m.rt.providerVault == nil {
		return ProviderRecord{}, ErrNoProviderVault
	}
	kind, err := normalizeProviderKind(in.Kind)
	if err != nil {
		return ProviderRecord{}, err
	}
	name, err := validProviderName(in.DisplayName)
	if err != nil {
		return ProviderRecord{}, err
	}
	baseURL, err := validProviderBaseURL(kind, in.BaseURL)
	if err != nil {
		return ProviderRecord{}, err
	}
	key, err := validProviderKey(in.APIKey)
	if err != nil {
		return ProviderRecord{}, err
	}
	ref := newProviderRecordRef()
	secretRef, err := m.rt.providerVault.Seal(ctx, in.Actor, tenant, providerVaultName(ref), []byte(key))
	if err != nil {
		return ProviderRecord{}, sealFailure(err)
	}
	var out ProviderRecord
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(providerRecordKind)
		if rerr != nil {
			return rerr
		}
		created, rerr := repo.Create(ctx, model.Record{
			colPRRef:         ref,
			colPRKind:        kind,
			colPRDisplayName: name,
			colPRBaseURL:     baseURL,
			colPRSecretRef:   secretRef,
			colPRKeyHint:     providerKeyHint(key),
			colPRState:       ProviderRecordActive,
			colPRNameSlot:    activeProviderNameSlot(kind, name),
			colPRProbeState:  ProbeNever,
		})
		if rerr != nil {
			return rerr
		}
		out = providerRecordFromRecord(created)
		return nil
	})
	if err != nil {
		// The row did not land, so the sealed value has no owner. Removing it is
		// compensation, not cleanup: a sealed blob nobody can reach is exactly the
		// kind of residue a rotation policy later cannot see.
		if rerr := m.rt.providerVault.Revoke(ctx, in.Actor, tenant, secretRef); rerr != nil && m.log != nil {
			m.log.Warn("sessions: could not withdraw the sealed provider credential of a record that failed to persist",
				"provider_ref", ref)
		}
		if errors.Is(err, store.ErrConflict) {
			return ProviderRecord{}, ErrProviderNameTaken
		}
		return ProviderRecord{}, err
	}
	return out, nil
}

// GetProviderRecord reads one record by ref. There is no variant of this that
// returns the value: the vault is reached from the LAUNCH path and from nowhere a
// reader can address.
func (m *Module) GetProviderRecord(ctx context.Context, tenant model.TenantID, ref string) (ProviderRecord, error) {
	if m.data == nil {
		return ProviderRecord{}, errNoData
	}
	if !validProviderRecordRef(ref) {
		return ProviderRecord{}, ErrProviderRecordNotFound
	}
	var out ProviderRecord
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, err := findProviderRecordRec(ctx, sc, ref)
		if err != nil {
			return err
		}
		out = providerRecordFromRecord(rec)
		return nil
	})
	return out, err
}

// ListProviderRecords returns the tenant's records, newest first, optionally
// narrowed to one state or one kind.
func (m *Module) ListProviderRecords(ctx context.Context, tenant model.TenantID, state, kind string, q model.Query) ([]ProviderRecord, model.Page, error) {
	if m.data == nil {
		return nil, model.Page{}, errNoData
	}
	if state != "" {
		q.Filters = append(q.Filters, eq(colPRState, state))
	}
	if kind != "" {
		q.Filters = append(q.Filters, eq(colPRKind, kind))
	}
	var out []ProviderRecord
	var page model.Page
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerRecordKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(ctx, q)
		if err != nil {
			return err
		}
		out = make([]ProviderRecord, 0, len(recs))
		for _, rec := range recs {
			out = append(out, providerRecordFromRecord(rec))
		}
		page = p
		return nil
	})
	return out, page, err
}

// PatchProviderRecord renames, re-endpoints and/or ROTATES one record. Rotation
// reseals under the SAME locator, so every binding keeps working and the next
// launch — not the live one — uses the new value.
func (m *Module) PatchProviderRecord(ctx context.Context, tenant model.TenantID, ref string, p ProviderRecordPatch) (ProviderRecord, error) {
	if m.data == nil {
		return ProviderRecord{}, errNoData
	}
	if !validProviderRecordRef(ref) {
		return ProviderRecord{}, ErrProviderRecordNotFound
	}
	if p.APIKey != nil && m.rt.providerVault == nil {
		return ProviderRecord{}, ErrNoProviderVault
	}
	var key string
	if p.APIKey != nil {
		k, err := validProviderKey(*p.APIKey)
		if err != nil {
			return ProviderRecord{}, err
		}
		key = k
	}
	var name string
	if p.DisplayName != nil {
		n, err := validProviderName(*p.DisplayName)
		if err != nil {
			return ProviderRecord{}, err
		}
		name = n
	}
	// The rotation is performed BEFORE the transaction and the transaction is what
	// records it. The order matters in exactly one direction: a sealed value whose
	// row never landed is invisible residue, while a row that names a value that was
	// never sealed would authorize a launch that then cannot start.
	var out ProviderRecord
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(providerRecordKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := findProviderRecordRec(ctx, sc, ref)
		if rerr != nil {
			return rerr
		}
		if rec.String(colPRState) == ProviderRecordRevoked {
			return ErrProviderRecordRevoked
		}
		if p.BaseURL != nil {
			b, verr := validProviderBaseURL(rec.String(colPRKind), *p.BaseURL)
			if verr != nil {
				return verr
			}
			rec[colPRBaseURL] = b
		}
		if p.DisplayName != nil {
			rec[colPRDisplayName] = name
			rec[colPRNameSlot] = activeProviderNameSlot(rec.String(colPRKind), name)
		}
		if p.APIKey != nil {
			if _, serr := m.rt.providerVault.Seal(ctx, p.Actor, tenant, providerVaultName(ref), []byte(key)); serr != nil {
				return sealFailure(serr)
			}
			rec[colPRKeyHint] = providerKeyHint(key)
			// A rotated credential has not been tested. Keeping the previous verdict
			// would report a green connection that was measured on another value.
			rec[colPRProbeState] = ProbeNever
			rec[colPRProbeDetail] = ""
			rec[colPRProbeLatency] = int64(0)
			rec[colPRModels] = ""
		}
		updated, rerr := repo.Update(ctx, rec)
		if rerr != nil {
			return rerr
		}
		out = providerRecordFromRecord(updated)
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		return ProviderRecord{}, ErrProviderNameTaken
	}
	if err != nil {
		return ProviderRecord{}, err
	}
	return out, nil
}

// RevokeProviderRecord withdraws a credential for good. The sealed value is
// destroyed, the row stays, and its name becomes free for another record.
//
// The row is NOT deleted, and the binding on a profile is NOT rewritten. A profile
// that names this record is refused at launch by name, which an operator can act
// on; a binding that silently vanished would present as "no credential configured"
// on a profile the operator configured deliberately.
func (m *Module) RevokeProviderRecord(ctx context.Context, actor auth.Principal, tenant model.TenantID, ref string) (ProviderRecord, error) {
	if m.data == nil {
		return ProviderRecord{}, errNoData
	}
	if !validProviderRecordRef(ref) {
		return ProviderRecord{}, ErrProviderRecordNotFound
	}
	var secretRef string
	var out ProviderRecord
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(providerRecordKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := findProviderRecordRec(ctx, sc, ref)
		if rerr != nil {
			return rerr
		}
		if rec.String(colPRState) == ProviderRecordRevoked {
			return ErrProviderRecordRevoked
		}
		secretRef = rec.String(colPRSecretRef)
		rec[colPRState] = ProviderRecordRevoked
		rec[colPRNameSlot] = revokedProviderNameSlot(ref)
		rec[colPRRevokedAt] = model.NewTimestamp(m.now()).String()
		rec[colPRProbeState] = ProbeNever
		rec[colPRProbeDetail] = ""
		rec[colPRModels] = ""
		updated, rerr := repo.Update(ctx, rec)
		if rerr != nil {
			return rerr
		}
		out = providerRecordFromRecord(updated)
		return nil
	})
	if err != nil {
		return ProviderRecord{}, err
	}
	// The state change is what refuses the launch; destroying the value is what makes
	// the refusal true even for a reader that goes around the state. It runs AFTER the
	// row is committed so a failed transaction never destroys a live credential.
	if m.rt.providerVault != nil && secretRef != "" {
		if verr := m.rt.providerVault.Revoke(ctx, actor, tenant, secretRef); verr != nil && m.log != nil {
			m.log.Warn("sessions: the provider record is revoked but its sealed value could not be destroyed",
				"provider_ref", ref,
				"effect", "launches are already refused by the record state; the sealed blob remains until the vault is repaired")
		}
	}
	return out, nil
}

// TestProviderRecord asks the provider which models it serves, with the registered
// credential, and records the outcome on the row.
//
// It NEVER sends a completion. A test that generated a token would spend the
// operator's money, on a model nobody chose, to answer a question a model LIST
// already answers: is the endpoint reachable and does it accept this credential.
func (m *Module) TestProviderRecord(ctx context.Context, tenant model.TenantID, ref string) (ProviderRecord, error) {
	if m.data == nil {
		return ProviderRecord{}, errNoData
	}
	if m.rt.providerProbe == nil {
		return ProviderRecord{}, ErrNoProviderProbe
	}
	if m.rt.providerVault == nil {
		return ProviderRecord{}, ErrNoProviderVault
	}
	rec, err := m.GetProviderRecord(ctx, tenant, ref)
	if err != nil {
		return ProviderRecord{}, err
	}
	if rec.State != ProviderRecordActive {
		return ProviderRecord{}, ErrProviderRecordRevoked
	}
	key, err := m.rt.providerVault.Open(ctx, tenant, rec.SecretRef)
	if err != nil {
		return ProviderRecord{}, openFailure(err)
	}
	started := m.now()
	res, probeErr := m.rt.providerProbe.Probe(ctx, ProviderProbeRequest{
		Kind: rec.Kind, BaseURL: rec.BaseURL, APIKey: string(key),
	})
	elapsed := m.now().Sub(started)
	if elapsed < 0 {
		elapsed = 0
	}
	state, detail, models := classifyProbe(res, probeErr)
	return m.recordProbeOutcome(ctx, tenant, ref, state, detail, models, elapsed)
}

// classifyProbe turns a probe answer into the three-valued outcome. The split is
// the point: a refusal is a verdict about the credential, an unreachable endpoint
// is not, and reporting the second as the first sends an operator to regenerate a
// key that was never the problem.
func classifyProbe(res ProviderProbeResult, err error) (state, detail string, models []string) {
	switch {
	case err == nil:
		models = res.Models
		if len(models) > maxProbedModelCount {
			models = models[:maxProbedModelCount]
		}
		return ProbeOK, boundProbeDetail(res.Detail), models
	case errors.Is(err, ErrProviderRefused):
		return ProbeRefused, boundProbeDetail(err.Error()), nil
	default:
		return ProbeUnreachable, boundProbeDetail(err.Error()), nil
	}
}

// boundProbeDetail keeps the stored sentence bounded and single-line. A probe
// detail that grew a newline would be a probe detail that can forge a log record.
func boundProbeDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxProbeDetail {
		s = s[:maxProbeDetail]
	}
	return s
}

// recordProbeOutcome persists one probe result. It writes the outcome even when the
// outcome is a failure: a record whose last test failed must READ as a record whose
// last test failed, and silently keeping the previous green is how a console comes
// to show a connection that stopped working days ago.
func (m *Module) recordProbeOutcome(
	ctx context.Context,
	tenant model.TenantID,
	ref, state, detail string,
	models []string,
	elapsed time.Duration,
) (ProviderRecord, error) {
	encoded := ""
	if len(models) > 0 {
		if b, err := json.Marshal(models); err == nil {
			encoded = string(b)
		}
	}
	var out ProviderRecord
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, rerr := sc.Ext(providerRecordKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := findProviderRecordRec(ctx, sc, ref)
		if rerr != nil {
			return rerr
		}
		rec[colPRProbeState] = state
		rec[colPRProbeDetail] = detail
		rec[colPRProbeLatency] = elapsed.Milliseconds()
		rec[colPRProbedAt] = model.NewTimestamp(m.now()).String()
		rec[colPRModels] = encoded
		updated, rerr := repo.Update(ctx, rec)
		if rerr != nil {
			return rerr
		}
		out = providerRecordFromRecord(updated)
		return nil
	})
	if err != nil {
		return ProviderRecord{}, err
	}
	return out, nil
}
