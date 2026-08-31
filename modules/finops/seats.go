// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// seats.go is the PER-SEAT UTILIZATION surface: assigned (and premium)
// seat denominators per provider/day, joined against the ACTIVE side the module
// already holds — the distinct actors with estimated spend that day (the Claude
// Code Analytics feed emits one per-developer CostSample per day with
// Actor=email, so for Claude "active" means "developer with Claude Code activity
// on the subscription").
//
// The denominators arrive over HTTP (POST /v1/m/finops/seats): the Enterprise
// Analytics summary (connectors/claude-api enterprise.go) carries
// assigned_seat_count/pending_invite_count as an analytics finding, and the
// connector cannot reach module HTTP (the license arrow only runs
// module→connector) — so an operator/automation posts the counts here, the
// sanctioned bridge. Premium seats = the Claude-Code-enabled tier on Claude
// Enterprise; 0 = not reported (the summary schema does not split premium today —
// never inferred).
//
// HONESTY: utilization is active/assigned for the day; a day with no seat row
// has NO utilization (the active count alone is not a percentage), and a
// truncated actor scan is flagged, never silently under-counted.

// seatDay is the UTC day layout for seat rows and query bounds.
const seatDay = "2006-01-02"

// seatIngestRequest is the snake_case DTO for one provider/day seat snapshot.
type seatIngestRequest struct {
	Provider       string `json:"provider"`
	Day            string `json:"day"` // UTC day, YYYY-MM-DD
	AssignedSeats  int64  `json:"assigned_seats"`
	PremiumSeats   int64  `json:"premium_seats,omitempty"`
	PendingInvites int64  `json:"pending_invites,omitempty"`
}

// validate returns a human error for a malformed snapshot ("" = valid).
func (in seatIngestRequest) validate() string {
	if in.Provider == "" {
		return "provider is required"
	}
	if _, err := time.Parse(seatDay, in.Day); err != nil {
		return "day must be a UTC day in YYYY-MM-DD form"
	}
	if in.AssignedSeats < 0 || in.PremiumSeats < 0 || in.PendingInvites < 0 {
		return "seat counts must be >= 0"
	}
	return ""
}

// handleIngestSeats upserts one provider/day seat snapshot (202 Accepted: a
// re-posted day replaces its values — a snapshot, never additive). The
// principal's privileged write is audited atomically with its effect, mirroring
// the cost ingest.
func (m *Module) handleIngestSeats(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in seatIngestRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if _, aerr := sc.Audit().Append(r.Context(), model.AuditDraft{
			Actor:      mc.Principal.Actor(),
			ActorKind:  mc.Principal.ActorKind(),
			Action:     "finops.seats.ingest",
			TargetKind: seatCountKind,
			Meta: map[string]any{
				"provider":        in.Provider,
				"day":             in.Day,
				"assigned_seats":  in.AssignedSeats,
				"premium_seats":   in.PremiumSeats,
				"pending_invites": in.PendingInvites,
			},
		}); aerr != nil {
			return aerr
		}
		return upsertSeatCount(r.Context(), sc, in)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

// upsertSeatCount writes the (provider, day) snapshot, replacing an existing row.
func upsertSeatCount(ctx context.Context, sc store.Scope, in seatIngestRequest) error {
	repo, err := sc.Ext(seatCountKind)
	if err != nil {
		return err
	}
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colSeatProvider, in.Provider), eq(colSeatDay, in.Day)},
		Limit:   1,
	})
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		rec := rows[0]
		rec[colAssignedSeats] = in.AssignedSeats
		rec[colPremiumSeats] = in.PremiumSeats
		rec[colPendingInvites] = in.PendingInvites
		_, err = repo.Update(ctx, rec)
		return err
	}
	_, err = repo.Create(ctx, model.Record{
		colSeatProvider:   in.Provider,
		colSeatDay:        in.Day,
		colAssignedSeats:  in.AssignedSeats,
		colPremiumSeats:   in.PremiumSeats,
		colPendingInvites: in.PendingInvites,
	})
	if errors.Is(err, store.ErrConflict) {
		return nil // raced with a concurrent insert of the same snapshot
	}
	return err
}

// seatUtilizationDay is one day of the utilization view. UtilizationPct /
// PremiumUtilizationPct are 0 when the matching denominator is 0/not reported
// (no fabricated percentage). HasSeats distinguishes "no seat snapshot posted
// for this day" from a real zero-assigned day.
type seatUtilizationDay struct {
	Day                   string  `json:"day"`
	AssignedSeats         int64   `json:"assigned_seats"`
	PremiumSeats          int64   `json:"premium_seats,omitempty"`
	PendingInvites        int64   `json:"pending_invites,omitempty"`
	ActiveActors          int64   `json:"active_actors"`
	UtilizationPct        float64 `json:"utilization_pct"`
	PremiumUtilizationPct float64 `json:"premium_utilization_pct,omitempty"`
	HasSeats              bool    `json:"has_seats"`
}

// seatUtilizationResponse is the GET /seats/utilization payload.
type seatUtilizationResponse struct {
	Provider  string               `json:"provider"`
	From      string               `json:"from"`
	To        string               `json:"to"`
	Days      []seatUtilizationDay `json:"days"`
	Truncated bool                 `json:"truncated,omitempty"`
}

// handleSeatUtilization joins the seat denominators with the per-day distinct
// active actors for a provider over an inclusive [from, to] day range.
func (m *Module) handleSeatUtilization(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := r.URL.Query()
	provider := q.Get("provider")
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("provider is required"))
		return
	}
	from, errFrom := time.Parse(seatDay, q.Get("from"))
	to, errTo := time.Parse(seatDay, q.Get("to"))
	if errFrom != nil || errTo != nil || to.Before(from) {
		writeJSON(w, http.StatusBadRequest, errorBody("from/to must be UTC days (YYYY-MM-DD) with from <= to"))
		return
	}
	var out seatUtilizationResponse
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var e error
		out, e = seatUtilization(r.Context(), sc, provider, from, to)
		return e
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// seatUtilization computes the per-day utilization view: seat snapshots for the
// range plus COUNT(DISTINCT actor) of the provider's ESTIMATED samples per day
// (the billed stream carries no per-actor attribution and would double-count).
// Days appear when they have a seat snapshot OR activity; utilization is only
// computed against a posted denominator.
func seatUtilization(ctx context.Context, sc store.Scope, provider string, from, to time.Time) (seatUtilizationResponse, error) {
	out := seatUtilizationResponse{
		Provider: provider,
		From:     from.Format(seatDay),
		To:       to.Format(seatDay),
		Days:     []seatUtilizationDay{},
	}
	days := map[string]*seatUtilizationDay{}
	day := func(d string) *seatUtilizationDay {
		if got, ok := days[d]; ok {
			return got
		}
		nd := &seatUtilizationDay{Day: d}
		days[d] = nd
		return nd
	}

	// Seat snapshots in range (string day bounds compare lexicographically).
	repo, err := sc.Ext(seatCountKind)
	if err != nil {
		return out, err
	}
	q := model.Query{Filters: []model.Filter{
		eq(colSeatProvider, provider),
		{Column: colSeatDay, Op: model.OpGte, Value: out.From},
		{Column: colSeatDay, Op: model.OpLte, Value: out.To},
	}, Limit: listCap}
	for pages := 0; ; pages++ {
		rows, page, lerr := repo.List(ctx, q)
		if lerr != nil {
			return out, lerr
		}
		for _, rec := range rows {
			d := day(rec.String(colSeatDay))
			d.AssignedSeats = rec.Int(colAssignedSeats)
			d.PremiumSeats = rec.Int(colPremiumSeats)
			d.PendingInvites = rec.Int(colPendingInvites)
			d.HasSeats = true
		}
		if !page.HasMore {
			break
		}
		if pages+1 >= maxScanPages {
			out.Truncated = true
			break
		}
		q.Cursor = page.Cursor
	}

	// Active side: distinct actors per day from the estimated cost read-model.
	// The window is the half-open [from 00:00, to+1d 00:00).
	actorsByDay := map[string]map[string]struct{}{}
	filters := []model.Filter{
		estimatedFilter(),
		eq(colProviderRef, provider),
		{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(from).String()},
		{Column: colOccurredAt, Op: model.OpLt, Value: model.NewTimestamp(to.AddDate(0, 0, 1)).String()},
	}
	trunc, err := scanSamples(ctx, sc, filters, func(r model.Record) {
		actor := r.String(colActor)
		ts := r.String(colOccurredAt)
		if actor == "" || len(ts) < 10 {
			return
		}
		d := ts[:10]
		if actorsByDay[d] == nil {
			actorsByDay[d] = map[string]struct{}{}
		}
		actorsByDay[d][actor] = struct{}{}
	})
	if err != nil {
		return out, err
	}
	out.Truncated = out.Truncated || trunc

	for d, actors := range actorsByDay {
		day(d).ActiveActors = int64(len(actors))
	}
	for _, d := range days {
		if d.HasSeats && d.AssignedSeats > 0 {
			d.UtilizationPct = pct(d.ActiveActors, d.AssignedSeats)
		}
		if d.HasSeats && d.PremiumSeats > 0 {
			d.PremiumUtilizationPct = pct(d.ActiveActors, d.PremiumSeats)
		}
		out.Days = append(out.Days, *d)
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Day < out.Days[j].Day })
	return out, nil
}

// pct returns active/denominator as a percentage rounded to one decimal.
func pct(active, denom int64) float64 {
	return float64(int64(float64(active)/float64(denom)*1000+0.5)) / 10
}
