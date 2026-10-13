// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// maxRevisionWalk bounds the search for the revision before another one. A pack
// is republished by a person, so its history is short; a longer one names its
// predecessor with ?from=.
const maxRevisionWalk = 1024

var sourceDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DiffSide names one side of a comparison: a published revision, or a candidate
// that has been read but not published (RevisionID and Number are then empty).
type DiffSide struct {
	RevisionID     string `json:"revision_id,omitempty"`
	Number         int64  `json:"number,omitempty"`
	SourceDigest   string `json:"source_digest"`
	ManifestDigest string `json:"manifest_digest"`
	ResolvedCommit string `json:"resolved_commit,omitempty"`
}

func revisionSide(rev Revision) DiffSide {
	return DiffSide{RevisionID: rev.ID, Number: rev.Number, SourceDigest: rev.Source.SourceDigest, ManifestDigest: rev.ManifestDigest, ResolvedCommit: rev.Source.ResolvedCommit}
}

// RevisionDiff is the answer of GET /packs/{id}/diff.
type RevisionDiff struct {
	PackID string    `json:"pack_id"`
	From   *DiffSide `json:"from,omitempty"`
	To     DiffSide  `json:"to"`
	PackDiff
}

// UpdateCheck is the answer of POST /packs/{id}/update when nothing is applied:
// the recorded source read again and compared with the latest revision.
type UpdateCheck struct {
	State string `json:"state"`
	Pack  Pack   `json:"pack"`
	// Source is where the candidate was read: the origin, the ref and the
	// subdirectory, so a reviewer sees which tree was compared.
	Source    Source    `json:"source"`
	Current   DiffSide  `json:"current"`
	Candidate *DiffSide `json:"candidate,omitempty"`
	Diff      *PackDiff `json:"diff,omitempty"`
}

type updateRequest struct {
	Apply          bool   `json:"apply,omitempty"`
	Ref            string `json:"ref,omitempty"`
	Subdir         string `json:"subdir,omitempty"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

// loadRevision reads one revision of the pack; a revision of another pack is
// answered as a missing one.
func loadRevision(ctx context.Context, sc store.Scope, packID, revisionID string) (Revision, error) {
	revisions, err := sc.Ext(RevisionKind)
	if err != nil {
		return Revision{}, err
	}
	row, err := revisions.Get(ctx, model.ID(revisionID))
	if err != nil {
		return Revision{}, err
	}
	rev, err := revisionDTO(row)
	if err != nil {
		return Revision{}, err
	}
	if rev.PackID != packID {
		return Revision{}, store.ErrNotFound
	}
	return rev, nil
}

// previousRevision is the revision numbered one below rev.
func previousRevision(ctx context.Context, sc store.Scope, packID string, rev Revision) (Revision, error) {
	revisions, err := sc.Ext(RevisionKind)
	if err != nil {
		return Revision{}, err
	}
	q := model.Query{Limit: 1, Filters: []model.Filter{{Column: "pack_id", Op: model.OpEq, Value: packID}}}
	for walked := 0; walked < maxRevisionWalk; walked++ {
		rows, page, err := revisions.List(ctx, q)
		if err != nil {
			return Revision{}, err
		}
		if len(rows) == 0 {
			break
		}
		candidate, err := revisionDTO(rows[0])
		if err != nil {
			return Revision{}, err
		}
		if candidate.Number == rev.Number-1 {
			return candidate, nil
		}
		if !page.HasMore || page.Cursor == "" || page.Cursor == q.Cursor {
			break
		}
		q.Cursor = page.Cursor
	}
	return Revision{}, store.ErrNotFound
}

// diffPack compares two revisions of a pack from their verified artifacts. With
// no query it compares the latest revision with the one before it.
func (m *Module) diffPack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	packID := chi.URLParam(r, "id")
	fromID, toID := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	for _, id := range []string{fromID, toID} {
		if id != "" && !canonicalID(id) {
			respondError(w, errInvalidRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var to Revision
	var from *Revision
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		packs, err := sc.Ext(PackKind)
		if err != nil {
			return err
		}
		row, err := packs.Get(ctx, model.ID(packID))
		if err != nil {
			return err
		}
		if toID == "" {
			toID = row.String("latest_revision_id")
		}
		if to, err = loadRevision(ctx, sc, packID, toID); err != nil {
			return err
		}
		switch {
		case fromID != "":
			prior, err := loadRevision(ctx, sc, packID, fromID)
			if err != nil {
				return err
			}
			from = &prior
		case to.Number > 1:
			prior, err := previousRevision(ctx, sc, packID, to)
			if err != nil {
				return err
			}
			from = &prior
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	// Each side has its own expanded-size allowance: two valid packs are never
	// refused only because they are compared.
	var fromTotal, toTotal int64
	var fromPack *ValidatedPack
	if from != nil {
		if fromPack, err = m.readArtifact(ctx, mc.Tenant, *from, &fromTotal); err != nil {
			respondError(w, err)
			return
		}
	}
	toPack, err := m.readArtifact(ctx, mc.Tenant, to, &toTotal)
	if err != nil {
		respondError(w, err)
		return
	}
	out := RevisionDiff{PackID: packID, To: revisionSide(to), PackDiff: DiffPacks(fromPack, toPack)}
	if from != nil {
		side := revisionSide(*from)
		out.From = &side
	}
	respond(w, http.StatusOK, out)
}

// sourceReader returns the read of the source a revision came from, so an
// update asks the origin again and never takes a source from the request.
// Built-in packs read the pinned catalog of this binary, Git packs the same URL
// at the recorded (or newly named) ref, registered folders their registration.
// An uploaded archive has no origin to ask.
func (m *Module) sourceReader(ctx context.Context, mc api.ModuleContext, recorded Source, req updateRequest) (func() (*ValidatedPack, Source, error), error) {
	switch recorded.Kind {
	case "builtin":
		if req.Ref != "" || req.Subdir != "" {
			return nil, errInvalidRequest
		}
		return func() (*ValidatedPack, Source, error) { return importBuiltin(ctx, recorded.RequestedRef) }, nil
	case "git":
		ref := cmp.Or(req.Ref, recorded.RequestedRef, "HEAD")
		subdir := cmp.Or(req.Subdir, recorded.Subdir)
		origin, err := validateGitSource(recorded.Origin, ref, subdir)
		if err != nil {
			return nil, err
		}
		return func() (*ValidatedPack, Source, error) {
			if m.opts.Git == nil {
				return nil, Source{}, refuse("source_unavailable", "git transport not configured")
			}
			pack, commit, err := m.opts.Git.Import(ctx, mc.Tenant, origin.String(), ref, subdir)
			if err != nil {
				return nil, Source{}, err
			}
			return pack, Source{Kind: "git", Origin: origin.String(), RequestedRef: ref, Subdir: subdir, ResolvedCommit: commit, SourceDigest: pack.SourceDigest}, nil
		}, nil
	case "workspace":
		if req.Ref != "" || req.Subdir != "" {
			return nil, errInvalidRequest
		}
		ref, dir, ok := strings.Cut(recorded.Origin, "/")
		if !ok || !canonicalID(ref) || !safePath(dir) {
			return nil, refuse("update_unsupported", "workspace origin")
		}
		return func() (*ValidatedPack, Source, error) {
			pack, version, err := m.importWorkspace(ctx, mc, ref, dir)
			if err != nil {
				return nil, Source{}, err
			}
			return pack, Source{Kind: "workspace", Origin: recorded.Origin, SourceDigest: pack.SourceDigest, RegistrationVersion: version}, nil
		}, nil
	}
	return nil, refuse("update_unsupported", recorded.Kind)
}

// updatePack reads a pack's recorded source again. Without apply it answers
// current or available, with the diff of what a new revision would change and
// the manifest digest of the bytes read; nothing is published. With apply it publishes
// exactly the bytes whose digest the caller reviewed, as the next revision.
// Assignments keep their pins: moving them is a separate, explicit action.
func (m *Module) updatePack(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var req updateRequest
	if err := api.DecodeRequestBody(w, r, &req, api.RequestBodySpec{MaxBytes: 4 << 10}); err != nil {
		respondError(w, errInvalidRequest)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if req.Apply {
		if !sourceDigestPattern.MatchString(req.ExpectedDigest) || key == "" || len(key) > 128 || strings.IndexFunc(key, func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
			respondError(w, errInvalidRequest)
			return
		}
	} else if req.ExpectedDigest != "" {
		respondError(w, errInvalidRequest)
		return
	}
	packID := chi.URLParam(r, "id")
	var pack Pack
	var latest Revision
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		packs, err := sc.Ext(PackKind)
		if err != nil {
			return err
		}
		row, err := packs.Get(ctx, model.ID(packID))
		if err != nil {
			return err
		}
		pack = packDTO(row)
		if pack.State != "enabled" {
			return store.ErrConflict
		}
		latest, err = loadRevision(ctx, sc, packID, pack.LatestRevisionID)
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	read, err := m.sourceReader(ctx, mc, latest.Source, req)
	if err != nil {
		respondError(w, err)
		return
	}
	if req.Apply {
		encoded, _ := json.Marshal(req)
		// The reviewed digest is the manifest digest: it covers exactly the selected
		// files, so a Git fetch whose pack bytes differ for the same tree, or a
		// different subdirectory of the same repository, cannot pass for it.
		plan := importPlan{fingerprint: digest(encoded), expectLatest: latest.ID, read: func() (*ValidatedPack, Source, error) {
			candidate, source, err := read()
			if err != nil {
				return nil, Source{}, err
			}
			if candidate.ManifestDigest != req.ExpectedDigest {
				return nil, Source{}, refuse("source_changed", "manifest digest")
			}
			if candidate.ManifestDigest == latest.ManifestDigest {
				return nil, Source{}, refuse("up_to_date", "")
			}
			return candidate, source, nil
		}}
		result, replay, err := m.publishImport(ctx, mc, packID, key, plan)
		if err != nil {
			respondError(w, err)
			return
		}
		status := http.StatusCreated
		if replay {
			status = http.StatusOK
		}
		respond(w, status, result)
		return
	}
	candidate, source, err := read()
	if err != nil {
		respondError(w, err)
		return
	}
	out := UpdateCheck{State: "current", Pack: pack, Source: source, Current: revisionSide(latest)}
	if candidate.ManifestDigest != latest.ManifestDigest {
		var total int64
		current, err := m.readArtifact(ctx, mc.Tenant, latest, &total)
		if err != nil {
			respondError(w, err)
			return
		}
		diff := DiffPacks(current, candidate)
		out.State = "available"
		out.Candidate = &DiffSide{SourceDigest: candidate.SourceDigest, ManifestDigest: candidate.ManifestDigest, ResolvedCommit: source.ResolvedCommit}
		out.Diff = &diff
	}
	respond(w, http.StatusOK, out)
}
