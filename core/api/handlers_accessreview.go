// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// the sealed access-review export. It answers "who can access this resource,
// and how?" as a single signed-into-the-ledger artifact: it runs the SAME reverse
// query the AuthZEN subject-search runs (enumerate the candidate population, batch the
// REAL Authorizer), then computes a content digest and records an access_review.export
// event carrying that digest in the tamper-evident audit ledger (docs/SECURITY-HARDENING.md) — the
// killswitch evidence-pack pattern (killswitch_evidence.go). The seal is fail-closed:
// if the audit append fails, the export is refused (no unsealed artifact escapes).
//
// It is admin-tier (authz:admin) AND AAL3-gated: reconstructing who-can-access-what is
// a privileged action, and a token principal (AAL 0) can never run it.

// accessReviewRequest is the export input: the resource under review, the permissions
// to check (default: the kind's read/write/admin verb tiers), an optional subject-type
// filter, and the assurance to evaluate users at (default AAL3 — maximal standing
// entitlement, the safe direction for a review).
type accessReviewRequest struct {
	Resource struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"resource"`
	Permissions []string `json:"permissions,omitempty"`
	SubjectType string   `json:"subject_type,omitempty"`
	Assurance   *int     `json:"assurance,omitempty"`
}

type accessReviewSubject struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Display string `json:"display,omitempty"`
}

type accessReviewEntry struct {
	Subject    accessReviewSubject `json:"subject"`
	Permission string              `json:"permission"`
	Via        string              `json:"via"` // rbac | scoped-grant | superadmin
	Reason     string              `json:"reason"`
}

type accessReviewResource struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Sensitivity string `json:"sensitivity,omitempty"`
}

type accessReviewIntegrity struct {
	PackSHA256 string `json:"pack_sha256"`
	Sealed     bool   `json:"sealed"`
	AuditSeq   int64  `json:"audit_seq,omitempty"`
}

type accessReviewPack struct {
	Resource    accessReviewResource  `json:"resource"`
	Tenant      string                `json:"tenant"`
	GeneratedAt model.Timestamp       `json:"generated_at"`
	Assurance   int                   `json:"assurance"`
	Permissions []string              `json:"permissions"`
	Population  map[string]any        `json:"population"`
	Entries     []accessReviewEntry   `json:"entries"`
	Integrity   accessReviewIntegrity `json:"integrity"`
}

// accessReviewKinds are the resource kinds an export supports (the tree
// entities access-reviews care about). Other kinds are rejected with a clear 400.
var accessReviewKinds = map[string]bool{"resource": true, "agent": true, "session": true}

// handleAccessReviewExport produces and seals a who-can-access-R access review.
func (s *Server) handleAccessReviewExport(w http.ResponseWriter, r *http.Request) {
	if !s.allowSurface(w, r, azKindExport) {
		return
	}
	p, tenant, ok := s.authzTenant(w, r, auth.PermAuthzAdmin)
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}
	var in accessReviewRequest
	if err := decodeJSON(w, r, &in); err != nil {
		s.badRequest(w, r, "invalid JSON body")
		return
	}
	kind := strings.TrimSpace(in.Resource.Type)
	rid := strings.TrimSpace(in.Resource.ID)
	if kind == "" || rid == "" {
		s.badRequest(w, r, "resource.type and resource.id are required")
		return
	}
	if !accessReviewKinds[kind] {
		s.badRequest(w, r, "access-review export supports resource, agent and session targets")
		return
	}
	resID, err := model.ParseID(rid)
	if err != nil || resID.IsZero() {
		s.badRequest(w, r, "resource.id is not a valid id")
		return
	}
	var subjectFilter auth.PrincipalKind
	hasFilter := false
	if t := strings.TrimSpace(in.SubjectType); t != "" {
		k, okk := authzenKind(t)
		if !okk {
			s.badRequest(w, r, "subject_type must be user or token")
			return
		}
		subjectFilter, hasFilter = k, true
	}
	assurance := auth.AAL3
	if in.Assurance != nil {
		assurance = *in.Assurance
	}
	perms := in.Permissions
	if len(perms) == 0 {
		perms = []string{kind + ":" + auth.VerbRead, kind + ":" + auth.VerbWrite, kind + ":" + auth.VerbAdmin}
	}

	// Phase 1 — confirm the target exists and enrich the pack (404 if absent).
	resMeta := accessReviewResource{Type: kind, ID: rid}
	if err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		switch kind {
		case "resource":
			x, e := sc.Resources().Get(r.Context(), resID)
			if e != nil {
				return e
			}
			resMeta.Name, resMeta.Sensitivity = x.Name, x.Sensitivity
		case "agent":
			x, e := sc.Agents().Get(r.Context(), resID)
			if e != nil {
				return e
			}
			resMeta.Name = x.Name
		case "session":
			x, e := sc.Sessions().Get(r.Context(), resID)
			if e != nil {
				return e
			}
			resMeta.Name = x.Goal
		}
		return nil
	}); err != nil {
		s.writeError(w, r, err)
		return
	}

	// Phase 2 — enumerate the candidate population and batch the REAL Authorizer.
	pop, err := s.authzenPrincipals.TenantPrincipals(r.Context(), tenant, assurance)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	res := auth.ResourceAttrs{Kind: kind, ID: rid}
	entries := []accessReviewEntry{}
	for _, pr := range pop {
		if hasFilter && pr.Kind != subjectFilter {
			continue
		}
		for _, permStr := range perms {
			d := s.authz.Authorize(r.Context(), auth.Request{Principal: pr, Permission: auth.Permission(permStr), Tenant: tenant, Resource: res})
			if !d.Allow {
				continue
			}
			t, id := authzenSubjectRef(pr)
			entries = append(entries, accessReviewEntry{
				Subject:    accessReviewSubject{Type: t, ID: id, Display: pr.DisplayName},
				Permission: permStr,
				Via:        accessVia(pr, d),
				Reason:     d.Reason,
			})
		}
	}

	// Phase 3 — build the pack and its content digest.
	pack := accessReviewPack{
		Resource:    resMeta,
		Tenant:      tenant.String(),
		GeneratedAt: s.clock.Now(),
		Assurance:   assurance,
		Permissions: perms,
		Population:  authzenPopulation(pop),
		Entries:     entries,
	}
	digest, err := accessReviewDigest(pack)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	pack.Integrity.PackSHA256 = digest

	// Phase 4 — seal the export in the tenant ledger (FAIL-CLOSED: no audit, no pack).
	var seq int64
	if err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		ev, e := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor: p.Actor(), ActorKind: p.ActorKind(),
			Action: "access_review.export", TargetKind: model.Kind("core." + kind), TargetID: resID,
			Meta: map[string]any{
				"pack_sha256":      digest,
				"entries":          len(entries),
				"permissions":      perms,
				"assurance":        assurance,
				"population_total": len(pop),
			},
		})
		if e != nil {
			return e
		}
		if ev.Seq == 0 {
			return store.ErrAuditSpoolFull
		}
		seq = ev.Seq
		return nil
	}); err != nil {
		s.writeError(w, r, err)
		return
	}
	pack.Integrity.Sealed = true
	pack.Integrity.AuditSeq = seq
	writeJSON(w, http.StatusOK, pack)
}

// accessReviewDigest is the deterministic SHA-256 over the pack with its Integrity
// block excluded from the preimage (hex). Re-running an export yields a different
// digest because GeneratedAt advances — the digest fingerprints THIS export, and the
// ledger event binds it tamper-evidently.
func accessReviewDigest(pack accessReviewPack) (string, error) {
	pack.Integrity = accessReviewIntegrity{}
	b, err := json.Marshal(pack)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// accessVia classifies HOW a subject's access is conferred, for the reviewer: the
// system role (superadmin), a positive scoped grant, or tenant-wide RBAC.
func accessVia(p auth.Principal, d auth.Decision) string {
	if p.Superadmin {
		return "superadmin"
	}
	if strings.Contains(d.Reason, "scoped grant") {
		return "scoped-grant"
	}
	return "rbac"
}
