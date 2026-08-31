// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// sessionDTO is the recording-session view. tip_hash/seqs make the ledger
// binding externally checkable; gap is the reserved>written evidence.
type sessionDTO struct {
	ID          string `json:"id"`
	Subject     string `json:"subject"`
	SubjectKind string `json:"subject_kind"`
	SubjectUser string `json:"subject_user,omitempty"`
	Cred        string `json:"cred"`
	Status      string `json:"status"`
	OpenedAt    string `json:"opened_at"`
	LastAt      string `json:"last_at,omitempty"`
	SealedAt    string `json:"sealed_at,omitempty"`
	SealReason  string `json:"seal_reason,omitempty"`
	Written     int64  `json:"frames_written"`
	Reserved    int64  `json:"frames_reserved"`
	Gap         bool   `json:"gap"`
	TipHash     string `json:"tip_hash,omitempty"`
	OpenSeq     int64  `json:"open_seq,omitempty"`
	AnchorSeq   int64  `json:"anchor_seq,omitempty"`
	SealSeq     int64  `json:"seal_seq,omitempty"`
	ConsentAt   string `json:"consent_at,omitempty"`
	ConsentMode string `json:"consent_mode,omitempty"`
	BreakGlass  string `json:"breakglass_grant,omitempty"`
	Summary     string `json:"summary,omitempty"`
	SummaryMeta any    `json:"summary_meta,omitempty"`
	Retention   string `json:"retention_class,omitempty"`
}

func toSessionDTO(rec model.Record) sessionDTO {
	dto := sessionDTO{
		ID: rec.String(model.ColID), Subject: rec.String(colSubject),
		SubjectKind: rec.String(colSubjectKind), SubjectUser: rec.String(colSubjectUser),
		Cred: rec.String(colCred), Status: rec.String(colStatus),
		OpenedAt: rec.String(colOpenedAt), LastAt: rec.String(colLastAt),
		SealedAt: rec.String(colSealedAt), SealReason: rec.String(colSealReason),
		Written: rec.Int(colWritten), Reserved: rec.Int(colReserved),
		TipHash: rec.String(colTipHash), OpenSeq: rec.Int(colOpenSeq),
		AnchorSeq: rec.Int(colAnchorSeq), SealSeq: rec.Int(colSealSeq),
		ConsentAt: rec.String(colConsentAt), ConsentMode: rec.String(colConsentMode),
		BreakGlass: rec.String(colBGGrant), Summary: rec.String(colSummary),
		Retention: rec.String(colRetention),
	}
	dto.Gap = dto.Reserved > dto.Written
	if raw := rec.String(colSummaryMeta); raw != "" {
		var meta map[string]any
		if err := json.Unmarshal([]byte(raw), &meta); err == nil {
			dto.SummaryMeta = meta
		}
	}
	return dto
}

// frameDTO is one human-readable timeline entry of the replay.
type frameDTO struct {
	Idx       int64             `json:"idx"`
	At        string            `json:"at"`
	Actor     string            `json:"actor"`
	ActorKind string            `json:"actor_kind"`
	ActorUser string            `json:"actor_user,omitempty"`
	ActAs     string            `json:"act_as,omitempty"`
	Namespace string            `json:"namespace"`
	Method    string            `json:"method"`
	Pattern   string            `json:"pattern"`
	Perm      string            `json:"perm"`
	Params    map[string]string `json:"params,omitempty"`
	QueryKeys string            `json:"query_keys,omitempty"`
	Status    int64             `json:"http_status"`
	Outcome   string            `json:"outcome"`
	BodySHA   string            `json:"body_sha256,omitempty"`
	BodyBytes int64             `json:"body_bytes,omitempty"`
	DurMS     int64             `json:"dur_ms"`
	PrevHash  string            `json:"prev_hash"`
	Hash      string            `json:"hash"`
	AnchorSeq int64             `json:"anchor_seq,omitempty"`
}

func toFrameDTO(rec model.Record) frameDTO {
	return frameDTO{
		Idx: rec.Int(colFrIdx), At: rec.String(colFrAt),
		Actor: rec.String(colFrActor), ActorKind: rec.String(colFrActorKind),
		ActorUser: rec.String(colFrActorUser), ActAs: rec.String(colFrActAs),
		Namespace: rec.String(colFrNamespace), Method: rec.String(colFrMethod),
		Pattern: rec.String(colFrPattern), Perm: rec.String(colFrPerm),
		Params: paramsOf(rec), QueryKeys: rec.String(colFrQueryKeys),
		Status: rec.Int(colFrStatus), Outcome: rec.String(colFrOutcome),
		BodySHA: rec.String(colFrBodySHA), BodyBytes: rec.Int(colFrBodyBytes),
		DurMS: rec.Int(colFrDurMS), PrevHash: rec.String(colFrPrevHash),
		Hash: rec.String(colFrHash), AnchorSeq: rec.Int(colFrAnchorSeq),
	}
}

// ledgerEventDTO is one correlated audit-ledger event inside the session
// window (the semantic layer the frames are replayed against).
type ledgerEventDTO struct {
	Seq        int64  `json:"seq"`
	OccurredAt string `json:"occurred_at"`
	Actor      string `json:"actor"`
	Action     string `json:"action"`
	TargetKind string `json:"target_kind,omitempty"`
	TargetID   string `json:"target_id,omitempty"`
}

// handleNotice returns the caller's recording posture for this tenant: what is
// recorded, the consent mode, and whether THIS credential has an acknowledged
// active session. It is consent-exempt by design (it is how a console learns
// the policy before the operator consents).
func (m *Module) handleNotice(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	cfg, err := m.configOf(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	type noticeResponse struct {
		RecordedNamespaces []string `json:"recorded_namespaces"`
		BreakGlassAlways   bool     `json:"breakglass_always"`
		ConsentMode        string   `json:"consent_mode"`
		ConsentRequired    bool     `json:"consent_required"`
		Acknowledged       bool     `json:"acknowledged"`
		SessionID          string   `json:"session_id,omitempty"`
		Schema             string   `json:"schema"`
		Semconv            string   `json:"semconv"`
	}
	out := noticeResponse{
		RecordedNamespaces: sortedNamespaces(cfg),
		BreakGlassAlways:   true,
		ConsentMode:        cfg.consent,
		Schema:             schemaVersion,
		Semconv:            semconvVersion,
	}
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(r.Context(), repo, eq(colCred, credOf(mc.Principal)), eq(colOpenGuard, openGuard))
		if err != nil || !found {
			return err
		}
		out.SessionID = rec.String(model.ColID)
		out.Acknowledged = rec.String(colConsentAt) != ""
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	out.ConsentRequired = mc.Principal.Kind == auth.KindUser && cfg.consent == consentRequired && !out.Acknowledged
	writeJSON(w, http.StatusOK, out)
}

// handleAck records the operator's explicit AC-8 acknowledgement: it opens (or
// finds) the caller's active recording session and stamps consent. Human
// operators only (a token's consent is its provisioning); idempotent.
func (m *Module) handleAck(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if mc.Principal.Kind != auth.KindUser || mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("recording consent is acknowledged by human operators; automation is covered at provisioning"))
		return
	}
	now := m.clock.Now()
	var out struct {
		SessionID      string `json:"session_id"`
		AcknowledgedAt string `json:"acknowledged_at"`
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(r.Context(), repo, eq(colCred, credOf(mc.Principal)), eq(colOpenGuard, openGuard))
		if err != nil {
			return err
		}
		if !found {
			rec, err = m.openSession(r.Context(), sc, mc.Tenant, mc.Principal, now, consentRequired)
			if err != nil {
				return err
			}
		}
		// The explicit ack is the "required"-grade acknowledgement: it upgrades a
		// session auto-opened under "notice" too (after a consent flip, the Gate
		// re-gates any session whose consent_mode is below the tenant's dial).
		if rec.String(colConsentAt) == "" || rec.String(colConsentMode) != consentRequired {
			if rec.String(colConsentAt) == "" {
				rec[colConsentAt] = now.String()
			}
			rec[colConsentMode] = consentRequired
			// Audit before locking the session row. sealLocked and frame anchors
			// use the same tenant-audit → session order; reversing it here can
			// deadlock a concurrent break-glass review on PostgreSQL.
			if _, err := appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
				actionConsentAck, sessionKind, model.ID(rec.String(model.ColID)), nil, nil); err != nil {
				return err
			}
			if rec, err = repo.Update(r.Context(), rec); err != nil {
				return err
			}
		}
		out.SessionID = rec.String(model.ColID)
		out.AcknowledgedAt = rec.String(colConsentAt)
		return nil
	})
	if err != nil {
		if isConflict(err) {
			writeJSON(w, http.StatusConflict, errorBody("concurrent acknowledgement; retry"))
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListSessions lists recording sessions (admin). Viewing recordings is a
// privileged read of operator activity: it runs in a Mutate so its self-audit
// persists (the accessgraph.read pattern).
func (m *Module) handleListSessions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colStatus, v))
	}
	if v := r.URL.Query().Get("subject_user"); v != "" {
		q.Filters = append(q.Filters, eq(colSubjectUser, v))
	}
	if v := r.URL.Query().Get("grant"); v != "" {
		q.Filters = append(q.Filters, eq(colBGGrant, v))
	}
	if v := r.URL.Query().Get("seal_reason"); v != "" {
		q.Filters = append(q.Filters, eq(colSealReason, v))
	}
	if v := r.URL.Query().Get("opened_after"); v != "" {
		q.Filters = append(q.Filters, model.Filter{Column: colOpenedAt, Op: model.OpGte, Value: v})
	}
	if v := r.URL.Query().Get("opened_before"); v != "" {
		q.Filters = append(q.Filters, model.Filter{Column: colOpenedAt, Op: model.OpLte, Value: v})
	}
	// subject_contains is a literal substring search. The store passes the
	// pattern verbatim to SQL LIKE, so escape metacharacters before adding the
	// contains wildcards.
	if v := r.URL.Query().Get("subject_contains"); v != "" {
		q.Filters = append(q.Filters, model.Filter{
			Column: colSubject,
			Op:     model.OpLike,
			Value:  literalContainsPattern(v),
		})
	}
	out := listResponse[sessionDTO]{Items: []sessionDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toSessionDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		_, err = appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSessionRead, sessionKind, "", map[string]any{"items": len(out.Items)}, nil)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// literalContainsPattern turns arbitrary text into a SQL LIKE contains pattern.
// Backslashes must be escaped first so the escapes introduced for % and _ are
// not themselves doubled.
func literalContainsPattern(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `%`, `\%`)
	escaped = strings.ReplaceAll(escaped, `_`, `\_`)
	return `%` + escaped + `%`
}

// handleGetSession returns one session's metadata (admin; self-audited).
func (m *Module) handleGetSession(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out sessionDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toSessionDTO(rec)
		_, err = appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSessionRead, sessionKind, id, nil, nil)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// replayLedgerCap bounds the correlated ledger events one replay page carries.
const replayLedgerCap = 500

// replayWalkCap bounds how many ledger events the correlation walk inspects, so
// a busy tenant chain cannot turn one replay into an unbounded scan.
const replayWalkCap = int64(20000)

// handleReplay returns the forensic replay of one session: its session header,
// a page of frames (chain order), and the correlated audit-ledger events of the
// session's window — reconstruction, not blobs. Self-audited (viewing a
// recording is itself evidence).
func (m *Module) handleReplay(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	type replayResponse struct {
		Schema          string                 `json:"schema"`
		Semconv         string                 `json:"semconv"`
		Session         sessionDTO             `json:"session"`
		Frames          listResponse[frameDTO] `json:"frames"`
		Ledger          []ledgerEventDTO       `json:"ledger"`
		LedgerTruncated bool                   `json:"ledger_truncated"`
	}
	out := replayResponse{Schema: schemaVersion, Semconv: semconvVersion, Ledger: []ledgerEventDTO{}}
	out.Frames.Items = []frameDTO{}
	q := listQuery(r)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		sessions, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := sessions.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out.Session = toSessionDTO(rec)

		frames, err := sc.Ext(frameKind)
		if err != nil {
			return err
		}
		// Frames were created in chain order; UUIDv7 row ids keep the default
		// keyset order aligned with idx, so cursor pagination works unchanged.
		q.Filters = append(q.Filters, eq(colFrSession, id.String()))
		recs, page, err := frames.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, fr := range recs {
			out.Frames.Items = append(out.Frames.Items, toFrameDTO(fr))
		}
		out.Frames.Cursor, out.Frames.HasMore = page.Cursor, page.HasMore

		// Correlate the session window's semantic ledger events: same actor or
		// targeting this session, between the open anchor and the seal (or head).
		if from := rec.Int(colOpenSeq); from > 0 {
			out.Ledger, out.LedgerTruncated, err = collectLedgerWindow(r.Context(), sc, from,
				rec.Int(colSealSeq), rec.String(colSubject), id)
			if err != nil {
				return err
			}
		}
		_, err = appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSessionReplay, sessionKind, id, nil, nil)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// unifiedResponse is the combined evidence + operational view of one recording
// session — the single-call surface for the session recording viewer.
type unifiedResponse struct {
	Schema          string                 `json:"schema"`
	Semconv         string                 `json:"semconv"`
	Session         sessionDTO             `json:"session"`
	Live            *liveCorrelation       `json:"live"`
	Frames          listResponse[frameDTO] `json:"frames"`
	Timeline        timelineResponse       `json:"timeline"`
	Ledger          []ledgerEventDTO       `json:"ledger"`
	LedgerTruncated bool                   `json:"ledger_truncated"`
	Verify          *verifyResponse        `json:"verify"`
}

// timelineResponse keeps capability availability separate from cardinality: an
// empty wired timeline is evidence; the noop resolver means we could not look.
type timelineResponse struct {
	Items     api.JSONArray[TimelineEntry] `json:"items"`
	Cursor    string                       `json:"cursor,omitempty"`
	HasMore   bool                         `json:"has_more"`
	Available bool                         `json:"available"`
}

// liveCorrelation holds the cross-module session reference when the recording
// credential is correlated to an active operational session.
type liveCorrelation struct {
	SessionRef string `json:"session_ref"`
}

// handleUnified returns the combined forensic + operational view of one
// recording session in a single call: session header, frames (paginated),
// ledger correlation, chain-verification verdict, and the correlated
// operational timeline from the sessions module (via the TimelineResolver
// seam). Self-audited with the replay action.
func (m *Module) handleUnified(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}

	out := unifiedResponse{
		Schema:  schemaVersion,
		Semconv: semconvVersion,
		Ledger:  []ledgerEventDTO{},
	}
	out.Frames.Items = []frameDTO{}
	out.Timeline.Items = api.JSONArray[TimelineEntry]{}

	frameQ := listQuery(r)
	// Allow a separate frame_cursor so the caller can page frames independently.
	if c := r.URL.Query().Get("frame_cursor"); c != "" {
		frameQ.Cursor = c
	}

	var sess model.Record

	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		sessions, serr := sc.Ext(sessionKind)
		if serr != nil {
			return serr
		}
		rec, serr := sessions.Get(r.Context(), id)
		if serr != nil {
			return serr
		}
		sess = rec
		out.Session = toSessionDTO(rec)

		// Frames (same ordering as handleReplay: UUIDv7 keyset = chain order).
		frames, ferr := sc.Ext(frameKind)
		if ferr != nil {
			return ferr
		}
		frameQ.Filters = append(frameQ.Filters, eq(colFrSession, id.String()))
		recs, page, ferr := frames.List(r.Context(), frameQ)
		if ferr != nil {
			return ferr
		}
		for _, fr := range recs {
			out.Frames.Items = append(out.Frames.Items, toFrameDTO(fr))
		}
		out.Frames.Cursor, out.Frames.HasMore = page.Cursor, page.HasMore

		// Ledger correlation: semantic events in the session's window.
		if from := rec.Int(colOpenSeq); from > 0 {
			var lerr error
			out.Ledger, out.LedgerTruncated, lerr = collectLedgerWindow(r.Context(), sc, from,
				rec.Int(colSealSeq), rec.String(colSubject), id)
			if lerr != nil {
				return lerr
			}
		}

		// Chain verification (inline, same logic as handleVerify).
		vr, verr := m.verifySession(r.Context(), sc, rec, id)
		if verr == nil {
			out.Verify = &vr
		}

		// Self-audit: reading the unified view is a replay read.
		_, aerr := appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSessionReplay, sessionKind, id, nil, nil)
		return aerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Cross-module: resolve the operational timeline. This runs OUTSIDE the
	// recording transaction — it belongs to a different module and must not
	// hold the recording store scope open during a potentially slow cross-module
	// call.
	cred := sess.String(colCred)
	if cred != "" && m.timelineResolverConfigured {
		out.Timeline.Available = true
		tlLimit := 50
		if l := r.URL.Query().Get("timeline_limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 {
				tlLimit = n
			}
		}
		sessionRef, tl, nextCursor, hasMore, resolveErr := m.timelineResolver.ResolveTimeline(
			r.Context(), mc.Tenant, cred, tlLimit, r.URL.Query().Get("timeline_cursor"),
		)
		if resolveErr != nil {
			out.Timeline.Available = false
		} else {
			if sessionRef != "" {
				out.Live = &liveCorrelation{SessionRef: sessionRef}
			}
			out.Timeline.Items = append(out.Timeline.Items, tl...)
			out.Timeline.Cursor, out.Timeline.HasMore = nextCursor, hasMore
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// errStopWalk is the early-stop sentinel for ledger walks.
var errStopWalk = errors.New("stop walk")

// validationErrActiveSummary marks the summarize-on-active refusal (409).
var validationErrActiveSummary = errors.New("active session cannot be summarized")

// collectLedgerWindow walks the tenant chain from fromSeq, collecting events by
// the session's subject (or targeting the session) until sealSeq (0 = head),
// the collection cap, or the walk cap.
func collectLedgerWindow(ctx context.Context, sc store.Scope, fromSeq, sealSeq int64, subject string, session model.ID) ([]ledgerEventDTO, bool, error) {
	out := []ledgerEventDTO{}
	truncated := false
	walked := int64(0)
	err := sc.Audit().Walk(ctx, fromSeq, func(ev model.AuditEvent) error {
		if sealSeq > 0 && ev.Seq > sealSeq {
			return errStopWalk
		}
		walked++
		if walked > replayWalkCap {
			truncated = true
			return errStopWalk
		}
		if ev.Actor != subject && ev.TargetID != session {
			return nil
		}
		if len(out) >= replayLedgerCap {
			truncated = true
			return errStopWalk
		}
		out = append(out, ledgerEventDTO{
			Seq: ev.Seq, OccurredAt: ev.OccurredAt.String(), Actor: ev.Actor,
			Action: ev.Action, TargetKind: string(ev.TargetKind), TargetID: ev.TargetID.String(),
		})
		return nil
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, false, err
	}
	return out, truncated, nil
}

// verifyResponse is the chain-verification report of one session.
type verifyResponse struct {
	OK             bool   `json:"ok"`
	FramesChecked  int64  `json:"frames_checked"`
	BreakAt        int64  `json:"break_at,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Written        int64  `json:"written"`
	Reserved       int64  `json:"reserved"`
	Gap            bool   `json:"gap"`
	TipMatch       bool   `json:"tip_match"`
	AnchorsChecked int64  `json:"anchors_checked"`
	AnchorsOK      bool   `json:"anchors_ok"`
	// AnchorFailures localizes every failed ledger-anchor check (which anchor,
	// where, why) — a forensic reviewer must never have to re-walk the ledger by
	// hand to find the divergence.
	AnchorFailures []anchorFailure `json:"anchor_failures,omitempty"`
	// AnchoredThrough is the highest frame idx covered by a VERIFIED ledger
	// anchor (the seal covers everything). Frames beyond it on an ACTIVE session
	// are bound only by the chain + the session tip until the next anchor or the
	// seal lands — the same bounded residual as the ledger's own checkpoint
	// interval (docs/SECURITY-HARDENING.md).
	AnchoredThrough int64 `json:"anchored_through"`
}

// anchorFailure names one failed anchor check.
type anchorFailure struct {
	Kind   string `json:"kind"` // open | periodic | seal
	Seq    int64  `json:"seq"`
	AtIdx  int64  `json:"at_idx,omitempty"`
	Reason string `json:"reason"` // missing | wrong-event | tip-mismatch
}

// handleExport returns the session and its full frame list as either a JSON
// document (format=json, default) or a plain-text timeline summary
// (format=summary). Intended as a single-call export surface for tooling and
// the session recording viewer. Self-audited with the replay action.
func (m *Module) handleExport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "summary" {
		writeJSON(w, http.StatusBadRequest, errorBody("format must be json or summary"))
		return
	}

	var sess sessionDTO
	allFrames := []frameDTO{}

	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		sessions, serr := sc.Ext(sessionKind)
		if serr != nil {
			return serr
		}
		rec, serr := sessions.Get(r.Context(), id)
		if serr != nil {
			return serr
		}
		sess = toSessionDTO(rec)

		frames, ferr := sc.Ext(frameKind)
		if ferr != nil {
			return ferr
		}
		q := model.Query{Filters: []model.Filter{eq(colFrSession, id.String())}, Limit: listCap}
		for {
			recs, page, lerr := frames.List(r.Context(), q)
			if lerr != nil {
				return lerr
			}
			for _, fr := range recs {
				allFrames = append(allFrames, toFrameDTO(fr))
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			q.Cursor = page.Cursor
		}
		_, aerr := appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSessionReplay, sessionKind, id, nil, nil)
		return aerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	if format == "json" {
		writeJSON(w, http.StatusOK, struct {
			Session sessionDTO `json:"session"`
			Frames  []frameDTO `json:"frames"`
		}{Session: sess, Frames: allFrames})
		return
	}

	// Summary: plain-text timeline.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Session: %s\nSubject: %s\nStatus: %s\nOpened: %s\nSealed: %s\nFrames: %d\n\n",
		sess.ID, sess.Subject, sess.Status, sess.OpenedAt, sess.SealedAt, sess.Written)
	fmt.Fprintln(w, "--- Timeline ---")
	for _, f := range allFrames {
		fmt.Fprintf(w, "[%04d] %s %s %s → %s (%s, %dms)\n",
			f.Idx, f.At, f.Method, f.Pattern, f.Outcome, f.Actor, f.DurMS)
	}
}

// handleVerify recomputes one session's frame chain and re-checks every ledger
// anchor (open, periodic, seal) against the chain — proves, never trusts.
func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out verifyResponse
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		sessions, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := sessions.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out, err = m.verifySession(r.Context(), sc, rec, id)
		if err != nil {
			return err
		}
		_, err = appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSessionVerify, sessionKind, id, map[string]any{"ok": out.OK}, nil)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// verifySession is the verification core (shared with tests): recompute the
// frame chain, compare the tip, and re-check the ledger anchors.
func (m *Module) verifySession(ctx context.Context, sc store.Scope, sess model.Record, id model.ID) (verifyResponse, error) {
	out := verifyResponse{OK: true, AnchorsOK: true,
		Written: sess.Int(colWritten), Reserved: sess.Int(colReserved)}
	out.Gap = out.Reserved > out.Written

	frames, err := sc.Ext(frameKind)
	if err != nil {
		return out, err
	}
	recs, err := listAll(ctx, frames, eq(colFrSession, id.String()))
	if err != nil {
		return out, err
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Int(colFrIdx) < recs[j].Int(colFrIdx) })

	fail := func(at int64, reason string) {
		if out.OK {
			out.OK, out.BreakAt, out.Reason = false, at, reason
		}
	}

	tenant := tenantOfRecord(sess)
	anchorFail := func(kind string, seq, atIdx int64, reason string) {
		out.AnchorsOK = false
		out.AnchorFailures = append(out.AnchorFailures, anchorFailure{Kind: kind, Seq: seq, AtIdx: atIdx, Reason: reason})
	}
	prev := zeroHash
	for i, fr := range recs {
		idx := fr.Int(colFrIdx)
		if idx != int64(i)+1 {
			fail(int64(i)+1, "idx-gap")
			break
		}
		// The stored prev_hash column is itself evidence: it must equal the
		// running chain value (the zero hash at idx 1) — a forged column is a
		// prev-mismatch even when it never feeds the recomputation.
		storedPrev, ok := decodeHexHash(fr.String(colFrPrevHash))
		if !ok || !equalHash(storedPrev, prev) {
			fail(idx, "prev-mismatch")
			break
		}
		want := frameHash(tenant, id, fieldsOf(fr), prev)
		stored, ok := decodeHexHash(fr.String(colFrHash))
		if !ok || !equalHash(stored, want) {
			fail(idx, "hash-mismatch")
			break
		}
		prev = stored
		out.FramesChecked++

		// Re-check this frame's periodic ledger anchor, if it carries one.
		if aseq := fr.Int(colFrAnchorSeq); aseq > 0 {
			out.AnchorsChecked++
			ev, found, err := ledgerEventAt(ctx, sc, aseq)
			if err != nil {
				return out, err
			}
			switch {
			case !found:
				anchorFail("periodic", aseq, idx, "missing")
			case ev.Action != actionSessionAnchor || ev.TargetID != id:
				anchorFail("periodic", aseq, idx, "wrong-event")
			case !equalHash(ev.PayloadHash, stored):
				anchorFail("periodic", aseq, idx, "tip-mismatch")
			default:
				out.AnchoredThrough = idx
			}
		}
	}
	if int64(len(recs)) != out.Written {
		// Point the reviewer at the FIRST absent frame, not the counter.
		fail(out.FramesChecked+1, "frame-count-mismatch")
	}
	if tip, ok := decodeHexHash(sess.String(colTipHash)); ok {
		out.TipMatch = equalHash(tip, prev)
	} else {
		out.TipMatch = sess.String(colTipHash) == "" && len(recs) == 0
	}
	if !out.TipMatch {
		fail(out.Written, "tip-mismatch")
	}

	// Open and seal anchors. The open anchor is MANDATORY: every session is
	// created with one in the same transaction (openSession), so its absence is
	// itself evidence of a fabricated/incomplete row — never silently skipped.
	oseq := sess.Int(colOpenSeq)
	out.AnchorsChecked++
	if oseq <= 0 {
		anchorFail("open", 0, 0, "missing")
	} else {
		ev, found, err := ledgerEventAt(ctx, sc, oseq)
		if err != nil {
			return out, err
		}
		switch {
		case !found:
			anchorFail("open", oseq, 0, "missing")
		case ev.Action != actionSessionOpen || ev.TargetID != id:
			anchorFail("open", oseq, 0, "wrong-event")
		}
	}
	if sseq := sess.Int(colSealSeq); sseq > 0 {
		out.AnchorsChecked++
		ev, found, err := ledgerEventAt(ctx, sc, sseq)
		if err != nil {
			return out, err
		}
		tip, _ := decodeHexHash(sess.String(colTipHash))
		switch {
		case !found:
			anchorFail("seal", sseq, 0, "missing")
		case ev.Action != actionSessionSeal || ev.TargetID != id:
			anchorFail("seal", sseq, 0, "wrong-event")
		case !equalHash(ev.PayloadHash, tip):
			anchorFail("seal", sseq, 0, "tip-mismatch")
		default:
			out.AnchoredThrough = out.Written // the seal covers the whole trail
		}
	}
	if !out.AnchorsOK {
		fail(0, "anchor-mismatch")
	}
	return out, nil
}

// tenantOfRecord extracts the tenant a stored row belongs to (base column).
func tenantOfRecord(rec model.Record) model.TenantID {
	return model.TenantID(rec.String(model.ColTenantID))
}

// ledgerEventAt reads the single ledger event at seq (found=false when the
// chain has no such event).
func ledgerEventAt(ctx context.Context, sc store.Scope, seq int64) (model.AuditEvent, bool, error) {
	var (
		out   model.AuditEvent
		found bool
	)
	err := sc.Audit().Walk(ctx, seq, func(ev model.AuditEvent) error {
		if ev.Seq == seq {
			out, found = ev, true
		}
		return errStopWalk
	})
	if err != nil && !errors.Is(err, errStopWalk) {
		return model.AuditEvent{}, false, err
	}
	return out, found, nil
}

func equalHash(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// handleSeal closes one ACTIVE session explicitly (admin).
func (m *Module) handleSeal(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	var (
		out       sessionDTO
		clientErr string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colStatus) != statusActive {
			clientErr = "session is already sealed"
			return nil
		}
		if err := m.sealLocked(r.Context(), sc, rec, now, sealReasonClosed, mc.Principal.Actor(), mc.Principal.ActorKind()); err != nil {
			return err
		}
		fresh, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toSessionDTO(fresh)
		return nil
	})
	if clientErr != "" {
		writeJSON(w, http.StatusConflict, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSweep seals every idle ACTIVE session (admin; the lazy-seal safety net
// for credentials that never came back). Attribution follows the NHI-sweep
// precedent: each materialized seal is the SYSTEM actor with its own "sweep"
// reason (it is an expiry materialization, not a targeted intervention by the
// sweeping admin), while the sweep ACTION itself is one aggregate ledger event
// attributed to the real admin.
func (m *Module) handleSweep(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	cfg, err := m.configOf(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	now := m.clock.Now()
	sealed := 0
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colStatus, statusActive))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			last, ok := tsValue(rec, colLastAt)
			if !ok || now.Time().Before(last.Time().Add(time.Duration(cfg.idleSeconds)*time.Second)) {
				continue
			}
			if err := m.sealLocked(r.Context(), sc, rec, now, sealReasonSweep, model.ActorSystem, model.ActorSystem); err != nil {
				return err
			}
			sealed++
		}
		_, err = appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSweep, sessionKind, "", map[string]any{"sealed": sealed}, nil)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sealed": sealed})
}

// transcript bounds for the AI summary input.
const (
	summaryHeadFrames = 200
	summaryTailFrames = 200
)

// handleSummarize produces the optional Claude-backed reviewer summary of a
// session (admin). The summary is a DERIVED artifact — marked as such, stored
// beside the session, never a substitute for the frames. 501 when no
// summarizer is wired (honest posture, mirroring the evals offline judge).
func (m *Module) handleSummarize(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.summarizer == nil {
		writeJSON(w, http.StatusNotImplemented, errorBody("no summarizer configured (wire OLIVARES_CLAUDE_INFERENCE_KEY / ANTHROPIC_API_KEY)"))
		return
	}
	// Deny-closed third-party egress: the transcript is operator-activity
	// metadata leaving the trust boundary, so each tenant opts in explicitly
	// (the docs/SECURITY-HARDENING.md posture, mirroring the egressing-embedder refusal).
	cfg, err := m.configOf(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !cfg.aiSummaries {
		writeJSON(w, http.StatusForbidden, errorBody("AI summaries are disabled for this tenant (recording config ai_summaries; the transcript leaves the trust boundary)"))
		return
	}
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	// Load outside any transaction the summarizer call could hold open: the
	// Claude hop is network I/O and must never sit inside a store Mutate.
	var (
		transcript string
		covered    int64
		tip        string
	)
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		sessions, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := sessions.Get(r.Context(), id)
		if err != nil {
			return err
		}
		// Sealed-only: a summary of an ACTIVE session silently describes a prefix
		// of the eventual trail (and the post-Claude write would race every live
		// frame append). The seal fixes what "the session" means.
		if rec.String(colStatus) != statusSealed {
			return validationErrActiveSummary
		}
		covered, tip = rec.Int(colWritten), rec.String(colTipHash)
		frames, err := sc.Ext(frameKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), frames, eq(colFrSession, id.String()))
		if err != nil {
			return err
		}
		sort.Slice(recs, func(i, j int) bool { return recs[i].Int(colFrIdx) < recs[j].Int(colFrIdx) })
		transcript = buildTranscript(rec, recs)
		return nil
	})
	if errors.Is(err, validationErrActiveSummary) {
		writeJSON(w, http.StatusConflict, errorBody("session is still active; seal it before summarizing (a partial summary would masquerade as the session's)"))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	summary, err := m.summarizer.Summarize(r.Context(), mc.Tenant, transcript)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("summarizer failed; the recording itself is unaffected"))
		return
	}
	// The coverage binding (frames + tip at generation) makes a stale summary
	// detectable instead of trusted.
	meta := map[string]any{"derived": true, "generated_at": m.clock.Now().String(), "source": "claude",
		"frames": covered, "tip_hash": tip}
	metaJSON, _ := json.Marshal(meta)
	var out sessionDTO
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(sessionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		rec[colSummary] = summary
		rec[colSummaryMeta] = string(metaJSON)
		if rec, err = repo.Update(r.Context(), rec); err != nil {
			return err
		}
		out = toSessionDTO(rec)
		_, err = appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionSummarize, sessionKind, id, nil, nil)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// buildTranscript renders the bounded, human-readable frame transcript the
// summarizer receives (head+tail window for very long sessions; frames are
// redacted by construction, so nothing here can carry a secret).
func buildTranscript(sess model.Record, frames []model.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Privileged session %s — subject %s, opened %s, %d actions.\n",
		sess.String(model.ColID), sess.String(colSubject), sess.String(colOpenedAt), sess.Int(colWritten))
	if g := sess.String(colBGGrant); g != "" {
		fmt.Fprintf(&b, "BOUND TO BREAK-GLASS GRANT %s.\n", g)
	}
	writeLine := func(fr model.Record) {
		fmt.Fprintf(&b, "%d %s %s %s %s/%s → %d %s\n",
			fr.Int(colFrIdx), fr.String(colFrAt), fr.String(colFrActor),
			fr.String(colFrMethod), fr.String(colFrNamespace), strings.TrimPrefix(fr.String(colFrPattern), "/"),
			fr.Int(colFrStatus), fr.String(colFrOutcome))
	}
	if len(frames) <= summaryHeadFrames+summaryTailFrames {
		for _, fr := range frames {
			writeLine(fr)
		}
		return b.String()
	}
	for _, fr := range frames[:summaryHeadFrames] {
		writeLine(fr)
	}
	fmt.Fprintf(&b, "… %d actions elided …\n", len(frames)-summaryHeadFrames-summaryTailFrames)
	for _, fr := range frames[len(frames)-summaryTailFrames:] {
		writeLine(fr)
	}
	return b.String()
}

// configDTO is the per-tenant recording policy view. RetentionEnforced is a
// constant false until the retention/legal-hold engine lands: this module
// TAGS sessions (retention_class/retention_days) but never purges — the knob
// must not read as an enforced control before its enforcer exists.
type configDTO struct {
	Namespaces        []string `json:"namespaces"`
	BreakGlassAlways  bool     `json:"breakglass_always"`
	Consent           string   `json:"consent"`
	IdleSeconds       int64    `json:"idle_seconds"`
	RetentionDays     int64    `json:"retention_days"`
	RetentionEnforced bool     `json:"retention_enforced"`
	AISummaries       bool     `json:"ai_summaries"`
}

// isNamespaceShaped mirrors the engine's module-namespace shape (lowercase,
// digits, '-'/'_' inside, ≤32) so a case-mismatched name can never be accepted
// (Gate's lookup is exact-match).
func isNamespaceShaped(s string) bool {
	if len(s) == 0 || len(s) > 32 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	last := s[len(s)-1]
	if last == '-' || last == '_' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func sortedNamespaces(cfg tenantConfig) []string {
	out := make([]string, 0, len(cfg.namespaces))
	for n := range cfg.namespaces {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// handleGetConfig returns the resolved recording policy (defaults applied).
func (m *Module) handleGetConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	cfg, err := m.configOf(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, configDTO{
		Namespaces: sortedNamespaces(cfg), BreakGlassAlways: true,
		Consent: cfg.consent, IdleSeconds: cfg.idleSeconds, RetentionDays: cfg.retentionDays,
		RetentionEnforced: false, AISummaries: cfg.aiSummaries,
	})
}

// putConfigRequest is the recording-policy update body.
type putConfigRequest struct {
	Namespaces    []string `json:"namespaces"`
	Consent       string   `json:"consent"`
	IdleSeconds   int64    `json:"idle_seconds"`
	RetentionDays int64    `json:"retention_days"`
	AISummaries   bool     `json:"ai_summaries,omitempty"`
}

// handlePutConfig replaces the tenant's recording policy (admin; self-audited).
// Minimal scope is the point: a tenant may narrow what is recorded — but the
// break-glass floor is permission-based, not namespace-based, so no config can
// un-record emergency access.
func (m *Module) handlePutConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in putConfigRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Consent != consentNotice && in.Consent != consentRequired {
		writeJSON(w, http.StatusBadRequest, errorBody("consent must be \"notice\" or \"required\""))
		return
	}
	if in.IdleSeconds < 60 || in.IdleSeconds > 24*3600 {
		writeJSON(w, http.StatusBadRequest, errorBody("idle_seconds must be between 60 and 86400"))
		return
	}
	if in.RetentionDays < 1 || in.RetentionDays > 3650 {
		writeJSON(w, http.StatusBadRequest, errorBody("retention_days must be between 1 and 3650"))
		return
	}
	if len(in.Namespaces) > 64 {
		writeJSON(w, http.StatusBadRequest, errorBody("too many namespaces"))
		return
	}
	cleaned := make([]string, 0, len(in.Namespaces))
	seen := map[string]bool{}
	for _, n := range in.Namespaces {
		n = strings.TrimSpace(n)
		if n == "" || len(n) > 32 || seen[n] || !isNamespaceShaped(n) {
			writeJSON(w, http.StatusBadRequest, errorBody("namespaces must be unique lowercase module namespaces (≤32 chars)"))
			return
		}
		// Validate against the namespaces actually mounted (wired by the
		// composition root): a typo here would silently UN-RECORD a privileged
		// surface — fail-open on the watch layer, the one direction this module
		// must never fail.
		if len(m.knownNS) > 0 && !m.knownNS[n] {
			writeJSON(w, http.StatusBadRequest, errorBody("unknown module namespace "+n+"; recorded namespaces must name mounted modules"))
			return
		}
		seen[n] = true
		cleaned = append(cleaned, n)
	}
	sort.Strings(cleaned)
	nsJSON, _ := json.Marshal(cleaned)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(configKind)
		if err != nil {
			return err
		}
		rec, found, err := findOne(r.Context(), repo, eq(colCfgKey, cfgKey))
		if err != nil {
			return err
		}
		if !found {
			rec = model.Record{colCfgKey: cfgKey}
		}
		rec[colCfgNS] = string(nsJSON)
		rec[colCfgConsent] = in.Consent
		rec[colCfgIdleSecs] = in.IdleSeconds
		rec[colCfgRetention] = in.RetentionDays
		rec[colCfgAI] = in.AISummaries
		rec[colCfgUpdatedBy] = mc.Principal.Actor()
		if found {
			_, err = repo.Update(r.Context(), rec)
		} else {
			_, err = repo.Create(r.Context(), rec)
		}
		if err != nil {
			return err
		}
		_, err = appendAudit(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(),
			actionConfigUpdate, configKind, "", map[string]any{
				"namespaces": cleaned, "consent": in.Consent,
				"idle_seconds": in.IdleSeconds, "retention_days": in.RetentionDays,
				"ai_summaries": in.AISummaries,
			}, nil)
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.invalidateConfig(mc.Tenant)
	m.handleGetConfig(w, r, mc)
}
