// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// identityDTO is the governance view of a core Identity: its stable ref, label,
// classification and account status. It never carries an email or any credential.
type identityDTO struct {
	ID            string `json:"id"`
	Ref           string `json:"ref"`
	Name          string `json:"name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Source        string `json:"source,omitempty"`
	PrincipalType string `json:"principal_type,omitempty"`
	// Disabled is the account state the source reported. It is null when the
	// source never reported one (an identity the access-map bridge or a binding
	// created from a bare ref), which is not the same as an enabled account.
	Disabled *bool `json:"disabled"`
}

func toIdentityDTO(i model.Identity) identityDTO {
	d := identityDTO{ID: i.ID.String(), Ref: i.ExternalID, Name: i.Name, Kind: i.Kind, Source: i.Provider}
	if i.Metadata != nil {
		if s, ok := i.Metadata["principal_type"].(string); ok {
			d.PrincipalType = s
		}
		if b, ok := i.Metadata["disabled"].(bool); ok {
			d.Disabled = &b
		}
	}
	return d
}

// handleListIdentities lists the reconciled identity roster. Viewing who exists in
// the estate is recon-relevant, so the read self-audits in a committed
// transaction before returning, exactly as the access-map graph reads do.
func (m *Module) handleListIdentities(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(w, r)
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

// handleListGroups lists the reconciled collections (groups/roles/policies). Like the
// identity roster, the read self-audits in a committed transaction before returning.
func (m *Module) handleListGroups(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(w, r)
	if v := r.URL.Query().Get("source"); v != "" {
		q.Filters = append(q.Filters, eq(colSource, v))
	}
	if v := r.URL.Query().Get("kind"); v != "" {
		q.Filters = append(q.Filters, eq(colColKind, v))
	}
	out := listResponse[groupDTO]{Items: []groupDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "governance.group.list", collectionKind, "", nil); err != nil {
			return err
		}
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

type membersResponse struct {
	api.ListResponse[memberDTO]
	Truncated bool `json:"truncated,omitempty"`
	NotFound  bool `json:"not_found,omitempty"`
}

// handleGroupMembers lists a group's members. With ?transitive=true it walks
// nested collections (a group within a group) to the leaf identities, bounded by a
// visited set and a depth cap so a cyclic directory cannot loop. A group that does
// not exist adds not_found=true and a Warning while keeping the published 200;
// a typo is distinguishable from an empty group. A walk cut at
// the depth cap adds truncated=true.
// The read self-audits like the group listing.
func (m *Module) handleGroupMembers(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	if strings.TrimSpace(ref) == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("group ref is required"))
		return
	}
	transitive := r.URL.Query().Get("transitive") == "true"
	out := membersResponse{ListResponse: api.ListResponse[memberDTO]{Items: []memberDTO{}}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "governance.group.members", memberKind, "", nil); err != nil {
			return err
		}
		groups, err := sc.Ext(collectionKind)
		if err != nil {
			return err
		}
		if _, found, err := findOne(r.Context(), groups, eq(colColRef, ref)); err != nil {
			return err
		} else if !found {
			out.NotFound = true
			return nil
		}
		repo, err := sc.Ext(memberKind)
		if err != nil {
			return err
		}
		members, truncated, err := resolveMembers(r.Context(), repo, ref, transitive)
		if err != nil {
			return err
		}
		out.Items, out.Truncated = members, truncated
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if out.NotFound {
		w.Header().Add("Warning", `299 olivares "Unknown-group empty responses are deprecated; not_found distinguishes a missing group from an empty one."`)
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveMembers returns the direct (or, when transitive, the transitively
// resolved) members of a collection. truncated reports that a nested collection
// lay beyond maxMembershipDepth and was not walked, so the members are a partial set.
func resolveMembers(ctx context.Context, repo store.GenericRepo, root string, transitive bool) (members []memberDTO, truncated bool, err error) {
	direct, err := listAll(ctx, repo, eq(colCollectionRef, root))
	if err != nil {
		return nil, false, err
	}
	if !transitive {
		out := make([]memberDTO, 0, len(direct))
		for _, rec := range direct {
			out = append(out, memberDTO{MemberRef: rec.String(colMemberRef), MemberKind: rec.String(colMemberKind)})
		}
		return out, false, nil
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
				return nil, false, err
			}
		}
		for _, rec := range recs {
			mref := rec.String(colMemberRef)
			mkind := rec.String(colMemberKind)
			if mkind == string(identitysource.MemberCollection) {
				if !visited[mref] {
					if f.d+1 >= maxMembershipDepth {
						truncated = true
						continue
					}
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
	return out, truncated, nil
}
