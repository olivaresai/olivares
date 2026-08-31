// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// errNoData is returned when a seam method is used before the engine (or a test)
// wired the data handle via UseData.
var errNoData = errors.New("governance: module has no data handle; call UseData first")

// identityAttrAllowlist is the CLOSED set of directory attribute keys copied from
// the roster into core Identity.Metadata. It is an allow-list, not a deny-list, so
// a PII-bearing key (email, upn, mail, …) is dropped by construction and a future
// new attribute key cannot leak (docs/SECURITY-HARDENING.md; the auth-partition PII carve-out
// means email/upn must never reach an exportable, read-tier
// entity). Identity metadata is the per-person PII vector, so the strictest filter
// applies here.
//
// "clearance" and "region" are AUTHORIZATION attributes, not personal data: a
// classification-clearance label (public/internal/confidential/secret) and a
// data-residency region code are governance facts about what an identity may read,
// not who the person is — they carry no PII. They are allow-listed so the directory
// can drive governed retrieval: knowledge.RetrievalGuard reads attr_clearance
// / attr_region from Identity.Metadata to filter chunks by classification and to
// enforce data residency (the "Identity → Clearance/Region" contract closes it).
// Absent (the directory provides neither)
// the guard fails closed to public / no-region — never a silent over-grant.
var identityAttrAllowlist = map[string]bool{"ou": true, "trust_domain": true, "clearance": true, "region": true}

// pii-or-secret-bearing attribute key fragments dropped from collection (group)
// attributes — group metadata is structural (a Vault policy's path count), lower
// risk than identity attributes, so a deny-list keeps the useful keys while still
// guaranteeing the obvious sensitive ones never persist.
var droppedAttrFragments = []string{"email", "mail", "upn", "password", "passwd", "pwd", "secret", "token", "apikey", "api_key", "credential", "private", "key"}

// maxCollectionAttrs bounds the attribute map persisted on a collection.
const maxCollectionAttrs = 64

// RosterReport summarizes one reconciliation pass.
type RosterReport struct {
	Source              string `json:"source,omitempty"`
	Sources             int    `json:"sources"`
	ProvidersConfigured int    `json:"providers_configured"`
	Identities          int    `json:"identities"`
	Collections         int    `json:"collections"`
	Memberships         int    `json:"memberships"`
	// ProvidersFailed names the identity sources whose Snapshot did not answer this
	// call. It exists so that ONE failing source stops taking the whole tenant down
	// with it — see handleRosterSync — and so that surviving is not the same as
	// succeeding silently: a caller reading only `sources` would see a smaller number
	// and no reason for it.
	ProvidersFailed []RosterProviderFailure `json:"providers_failed,omitempty"`
	Note            string                  `json:"note,omitempty"`
}

// RosterProviderFailure identifies one identity source that could not be snapshotted.
//
// Provider is the connector's Go type (e.g. "*conjur.Connector"), not a configured
// name: GraphProvider carries no name, and adding one to a public interface for a
// diagnostic would make every implementer pay for it. The type is enough to tell an
// operator WHICH connector to look at, which is the question this field answers.
//
// Reason is deliberately FIXED and carries no error text. Before this change the route
// answered a generic "identity source snapshot failed" and put the real error in the
// debug log, and that disclosure posture is not what is being changed here: a directory
// error can carry a host, a DN or a token fragment. This change is about AVAILABILITY —
// one dead source no longer kills the others — and it would be a poor trade to buy that
// by leaking what the old 502 was careful not to say. The detail stays in the log.
type RosterProviderFailure struct {
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

func (r *RosterReport) add(o RosterReport) {
	r.Identities += o.Identities
	r.Collections += o.Collections
	r.Memberships += o.Memberships
}

// SyncRoster reconciles graph into the bound tenant on the SEAM path (the
// composition root's background/periodic Sync), auditing as the module connector
// since there is no human principal. The route path uses mc.Data + the real
// principal instead (handleRosterSync). Both ultimately call reconcileGraph.
func (m *Module) SyncRoster(ctx context.Context, tenant model.TenantID, graph identitysource.Graph) (RosterReport, error) {
	if m.data == nil {
		return RosterReport{}, errNoData
	}
	var rep RosterReport
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		r, e := reconcileGraph(ctx, sc, graph)
		if e != nil {
			return e
		}
		rep = r
		// map registry-declared owner/sponsor onto the lifecycle model,
		// in the same transaction so the refs resolve against this very sync.
		owned, skippedOwn, e := m.syncFederatedOwnership(ctx, sc, graph)
		if e != nil {
			return e
		}
		meta := map[string]any{"source": string(graph.Source), "identities": r.Identities, "collections": r.Collections, "memberships": r.Memberships}
		if owned > 0 || skippedOwn > 0 {
			meta["ownership_set"] = owned
			meta["ownership_skipped"] = skippedOwn
		}
		// a connector that deferred agent identities to a dedicated registry
		// connector (idp's Entra ServiceIdentity skip) reports the count, so an
		// estate missing that connector sees the unwatched class in every sync.
		if graph.DeferredAgentIdentities > 0 {
			meta["agent_identities_deferred"] = graph.DeferredAgentIdentities
		}
		_, e = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "connector:" + Name, ActorKind: model.ActorConnector,
			Action: "governance.roster.sync",
			Meta:   meta,
		})
		return e
	})
	return rep, err
}

// Sync reconciles every configured provider into its tenant (the composition
// root's entry point; not reachable from a route). It accumulates per-provider
// errors and continues, so one unreachable source does not abort the others.
func (m *Module) Sync(ctx context.Context) error {
	m.mu.Lock()
	providers := append([]RosterBinding(nil), m.providers...)
	m.mu.Unlock()
	var errs []error
	for _, b := range providers {
		tenant, ok := tenantOf(b.TenantRef)
		if !ok {
			continue
		}
		graph, err := b.Provider.Snapshot(ctx)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := m.SyncRoster(ctx, tenant, graph); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// reconcileGraph reconciles one identity Graph into the canonical entities within
// the tenant-pinned Scope: each Identity find-or-creates/UPGRADES a core Identity
// keyed on external_id ALONE (so it converges on the same row the access-map
// bridge creates from a raw audit reference — the precondition for firm
// attribution, ARCHITECTURE.md), and each Collection/Membership upserts the owned
// graph. Idempotent: a second pass updates in place rather than duplicating.
func reconcileGraph(ctx context.Context, sc store.Scope, graph identitysource.Graph) (RosterReport, error) {
	rep := RosterReport{Source: string(graph.Source)}
	for _, id := range graph.Identities {
		if strings.TrimSpace(id.Ref) == "" {
			continue // an identity with no stable ref cannot be a de-dup anchor — skip
		}
		if _, err := upsertIdentity(ctx, sc, id); err != nil {
			return rep, err
		}
		rep.Identities++
	}
	colRepo, err := sc.Ext(collectionKind)
	if err != nil {
		return rep, err
	}
	for _, c := range graph.Collections {
		if strings.TrimSpace(c.Ref) == "" {
			continue
		}
		if err := upsertCollection(ctx, colRepo, c); err != nil {
			return rep, err
		}
		rep.Collections++
	}
	memRepo, err := sc.Ext(memberKind)
	if err != nil {
		return rep, err
	}
	for _, mem := range graph.Memberships {
		if strings.TrimSpace(mem.MemberRef) == "" || strings.TrimSpace(mem.CollectionRef) == "" {
			continue
		}
		if err := upsertMembership(ctx, memRepo, mem); err != nil {
			return rep, err
		}
		rep.Memberships++
	}
	return rep, nil
}

// upsertIdentity find-or-creates a core Identity by external_id == ident.Ref
// (verbatim — the SAME namespacing the access-map bridge uses, e.g. Vault's
// "entity:<name>"), and upgrades an existing row in place rather than inserting a
// second. Keyed on external_id ALONE, never also on kind/provider, so a row the
// bridge created as kind="credential" is found and enriched, not duplicated.
func upsertIdentity(ctx context.Context, sc store.Scope, ident identitysource.Identity) (model.ID, error) {
	cur, found, err := identityByExternalID(ctx, sc, ident.Ref)
	if err != nil {
		return "", err
	}
	meta := identityMeta(cur.Metadata, ident)
	if found {
		changed := false
		if ident.DisplayName != "" && cur.Name != ident.DisplayName {
			cur.Name = ident.DisplayName
			changed = true
		}
		if ident.Kind != "" && cur.Kind != ident.Kind {
			cur.Kind = ident.Kind
			changed = true
		}
		if src := string(ident.Source); src != "" && cur.Provider != src {
			cur.Provider = src
			changed = true
		}
		if !metaEqual(cur.Metadata, meta) {
			cur.Metadata = meta
			changed = true
		}
		if !changed {
			return cur.ID, nil
		}
		out, err := sc.Identities().Update(ctx, cur)
		if err != nil {
			return "", err
		}
		return out.ID, nil
	}
	name := ident.DisplayName
	if name == "" {
		name = ident.Ref
	}
	kind := ident.Kind
	if kind == "" {
		kind = "identity"
	}
	out, err := sc.Identities().Create(ctx, model.Identity{
		Name: name, Kind: kind, ExternalID: ident.Ref, Provider: string(ident.Source), Metadata: meta,
	})
	if err != nil {
		return "", err
	}
	return out.ID, nil
}

// identityMeta builds the non-PII governance metadata for a core Identity: the
// roster-managed keys plus the allow-listed directory attributes, merged over any
// metadata the bridge already set (which it never sets, but the merge is
// defensive). PII keys (email/upn/mail) are dropped by the allow-list.
func identityMeta(existing map[string]any, ident identitysource.Identity) map[string]any {
	out := map[string]any{}
	for k, v := range existing {
		out[k] = v
	}
	out["principal_type"] = string(ident.Type)
	out["disabled"] = ident.Disabled
	if s := string(ident.Source); s != "" {
		out["source"] = s
	}
	if ident.Kind != "" {
		out["roster_kind"] = ident.Kind
	}
	for k, v := range ident.Attributes {
		if identityAttrAllowlist[strings.ToLower(strings.TrimSpace(k))] {
			out["attr_"+k] = v
		}
	}
	return out
}

// metaEqual compares two metadata maps by their canonical JSON, so an upgrade with
// no real change skips the version-bumping Update.
func metaEqual(a, b map[string]any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// identityByExternalID resolves a core Identity by external_id with the bridge's
// own ambiguity tolerance: it returns the first match (Limit 2 distinguishes 0/1
// from 2+), never silently picking one of several for ATTRIBUTION — but for an
// idempotent UPSERT, enriching the first match is correct. Under the single-writer
// SQLite deployment (MaxOpenConns(1)) there is no concurrent second creator, so no
// duplicate arises; there is no DB-level unique index on identity.external_id
// (single-writer model, see access-map/bridge.go), which is why this and the
// bridge must both key on external_id alone to converge.
func identityByExternalID(ctx context.Context, sc store.Scope, ref string) (model.Identity, bool, error) {
	list, _, err := sc.Identities().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", ref)}, Limit: 2})
	if err != nil {
		return model.Identity{}, false, err
	}
	if len(list) == 0 {
		return model.Identity{}, false, nil
	}
	return list[0], true, nil
}

// upsertCollection find-or-creates a collection by (source, ref) and updates it in
// place. Attributes are deny-list sanitized (group metadata is structural, lower
// risk than identity attributes).
func upsertCollection(ctx context.Context, repo store.GenericRepo, c identitysource.Collection) error {
	fields := model.Record{
		colSource:      string(c.Source),
		colColRef:      c.Ref,
		colColKind:     string(c.Kind),
		colDisplayName: c.DisplayName,
		colAttributes:  sanitizeAttrs(c.Attributes),
	}
	cur, found, err := findOne(ctx, repo, eq(colSource, string(c.Source)), eq(colColRef, c.Ref))
	if err != nil {
		return err
	}
	if found {
		for k, v := range fields {
			cur[k] = v
		}
		_, err := repo.Update(ctx, cur)
		return err
	}
	_, err = repo.Create(ctx, fields)
	if isConflict(err) { // a concurrent creator won the race; the row exists — fine
		return nil
	}
	return err
}

// upsertMembership find-or-creates a membership edge by (source, collection, member).
func upsertMembership(ctx context.Context, repo store.GenericRepo, mem identitysource.Membership) error {
	kind := string(mem.MemberKind)
	if kind == "" {
		kind = string(identitysource.MemberIdentity)
	}
	fields := model.Record{
		colSource:        string(mem.Source),
		colCollectionRef: mem.CollectionRef,
		colMemberRef:     mem.MemberRef,
		colMemberKind:    kind,
	}
	cur, found, err := findOne(ctx, repo, eq(colSource, string(mem.Source)), eq(colCollectionRef, mem.CollectionRef), eq(colMemberRef, mem.MemberRef))
	if err != nil {
		return err
	}
	if found {
		cur[colMemberKind] = kind
		_, err := repo.Update(ctx, cur)
		return err
	}
	_, err = repo.Create(ctx, fields)
	if isConflict(err) {
		return nil
	}
	return err
}

// sanitizeAttrs deny-list filters a directory attribute map and returns it as a
// bounded JSON object string. It drops any key whose lowercase name contains a
// PII/secret fragment and any value that looks like an inline credential, and caps
// the count — so a value can never reach storage even if a connector misbehaves.
func sanitizeAttrs(attrs map[string]string) string {
	if len(attrs) == 0 {
		return "{}"
	}
	clean := make(map[string]string, len(attrs))
	for k, v := range attrs {
		if len(clean) >= maxCollectionAttrs {
			break
		}
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" || containsDroppedFragment(lk) || containsInlineCredential(v) {
			continue
		}
		clean[k] = v
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func containsDroppedFragment(lowerKey string) bool {
	for _, frag := range droppedAttrFragments {
		if strings.Contains(lowerKey, frag) {
			return true
		}
	}
	return false
}

// containsInlineCredential rejects the obvious ways a credential could be smuggled
// into a value field (basic-auth userinfo in a URL, or a secret-like assignment).
// It is a guardrail, not a scanner — the real guarantee is the allow-list on
// identity metadata and the typed specs elsewhere. It never stores the matched value.
func containsInlineCredential(s string) bool {
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	if i := strings.Index(low, "://"); i >= 0 {
		rest := low[i+3:]
		if at := strings.IndexByte(rest, '@'); at >= 0 && strings.IndexByte(rest[:at], ':') >= 0 {
			return true
		}
	}
	for _, kw := range []string{"token=", "secret=", "password=", "passwd=", "api_key=", "apikey=", "access_key=", "client_secret="} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// --- read DTOs + handlers ----------------------------------------------------

// identityDTO is the governance view of a core Identity: its stable ref, label,
// classification and account status. It never carries an email or any credential.
type identityDTO struct {
	ID            string `json:"id"`
	Ref           string `json:"ref"`
	Name          string `json:"name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Source        string `json:"source,omitempty"`
	PrincipalType string `json:"principal_type,omitempty"`
	Disabled      bool   `json:"disabled"`
}

func toIdentityDTO(i model.Identity) identityDTO {
	d := identityDTO{ID: i.ID.String(), Ref: i.ExternalID, Name: i.Name, Kind: i.Kind, Source: i.Provider}
	if i.Metadata != nil {
		if s, ok := i.Metadata["principal_type"].(string); ok {
			d.PrincipalType = s
		}
		if b, ok := i.Metadata["disabled"].(bool); ok {
			d.Disabled = b
		}
	}
	return d
}

// handleListIdentities lists the reconciled identity roster. Viewing who exists in
// the estate is recon-relevant, so the read self-audits in a committed
// transaction before returning, exactly as the access-map graph reads do.
func (m *Module) handleListIdentities(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("source"); v != "" {
		q.Filters = append(q.Filters, eq("provider", v))
	}
	if v := r.URL.Query().Get("kind"); v != "" {
		q.Filters = append(q.Filters, eq("kind", v))
	}
	out := listResponse[identityDTO]{Items: []identityDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "governance.identity.list", collectionKind, "", nil); err != nil {
			return err
		}
		list, page, err := sc.Identities().List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, i := range list {
			out.Items = append(out.Items, toIdentityDTO(i))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// groupDTO is a directory group/role/policy mirrored from a source.
type groupDTO struct {
	Ref         string `json:"ref"`
	Kind        string `json:"kind,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Source      string `json:"source,omitempty"`
}

func toGroupDTO(rec model.Record) groupDTO {
	return groupDTO{Ref: rec.String(colColRef), Kind: rec.String(colColKind), DisplayName: rec.String(colDisplayName), Source: rec.String(colSource)}
}

// handleListGroups lists the reconciled collections (groups/roles/policies).
func (m *Module) handleListGroups(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("source"); v != "" {
		q.Filters = append(q.Filters, eq(colSource, v))
	}
	if v := r.URL.Query().Get("kind"); v != "" {
		q.Filters = append(q.Filters, eq(colColKind, v))
	}
	out := listResponse[groupDTO]{Items: []groupDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(collectionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toGroupDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// memberDTO is one resolved member of a group.
type memberDTO struct {
	MemberRef  string `json:"member_ref"`
	MemberKind string `json:"member_kind"`
	Via        string `json:"via,omitempty"` // the nested collection a transitive identity came through
}

// maxMembershipDepth bounds transitive group resolution against a cyclic directory.
const maxMembershipDepth = 32

// handleGroupMembers lists a group's members. With ?transitive=true it walks
// nested collections (a group within a group) to the leaf identities, bounded by a
// visited set and a depth cap so a cyclic directory cannot loop.
func (m *Module) handleGroupMembers(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if strings.TrimSpace(ref) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("group ref is required"))
		return
	}
	transitive := r.URL.Query().Get("transitive") == "true"
	out := listResponse[memberDTO]{Items: []memberDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(memberKind)
		if err != nil {
			return err
		}
		members, err := resolveMembers(r.Context(), repo, ref, transitive)
		if err != nil {
			return err
		}
		out.Items = members
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveMembers returns the direct (or, when transitive, the transitively
// resolved) members of a collection.
func resolveMembers(ctx context.Context, repo store.GenericRepo, root string, transitive bool) ([]memberDTO, error) {
	direct, err := listAll(ctx, repo, eq(colCollectionRef, root))
	if err != nil {
		return nil, err
	}
	if !transitive {
		out := make([]memberDTO, 0, len(direct))
		for _, rec := range direct {
			out = append(out, memberDTO{MemberRef: rec.String(colMemberRef), MemberKind: rec.String(colMemberKind)})
		}
		return out, nil
	}
	visited := map[string]bool{root: true}
	seenMember := map[string]bool{}
	var out []memberDTO
	type frame struct {
		ref string
		via string
		d   int
	}
	queue := []frame{{ref: root, d: 0}}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		recs := direct
		if f.ref != root {
			recs, err = listAll(ctx, repo, eq(colCollectionRef, f.ref))
			if err != nil {
				return nil, err
			}
		}
		for _, rec := range recs {
			mref := rec.String(colMemberRef)
			mkind := rec.String(colMemberKind)
			if mkind == string(identitysource.MemberCollection) {
				if !visited[mref] && f.d+1 < maxMembershipDepth {
					visited[mref] = true
					queue = append(queue, frame{ref: mref, via: mref, d: f.d + 1})
				}
				continue
			}
			if seenMember[mref] {
				continue
			}
			seenMember[mref] = true
			out = append(out, memberDTO{MemberRef: mref, MemberKind: mkind, Via: f.via})
		}
	}
	if out == nil {
		out = []memberDTO{}
	}
	return out, nil
}

// handleRosterSync triggers a roster reconciliation for the resolved tenant from
// the identity providers the composition root wired (UseRosterProviders). It is
// admin-tier and self-audited per source with the real principal. With no provider
// configured for the tenant it returns a report saying so — never a silent no-op.
func (m *Module) handleRosterSync(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.mu.Lock()
	providers := append([]RosterBinding(nil), m.providers...)
	m.mu.Unlock()

	// ProvidersConfigured counts the providers bound to THIS tenant, not every tenant's. It used
	// to be len(providers) — the whole wired list — so a tenant with none could report a non-zero
	// count and never reach the "no providers configured" note, while a single-tenant deployment
	// read correctly and hid it. Found by the adversarial contrast (P1-3). Filled after the loop
	// from `matched`, the same counter the 502 and the note already use, so the three cannot drift.
	agg := RosterReport{}
	// matched counts the providers bound to THIS tenant, which is not len(providers)
	// (that counts every tenant's) and not agg.Sources (that counts the ones that
	// answered). The 502 below needs the difference: "every source I had failed" and
	// "I had no sources" are different answers and must not collapse into one.
	matched := 0
	for _, b := range providers {
		t, ok := tenantOf(b.TenantRef)
		if !ok || t != mc.Tenant {
			continue
		}
		matched++
		graph, err := b.Provider.Snapshot(r.Context())
		if err != nil {
			// ACCUMULATE AND CONTINUE. This used to `return` a 502 on the FIRST provider
			// that failed, which meant one unreachable identity source took every other
			// source of that tenant down with it — including the open-core ones that were
			// answering perfectly.
			//
			// That is not a robustness nit, it is a product rule: closing an entitlement
			// seam on a commercial connector then REMOVES capability from a customer who
			// paid, because a lapsed license on one connector would 502 the whole roster.
			// The enterprise conjur connector documents exactly this — its Snapshot is
			// deliberately ungated, with a test pinning the reason, and its comment names
			// this loop as "the hub change that unblocks it". Requested by another lane;
			// the gate itself is theirs to add in enterprise/ once this lands.
			//
			// The failure becomes a datum in the report rather than the end of the call.
			// ⚠ DECLARADO, no introducido aquí: este debugf pasa el error CRUDO del conector, y
			// core/api/log_handler.go expone un visor de logs a `system:admin`, así que con DEBUG
			// activo un host, un DN o un fragmento de token pueden llegar a esa API. La línea es
			// IDÉNTICA a la de main (roster.go:626 allí) — la heredé, no la escribí. Lo que este
			// cambio SÍ altera es la frecuencia: antes el primer fallo abortaba el tenant y se
			// registraba UNO; ahora se registran todos los de la pasada. Señalado por el contraste
			// adversarial (P1-2) y dejado fuera de este PR a propósito: redactar el error de
			// conector es una política de logging de todo el módulo, no de esta función, y
			// arreglarla aquí la dejaría a medias en las otras rutas que hacen lo mismo.
			m.debugf("governance: roster snapshot failed", "err", err)
			agg.ProvidersFailed = append(agg.ProvidersFailed, RosterProviderFailure{
				Provider: fmt.Sprintf("%T", b.Provider),
				Reason:   "snapshot failed",
			})
			continue
		}
		agg.Sources++
		err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			rep, e := reconcileGraph(r.Context(), sc, graph)
			if e != nil {
				return e
			}
			agg.add(rep)
			// same federated-ownership mapping as the seam path (SyncRoster).
			owned, skippedOwn, e := m.syncFederatedOwnership(r.Context(), sc, graph)
			if e != nil {
				return e
			}
			meta := map[string]any{
				"source": string(graph.Source), "identities": rep.Identities, "collections": rep.Collections, "memberships": rep.Memberships,
			}
			if owned > 0 || skippedOwn > 0 {
				meta["ownership_set"] = owned
				meta["ownership_skipped"] = skippedOwn
			}
			if graph.DeferredAgentIdentities > 0 {
				meta["agent_identities_deferred"] = graph.DeferredAgentIdentities
			}
			return auditEvent(r.Context(), sc, mc, "governance.roster.sync", collectionKind, "", meta)
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}
	}
	agg.ProvidersConfigured = matched

	// TOTAL failure is still a 502, and that is the half of the old behavior worth
	// keeping: if every source this tenant has went down, answering 200 with zeroes
	// would report "nothing changed" for what is really "I could not look" — the exact
	// confusion this repository keeps removing from its gates.
	if matched > 0 && agg.Sources == 0 {
		writeJSON(w, http.StatusBadGateway, errorBody("every identity source for this tenant failed to snapshot"))
		return
	}
	// The note is about ABSENCE and must not be printed for FAILURE. It used to be guarded
	// on agg.Sources==0, and the mutant that removed the 502 above printed the result: the
	// body said "no identity providers configured for this tenant" while providers_failed
	// listed two of them.
	//
	// DECLARED, because a reader deserves to know what this line does and does not buy:
	// under the 502 above the two conditions are EQUIVALENT — reaching here with matched>0
	// and Sources==0 is impossible — so this changes no observable behavior today, and a
	// mutant reverting it to agg.Sources==0 SURVIVES the battery. It is kept because it is
	// the semantically correct condition (the sentence is about configuration, not about
	// reachability) and because the equivalence is an accident of statement order that the
	// next edit can undo silently. Not covered, and said so rather than left to look tested.
	if matched == 0 {
		agg.Note = "no identity providers configured for this tenant"
	}
	// A PARTIAL failure answers 200 WITH the failures listed, not 207 and not 502. The
	// call did real work — the surviving sources were reconciled and audited — so 502
	// would be false, and 207 would change a contract the console already consumes for a
	// case it can read from the body. The honesty lives in providers_failed, which is
	// why that field carries names and not a count.
	writeJSON(w, http.StatusOK, agg)
}
