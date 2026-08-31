// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SG-00 — the canonical session identity.
//
// Every other notion of "session" in the tree is keyed by a string that belongs
// to somebody else: the observe overlay keys sessions_live on the provider's own
// session id (live.go), the operated run keys sessions_run on a run_ref it mints
// and separately stores claude_session_id (runtime_schema.go), and inventory
// find-or-creates a core model.Session by external_id with no DB-level backstop
// (modules/inventory/entities.go). Nothing says which of those refer to the SAME
// working session, and two providers that both hand out the id "abc" collide.
//
// This file introduces the one identifier that is ours: a stable Olivares
// session id (the "sid"), plus an alias table that maps each provider's own id
// onto it under an engine-enforced UNIQUE (tenant_id, provider, external_id).
// The uniqueness is a property of the database, not a convention the writer is
// trusted to keep — that distinction is the whole point (docs/contracts/SG00).

// Registered kinds for the identity plane.
const (
	identityKind model.Kind = "sessions.identity"
	aliasKind    model.Kind = "sessions.alias"
)

// Physical tables (namespace_snake).
const (
	identityTable = "sessions_identity"
	aliasTable    = "sessions_alias"
)

// sessions.identity columns: the canonical session and its provenance.
const (
	colSID         = "sid"
	colOrigin      = "origin"
	colIDFirstSeen = "first_seen_at"
	colIDLastSeen  = "last_seen_at"
	colDeclaredAt  = "declared_at"
	colMergedInto  = "merged_into"
	// colIDWorkspaceID is the tenant workspace this canonical session is scoped
	// to. NULL means the tenant's DEFAULT workspace, exactly the soft-isolation
	// the rest of the model uses (model.Session.WorkspaceID, model.Agent.WorkspaceID).
	//
	// It exists because the plane must OWN this fact rather than borrow it. The
	// K2 work kernel asks "is this session eligible in workspace W", and the
	// composition root answered it by reading a core model.Session — a different
	// notion of "session" whose primary key a canonical sid never equals (see the
	// SG-00 preamble above). Deriving it instead from the driving agent was
	// considered and REFUSED by the hub on 2026-08-11: it repeats the same
	// pattern of asking the neighboring entity for a fact, and it couples two
	// lifecycles the design separates on purpose — an agent that changes
	// workspace would retroactively move every live session it drives, in a plane
	// whose whole point is that facts are durable.
	colIDWorkspaceID = "workspace_id"
)

// sessions.alias columns: one provider-issued id bound to a canonical session.
const (
	colAliasSID   = "sid"
	colProvider   = "provider"
	colExternalID = "external_id"
	colBoundAt    = "bound_at"
	colAliasConf  = "confidence"
)

// How an identity first came to exist. It records provenance, never authority:
// an observed identity is exactly as canonical as a declared one, because the
// alias — not the origin — is what makes it resolvable.
const (
	// OriginObserved: telemetry arrived before anybody declared the session.
	OriginObserved = "observed"
	// OriginDeclared: a session announced itself to the control plane.
	OriginDeclared = "declared"
	// OriginOperated: the plane launched the session itself (sessions.run).
	OriginOperated = "operated"
	// OriginA2A: an A2A protocol binding minted the synthetic session.
	OriginA2A = "a2a"
	// OriginMCP: an MCP protocol binding minted the synthetic session.
	OriginMCP = "mcp"
)

// Alias confidence. An approximate binding is one the resolver was not able to
// attribute precisely; it is recorded and displayed as such, never silently
// promoted (keeps the same distinction for edges).
const (
	confAttributed  = "attributed"
	confApproximate = "approximate"
)

// sidPrefix marks an Olivares session id at a glance. SG-00 exists because
// provider ids and ours were indistinguishable strings; a prefix makes a
// mistaken cross-assignment visible in a log line and greppable in a database.
const sidPrefix = "osn_"

// maxMergeHops bounds the merged_into walk. A cycle can only exist if a merge
// was recorded wrongly, and an unbounded walk would hang the read path rather
// than report the corruption.
const maxMergeHops = 8

// Identity-plane errors.
var (
	// ErrNoProvider rejects a binding with no provider: without it the alias key
	// degenerates to the bare external id, which is precisely the collision
	// (claude:abc vs codex:abc) this plane exists to prevent.
	ErrNoProvider = errors.New("sessions: binding requires a provider")
	// ErrNoExternalID rejects a binding with no external id.
	ErrNoExternalID = errors.New("sessions: binding requires an external id")
	// ErrAliasBound is returned when a triple already resolves to a DIFFERENT
	// canonical session. An alias binds once: rebinding it would silently
	// re-point references already written to the ledger.
	ErrAliasBound = errors.New("sessions: alias already bound to another session")
	// ErrMergeCycle reports a merged_into chain that does not terminate.
	ErrMergeCycle = errors.New("sessions: merge chain does not terminate")
)

// SessionBinding is the evidence a caller presents to resolve an identity: whose
// session id this is, and how we came to see it.
type SessionBinding struct {
	// Provider is the canonical engine key. Normalized to lower case: "Claude" and
	// "claude" are the same provider, and treating them as two would mint two
	// identities for one session.
	//
	// ⛔ NO SE ENUMERAN AQUÍ, y es deliberado. Esta línea decía «("claude", "codex")» y se
	//    leía como un conjunto CERRADO cuando no lo es: el campo es una cadena libre y no hay
	//    validación contra ninguna lista. La enumeración envejeció sola — Grok Build entró como
	//    TIER 1 el 2026-08-17 y este comentario seguía diciendo dos—, y añadir un tercer nombre
	//    sólo habría movido la fecha de caducidad.
	//
	//    Las claves canónicas las declara `sdk/model` (`EngineClaude`, `EngineCodex`,
	//    `EngineGrok`), que es la única lista que manda. Que el conjunto sea abierto es una
	//    PROPIEDAD, no un descuido: un motor nuevo no tiene que tocar este módulo. Lo que sí
	//    tiene que seguir siendo cierto es que el mismo id externo bajo dos proveedores dé DOS
	//    sesiones —si colisionaran, una sesión de un motor resolvería a la identidad de otro—, y
	//    eso lo fija `identity_grok_test.go`.
	Provider string
	// ExternalID is the provider's own session id, used verbatim (case is
	// significant in provider ids; only surrounding whitespace is trimmed).
	ExternalID string
	// Origin is how the identity came to exist on first sight (OriginObserved,
	// OriginDeclared, OriginOperated). Ignored when the alias already resolves.
	Origin string
	// At is the observation instant; the module clock fills a zero value.
	At time.Time
	// Approximate marks a binding the caller could not attribute precisely.
	Approximate bool
	// WorkspaceID scopes the canonical session to a tenant workspace on FIRST
	// SIGHT. Zero leaves it unscoped, which reads as the tenant's default
	// workspace. A later binding never rewrites it: re-pointing a session's
	// workspace would retroactively change which work it is eligible for, and
	// eligibility that moves under already-scoped work is the opposite of what a
	// durable plane is for.
	WorkspaceID model.ID
}

// normalize validates and canonicalizes a binding.
func (b SessionBinding) normalize(clock model.Clock) (SessionBinding, error) {
	b.Provider = strings.ToLower(strings.TrimSpace(b.Provider))
	b.ExternalID = strings.TrimSpace(b.ExternalID)
	if b.Provider == "" {
		return b, ErrNoProvider
	}
	if b.ExternalID == "" {
		return b, ErrNoExternalID
	}
	if b.Origin == "" {
		b.Origin = OriginObserved
	}
	b.At = nonZeroTime(b.At, clock)
	return b, nil
}

// newSID mints a fresh canonical session id.
func newSID() string { return sidPrefix + string(model.NewID()) }

// registerIdentitySchema declares the identity plane's two entities.
//
// It is a NEW descriptor pair, never an edit of an existing one: the engine
// creates a wholly missing module table from its descriptor on the next boot
// (sqlstore/schema.go reconcileColumns, "the additive-schema-growth vehicle for
// BOTH core auth/entity tables AND module-owned tables"), so declaring a new
// entity IS the migration. Note that store.ExtensionRegistry.Migrations is
// explicitly NOT the mechanism here — its contract says the embedded migration
// FS is "for secondary indexes, data backfills and unregistered helper tables —
// never for creating a registered entity's table (the engine does that from the
// descriptor)" (core/store/registry.go).
func (m *Module) registerIdentitySchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  identityKind,
		Table: identityTable,
		Fields: []model.FieldSpec{
			{Name: colSID, Kind: model.KindText},
			{Name: colOrigin, Kind: model.KindText},
			{Name: colIDFirstSeen, Kind: model.KindTimestamp},
			{Name: colIDLastSeen, Kind: model.KindTimestamp, Indexed: true},
			{Name: colDeclaredAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colMergedInto, Kind: model.KindText, Nullable: true},
			// Nullable is not laziness: it is what lets the engine ADD this column
			// to an existing sessions_identity on the next boot rather than needing
			// a hand-written migration per engine (reconcileColumns refuses a
			// non-nullable add, core/internal/store/sqlstore/schema.go). NULL also
			// carries meaning — the tenant's default workspace — so an identity
			// minted before this column existed reads as the safe default instead
			// of as missing evidence.
			{Name: colIDWorkspaceID, Kind: model.KindUUID, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_identity_sid_uniq",
			Columns: []string{model.ColTenantID, colSID},
			Unique:  true,
		}},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind:  aliasKind,
		Table: aliasTable,
		Fields: []model.FieldSpec{
			{Name: colAliasSID, Kind: model.KindText, Indexed: true},
			{Name: colProvider, Kind: model.KindText},
			{Name: colExternalID, Kind: model.KindText},
			{Name: colBoundAt, Kind: model.KindTimestamp},
			{Name: colAliasConf, Kind: model.KindText},
		},
		Indexes: []model.IndexSpec{{
			// THE guarantee of SG-00. One provider-issued id resolves to exactly
			// one canonical session, and the DATABASE says so — not the writer's
			// discipline. Measured before this index existed: sixteen goroutines
			// re-delivering ONE observation minted FIVE identities and five
			// aliases, on SQLite, in a single process (identity_test.go). The
			// read and the write are separate transactions, so every racer's
			// lookup missed and every racer created. tenant_id leads because a
			// unique index that did not would couple tenants and leak existence
			// (restated at sqlstore/registry.go:223).
			Name:    "sessions_alias_provider_external_uniq",
			Columns: []string{model.ColTenantID, colProvider, colExternalID},
			Unique:  true,
		}},
	})
}

// ResolveSession resolves a provider-issued session id to the canonical Olivares
// session id, minting the identity and its alias the first time the pair is
// seen. It is idempotent: the same binding always yields the same sid.
//
// It owns its transactions rather than joining the caller's, and that is a
// correctness requirement, not a style choice. The losing side of a concurrent
// first sight learns it lost from a UNIQUE-index violation, and on PostgreSQL a
// failed statement poisons the whole transaction — "after a failing statement
// PostgreSQL refuses everything until rollback (25P02)"
// (core/internal/store/sqlstore/migrationunit.go). A loser that tried to re-read
// inside the same transaction would therefore fail on Postgres while passing on
// SQLite, whose single writer serializes transactions so the race never even
// reaches the conflict. The engine's own evidence-claim repo settles the pattern:
// on conflict "the caller's Mutate rolls the WHOLE losing transaction back ...
// and a re-run re-reads the committed winner" (sqlstore/evidenceops.go).
func (m *Module) ResolveSession(ctx context.Context, tenant model.TenantID, b SessionBinding) (string, error) {
	nb, err := b.normalize(m.clock)
	if err != nil {
		return "", err
	}
	if m.data == nil {
		return "", errors.New("sessions: no data handle")
	}
	// 1. Committed read: an already-bound alias resolves without writing.
	if sid, ok, err := m.lookupAlias(ctx, tenant, nb); err != nil {
		return "", err
	} else if ok {
		return sid, m.touch(ctx, tenant, sid, nb.At)
	}
	// 2. First sight: mint identity + alias in one transaction.
	sid, err := m.mintIdentity(ctx, tenant, nb)
	if err == nil {
		return sid, nil
	}
	if !errors.Is(err, store.ErrConflict) {
		return "", err
	}
	// 3. We lost the race. The whole losing transaction rolled back (nothing
	//    half-written, no orphan identity), so a fresh read now sees the winner.
	sid, ok, lerr := m.lookupAlias(ctx, tenant, nb)
	if lerr != nil {
		return "", lerr
	}
	if !ok {
		return "", fmt.Errorf("sessions: alias conflict but no winner for %s:%s", nb.Provider, nb.ExternalID)
	}
	return sid, m.touch(ctx, tenant, sid, nb.At)
}

// lookupAlias reads the canonical sid for a binding, following any merge.
func (m *Module) lookupAlias(ctx context.Context, tenant model.TenantID, b SessionBinding) (string, bool, error) {
	var sid string
	found := false
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, ok, err := findAlias(ctx, sc, b.Provider, b.ExternalID)
		if err != nil || !ok {
			return err
		}
		resolved, err := resolveMerge(ctx, sc, rec.String(colAliasSID))
		if err != nil {
			return err
		}
		sid, found = resolved, true
		return nil
	})
	return sid, found, err
}

// mintIdentity creates the canonical row and its first alias atomically. A
// UNIQUE violation on either insert rolls the whole thing back and surfaces as
// store.ErrConflict.
func (m *Module) mintIdentity(ctx context.Context, tenant model.TenantID, b SessionBinding) (string, error) {
	sid := newSID()
	at := model.NewTimestamp(b.At).String()
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		idRepo, err := sc.Ext(identityKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSID:         sid,
			colOrigin:      b.Origin,
			colIDFirstSeen: at,
			colIDLastSeen:  at,
		}
		if !b.WorkspaceID.IsZero() {
			rec[colIDWorkspaceID] = b.WorkspaceID.String()
		}
		if b.Origin == OriginDeclared {
			rec[colDeclaredAt] = at
		}
		if _, err := idRepo.Create(ctx, rec); err != nil {
			return err
		}
		return bindAlias(ctx, sc, sid, b)
	})
	if err != nil {
		return "", err
	}
	return sid, nil
}

// bindAlias inserts the (provider, external_id) -> sid binding.
func bindAlias(ctx context.Context, sc store.Scope, sid string, b SessionBinding) error {
	repo, err := sc.Ext(aliasKind)
	if err != nil {
		return err
	}
	conf := confAttributed
	if b.Approximate {
		conf = confApproximate
	}
	_, err = repo.Create(ctx, model.Record{
		colAliasSID:   sid,
		colProvider:   b.Provider,
		colExternalID: b.ExternalID,
		colBoundAt:    model.NewTimestamp(b.At).String(),
		colAliasConf:  conf,
	})
	return err
}

// findAlias reads one alias row by its natural key.
func findAlias(ctx context.Context, sc store.Scope, provider, externalID string) (model.Record, bool, error) {
	repo, err := sc.Ext(aliasKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colProvider, provider), eq(colExternalID, externalID)},
		Limit:   1,
	})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	return recs[0], true, nil
}

// findIdentity reads one identity row by its sid.
func findIdentity(ctx context.Context, sc store.Scope, sid string) (model.Record, bool, error) {
	repo, err := sc.Ext(identityKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colSID, sid)},
		Limit:   1,
	})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	return recs[0], true, nil
}

// resolveMerge follows the merged_into chain to the surviving identity. An
// identity that was merged away keeps its row and its aliases: repointing them
// would rewrite references already written elsewhere, so resolution — not
// rewriting — is what makes the merge visible.
func resolveMerge(ctx context.Context, sc store.Scope, sid string) (string, error) {
	seen := make(map[string]bool, maxMergeHops)
	for hops := 0; hops < maxMergeHops; hops++ {
		if seen[sid] {
			return "", fmt.Errorf("%w: at %s", ErrMergeCycle, sid)
		}
		seen[sid] = true
		rec, ok, err := findIdentity(ctx, sc, sid)
		if err != nil {
			return "", err
		}
		if !ok {
			return sid, nil // dangling target: report the id we were given
		}
		next := rec.String(colMergedInto)
		if next == "" {
			return sid, nil
		}
		sid = next
	}
	return "", fmt.Errorf("%w: exceeded %d hops", ErrMergeCycle, maxMergeHops)
}

// touchAttempts bounds the optimistic-concurrency retry in touch. Each attempt
// is a FRESH transaction, so a retry re-reads the version the winner committed;
// the loop converges because every attempt either advances last_seen_at or finds
// it already past its target and stops writing.
const touchAttempts = 4

// touch advances an identity's last_seen_at. It never moves it backwards, so an
// out-of-order delivery cannot rewind liveness.
//
// It RETRIES on a version conflict, and that is not defensive padding: it was a
// measured defect. Concurrent re-deliveries of one session carrying DISTINCT
// instants all try to advance the same row, their read-modify-write shares a
// version, and the losers got store.ErrConflict — which ResolveSession then
// handed to its caller. Re-delivery must RESOLVE, not surface a store conflict;
// a resolver that errors because two observations of the same session arrived
// close together has merely moved the race onto its consumer. It reproduced on
// PostgreSQL and NOT on SQLite, whose single writer serializes the transactions
// away, so the SQLite-only suite reported it green
// (identity_crossbackend_test.go).
func (m *Module) touch(ctx context.Context, tenant model.TenantID, sid string, at time.Time) error {
	var err error
	for i := 0; i < touchAttempts; i++ {
		err = m.touchOnce(ctx, tenant, sid, at)
		if !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	// Exhausted: somebody else has been advancing this row the whole time, which
	// means last_seen_at is moving. Liveness is a hint, never a guarantee the
	// caller's resolution depends on, so this is not worth failing the resolve.
	if m.log != nil {
		m.log.Debug("sessions: last_seen_at left to a concurrent writer", "sid", sid)
	}
	return nil
}

// touchOnce is one optimistic attempt, in its own transaction.
func (m *Module) touchOnce(ctx context.Context, tenant model.TenantID, sid string, at time.Time) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(identityKind)
		if err != nil {
			return err
		}
		rec, ok, err := findIdentity(ctx, sc, sid)
		if err != nil || !ok {
			return err
		}
		atTS := model.NewTimestamp(at).String()
		if cur := rec.String(colIDLastSeen); cur != "" && cur >= atTS {
			return nil
		}
		rec[colIDLastSeen] = atTS
		_, err = repo.Update(ctx, rec)
		return err
	})
}

// DeclareSession binds a provider's session id to a canonical identity on the
// session's own initiative. When telemetry already created the identity, the
// declaration ADOPTS it (that is the point: an observed session that later
// declares itself must not acquire a second identity) and records when the
// declaration arrived.
func (m *Module) DeclareSession(ctx context.Context, tenant model.TenantID, b SessionBinding) (string, error) {
	b.Origin = OriginDeclared
	sid, err := m.ResolveSession(ctx, tenant, b)
	if err != nil {
		return "", err
	}
	at := model.NewTimestamp(nonZeroTime(b.At, m.clock)).String()
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(identityKind)
		if err != nil {
			return err
		}
		rec, ok, err := findIdentity(ctx, sc, sid)
		if err != nil || !ok {
			return err
		}
		if rec.String(colDeclaredAt) != "" {
			return nil // already declared; the first declaration stands
		}
		rec[colDeclaredAt] = at
		_, err = repo.Update(ctx, rec)
		return err
	})
	return sid, err
}

// BindAlias attaches an ADDITIONAL provider id to an existing canonical session
// (a resume that changes the provider's id, a second engine joining the same
// working session). An alias binds ONCE: if the triple already resolves to a
// different session it returns ErrAliasBound rather than re-pointing it.
func (m *Module) BindAlias(ctx context.Context, tenant model.TenantID, sid string, b SessionBinding) error {
	nb, err := b.normalize(m.clock)
	if err != nil {
		return err
	}
	if existing, ok, err := m.lookupAlias(ctx, tenant, nb); err != nil {
		return err
	} else if ok {
		if existing == sid {
			return nil // idempotent re-bind of the same pair
		}
		return fmt.Errorf("%w: %s:%s -> %s", ErrAliasBound, nb.Provider, nb.ExternalID, existing)
	}
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if _, ok, ferr := findIdentity(ctx, sc, sid); ferr != nil {
			return ferr
		} else if !ok {
			return store.ErrNotFound
		}
		return bindAlias(ctx, sc, sid, nb)
	})
	if errors.Is(err, store.ErrConflict) {
		// Lost a concurrent bind. Re-read: same target is success, other is a clash.
		existing, ok, lerr := m.lookupAlias(ctx, tenant, nb)
		if lerr != nil {
			return lerr
		}
		if ok && existing == sid {
			return nil
		}
		return fmt.Errorf("%w: %s:%s -> %s", ErrAliasBound, nb.Provider, nb.ExternalID, existing)
	}
	return err
}
