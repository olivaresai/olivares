// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the IMMUTABLE policy-revision store shared by the managed-*
// authoring console (B, claudepolicy.go) and the Cedar/OPA editor (C,
// pdp_authoring.go). A published revision is APPEND-ONLY: the monotonic
// (tenant, surface, revision) key is unique at the DB level, so a published version
// can never be silently rewritten and rollback/audit always read the exact bytes.

// Authoring surfaces. The four managed-* surfaces are the Claude Code policy files;
// cedar/opa are the policy-as-code engines. The revision store is shared across all.
const (
	surfaceManagedSettings = "managed-settings"
	surfaceHooks           = "hooks"
	surfaceManagedMCP      = "managed-mcp"
	surfaceSandbox         = "sandbox"
	surfaceCedar           = "cedar"
	surfaceOPA             = "opa"
)

// maxPolicyContentBytes bounds an authored document so a hostile/huge paste cannot
// exhaust storage. A managed-settings.json is tiny; a Cedar policy set is small.
const maxPolicyContentBytes = 256 << 10 // 256 KiB

// activationSurfaceSuffix identifies the append-only activation stream for an
// authored surface. Policy revisions remain immutable; selecting an older revision
// appends a pointer here instead of rewriting either the old or current policy row.
// this internal surface avoids a schema migration while preserving the
// existing (tenant, surface, revision) append-only uniqueness boundary.
const activationSurfaceSuffix = "-activation"

// revisionDTO is the wire shape of one stored revision (mirrors the frontend
// PolicyVersion). content is included only on a single-revision read, omitted from
// list pages (metadata-only) to keep them light.
type revisionDTO struct {
	Revision  int64  `json:"revision"`
	Surface   string `json:"surface"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Content   string `json:"content,omitempty"`
	Validated bool   `json:"validated"`
	Active    bool   `json:"active,omitempty"`
	Note      string `json:"note,omitempty"`
}

func toRevisionDTO(rec model.Record, includeContent bool) revisionDTO {
	d := revisionDTO{
		Revision:  rec.Int(colRevNumber),
		Surface:   rec.String(colRevSurface),
		Author:    rec.String(colRevAuthor),
		CreatedAt: rec.String(model.ColCreatedAt),
		Validated: rec.Bool(colRevValidated),
		Active:    rec.Bool(colRevActive),
		Note:      rec.String(colRevNote),
	}
	if includeContent {
		d.Content = rec.String(colRevContent)
	}
	return d
}

// containsInlineKey is the minimal-data backstop on a stored policy document: a
// managed-settings.json / hooks / Cedar / Rego artifact must never carry a live
// Anthropic credential. It rejects the obvious leak (an sk-ant- key) so a smuggled
// secret can never reach the immutable revision store (docs/SECURITY-HARDENING.md). It is a guardrail,
// not a scanner — structural validation is the real frontier.
func containsInlineKey(content string) bool {
	return strings.Contains(content, "sk-ant-")
}

// nextRevisionNumber returns max(revision)+1 for a surface (1 for the first). Under
// the single-writer SQLite deployment there is no race; under Postgres a concurrent
// publish loses the unique-index race on Create and the handler retries.
func nextRevisionNumber(ctx context.Context, sc store.Scope, surface string) (int64, error) {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return 0, err
	}
	recs, err := listAll(ctx, repo, eq(colRevSurface, surface))
	if err != nil {
		return 0, err
	}
	var maxNum int64
	for _, r := range recs {
		if n := r.Int(colRevNumber); n > maxNum {
			maxNum = n
		}
	}
	return maxNum + 1, nil
}

// appendRevision writes a new immutable revision for a surface and returns its
// number. It must run inside a Mutate closure; the caller audits the publish in the
// SAME transaction so the revision and its self-audit commit atomically (deny-closed:
// if the audit append fails, the whole publish rolls back). A store.ErrConflict from
// the unique index means a concurrent publish won the number — the caller retries.
func appendRevision(ctx context.Context, sc store.Scope, surface, content, author string, validated, active bool, note string) (int64, model.ID, error) {
	num, err := nextRevisionNumber(ctx, sc, surface)
	if err != nil {
		return 0, "", err
	}
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return 0, "", err
	}
	rec := model.Record{
		colRevSurface:   surface,
		colRevNumber:    num,
		colRevContent:   content,
		colRevAuthor:    author,
		colRevValidated: validated,
		colRevActive:    active,
		colRevNote:      note,
	}
	created, err := repo.Create(ctx, rec)
	if err != nil {
		return 0, "", err
	}
	return num, model.ID(created.String(model.ColID)), nil
}

// listRevisions returns a surface's revisions newest-first, metadata only (no
// content). It drains every page so the version history is complete.
func listRevisions(ctx context.Context, sc store.Scope, surface string) ([]revisionDTO, error) {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return nil, err
	}
	recs, err := listAll(ctx, repo, eq(colRevSurface, surface))
	if err != nil {
		return nil, err
	}
	active, hasActive, err := activeRevisionNumber(ctx, sc, surface)
	if err != nil {
		return nil, err
	}
	out := make([]revisionDTO, 0, len(recs))
	for _, r := range recs {
		d := toRevisionDTO(r, false)
		// Stored active flags are immutable legacy publication facts. The activation
		// stream is the current selection and therefore the only row reported active.
		d.Active = hasActive && d.Revision == active
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision > out[j].Revision })
	return out, nil
}

func activationSurface(surface string) string { return surface + activationSurfaceSuffix }

// activateRevision appends an immutable selection of targetRevision. The target policy
// row is never updated or duplicated. It must run inside the same Mutate closure as the
// caller's audit append so activation and evidence commit atomically.
func activateRevision(ctx context.Context, sc store.Scope, surface string, targetRevision int64, actor string) (model.ID, error) {
	_, id, err := appendRevision(
		ctx,
		sc,
		activationSurface(surface),
		strconv.FormatInt(targetRevision, 10),
		actor,
		true,
		true,
		fmt.Sprintf("activates %s revision %d", surface, targetRevision),
	)
	return id, err
}

// activeRevisionNumber returns the revision selected by the newest append-only
// activation record. Repositories created before have no activation stream, so
// the highest legacy active=true policy row remains the backward-compatible fallback.
func activeRevisionNumber(ctx context.Context, sc store.Scope, surface string) (int64, bool, error) {
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return 0, false, err
	}
	markers, err := listAll(ctx, repo, eq(colRevSurface, activationSurface(surface)))
	if err != nil {
		return 0, false, err
	}
	var newest model.Record
	var newestNumber int64
	for _, marker := range markers {
		if n := marker.Int(colRevNumber); newest == nil || n > newestNumber {
			newest, newestNumber = marker, n
		}
	}
	if newest != nil {
		target, parseErr := strconv.ParseInt(strings.TrimSpace(newest.String(colRevContent)), 10, 64)
		if parseErr != nil || target <= 0 {
			return 0, false, fmt.Errorf("invalid %s activation record", surface)
		}
		if _, found, getErr := getRevision(ctx, sc, surface, target); getErr != nil {
			return 0, false, getErr
		} else if !found {
			return 0, false, fmt.Errorf("%s activation points to absent revision %d", surface, target)
		}
		return target, true, nil
	}

	legacy, err := listAll(ctx, repo, eq(colRevSurface, surface), eq(colRevActive, true))
	if err != nil {
		return 0, false, err
	}
	var best int64
	found := false
	for _, rec := range legacy {
		if n := rec.Int(colRevNumber); !found || n > best {
			best, found = n, true
		}
	}
	if found && best <= 0 {
		// activationID uses zero as its unselected sentinel. A corrupt legacy
		// active row at zero/negative would otherwise collapse into "no policy",
		// skip bounded-freshness backfill, and let reload treat its bytes as an
		// unselected union. Selection numbers are strictly positive everywhere.
		return 0, false, fmt.Errorf("%s legacy active revision must be positive", surface)
	}
	return best, found, nil
}

// getRevision returns one revision of a surface WITH its content, or ok=false.
// It validates the returned DTO identity as well as the repository query: a
// decorator/TOCTOU can otherwise hand back revision M for a query that selected
// revision N, allowing a rollback to compile M while activating N.
func getRevision(ctx context.Context, sc store.Scope, surface string, number int64) (revisionDTO, bool, error) {
	if number <= 0 {
		return revisionDTO{}, false, fmt.Errorf("%s revision number must be positive", surface)
	}
	repo, err := sc.Ext(revisionKind)
	if err != nil {
		return revisionDTO{}, false, err
	}
	rec, found, err := findOne(ctx, repo, eq(colRevSurface, surface), eq(colRevNumber, number))
	if err != nil || !found {
		return revisionDTO{}, false, err
	}
	revision := toRevisionDTO(rec, true)
	if revision.Revision != number || revision.Surface != surface {
		return revisionDTO{}, false, fmt.Errorf("%s revision lookup %d returned mismatched identity", surface, number)
	}
	return revision, true, nil
}

// latestActiveContent returns the content selected by the append-only activation
// stream, or the highest legacy active=true revision when the stream does not exist.
// It backs boot reload and explicit rollback without ever rewriting policy history.
func latestActiveContent(ctx context.Context, sc store.Scope, surface string) (string, bool, error) {
	content, _, found, err := latestActiveSelection(ctx, sc, surface)
	return content, found, err
}

// latestActiveRevision returns the exact selected revision record, including its
// content and presentation metadata, from one selection read. Callers that later
// disclose the selected revision must carry this DTO forward instead of reading the
// same row again: a decorator/TOCTOU can otherwise mix content from a later read with
// the digest and authorization snapshot that were compiled from this one.
func latestActiveRevision(ctx context.Context, sc store.Scope, surface string) (revisionDTO, bool, error) {
	number, found, err := activeRevisionNumber(ctx, sc, surface)
	if err != nil || !found {
		return revisionDTO{}, found, err
	}
	if number <= 0 {
		return revisionDTO{}, false, fmt.Errorf("%s selected revision must be positive", surface)
	}
	revision, found, err := getRevision(ctx, sc, surface, number)
	if err != nil {
		return revisionDTO{}, false, err
	}
	if !found {
		// activeRevisionNumber proved that this activation selected `number`.
		// A decorator/TOCTOU that makes the row disappear on the second read is
		// not an empty surface: treating it as one would compile a partial Cedar
		// union and potentially omit a forbid. Preserve absence only when there
		// was no activation at all; a selected-but-missing row is fail-closed.
		return revisionDTO{}, false, fmt.Errorf("%s selected revision %d disappeared during selection read", surface, number)
	}
	if revision.Revision != number || revision.Surface != surface {
		// getRevision's query normally makes this impossible. Keep the explicit
		// assertion because an adapter/decorator that validates N on the first
		// read but returns M on this second read would otherwise bind M's content
		// to N's activation and epoch snapshot.
		return revisionDTO{}, false, fmt.Errorf("%s selected revision %d changed identity during selection read", surface, number)
	}
	return revision, true, nil
}

// latestActiveSelection is latestActiveContent plus the revision NUMBER it
// resolved, read in the same transaction. The number is the activation identity:
// content alone cannot tell two revisions apart, because appendRevision never
// deduplicates it.
func latestActiveSelection(ctx context.Context, sc store.Scope, surface string) (string, int64, bool, error) {
	revision, found, err := latestActiveRevision(ctx, sc, surface)
	if err != nil || !found {
		return "", 0, found, err
	}
	return revision.Content, revision.Revision, true, nil
}
