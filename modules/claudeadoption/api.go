// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package claudeadoption

import (
	"net/http"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The module's permissions.
//   - permRead gates the TEAM/ORG aggregate views (summary, trend, teams) — no individual
//     developer is exposed, so it rides the ordinary viewer-read tier (per-team default).
//   - permDeveloperRead gates the per-DEVELOPER drill-down (which exposes the developer
//     email + their productivity). It is a PRIVILEGED read (core/auth privilegedReadPerms):
//     deny-closed for the lowest viewer role, editor and above by default, and an org can
//     scope it further via custom roles —: per-team default, per-developer
//     opt-in gated by permission.
const (
	permRead          auth.Permission = "adoption:metrics:read"
	permDeveloperRead auth.Permission = "adoption:developer:read"
)

// APINamespace returns the module's namespace; routes root at /v1/m/adoption/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the permissions the module's routes require.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{permRead, permDeveloperRead}
}

// APIRoutes mounts the module's routes.
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Team/org adoption views (no per-developer PII): viewer-read tier.
	reg.Handle("GET", "/summary", permRead, m.handleSummary)
	reg.Handle("GET", "/trend", permRead, m.handleTrend)
	reg.Handle("GET", "/teams", permRead, m.handleTeams)
	reg.Handle("GET", "/discrepancy", permRead, m.handleDiscrepancy)
	// Per-developer ROI drill-down (exposes the developer email): deny-closed privileged read.
	reg.Handle("GET", "/developers", permDeveloperRead, m.handleDevelopers)
}

// handleSummary returns BOTH non-additive lenses over the window — the per-developer
// admin Analytics lens and the per-session OTLP telemetry lens — plus distinct developer/
// team counts and the Claude-API-only boundary. The two lenses are never summed (they are
// two vantage points on the same activity).
func (m *Module) handleSummary(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	topN := limitParam(r, 10)
	out := summaryResponse{Boundary: boundary()}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		buckets, trunc, err := aggregateBy(r.Context(), sc, windowFilters(nil, since, hasSince, until, hasUntil), bySubjectKind)
		if err != nil {
			return err
		}
		analytics := bucketOrEmpty(buckets, subjectDeveloper)
		telemetry := bucketOrEmpty(buckets, subjectSession)
		out.Analytics = analytics.lens(topN)
		out.Telemetry = telemetry.lens(topN)
		out.Developers = len(analytics.developers)
		out.Teams = len(telemetry.teams)
		out.Truncated = trunc
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	stampWindow(&out.Since, &out.Until, since, hasSince, until, hasUntil)
	writeJSON(w, http.StatusOK, out)
}

// handleTrend returns a per-day series for ONE lens (default analytics).
func (m *Module) handleTrend(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	lens := r.URL.Query().Get("lens")
	kind, ok := lensSubjectKind(lens)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid lens: expected analytics or telemetry"))
		return
	}
	out := trendResponse{Lens: lensName(kind), Boundary: boundary(), Days: []trendDay{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		filters := windowFilters([]model.Filter{subjectFilter(kind)}, since, hasSince, until, hasUntil)
		buckets, trunc, err := aggregateBy(r.Context(), sc, filters, byDay)
		if err != nil {
			return err
		}
		out.Truncated = trunc
		days := make([]string, 0, len(buckets))
		for d := range buckets {
			days = append(days, d)
		}
		sort.Strings(days)
		for _, d := range days {
			out.Days = append(out.Days, trendDay{Day: d, Totals: buckets[d].totals()})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTeams returns the per-team breakdown from the OTLP telemetry lens (the only lens
// that carries team labels). Rows with no team fold under the empty-team bucket, which the
// UI renders as "(unassigned)".
func (m *Module) handleTeams(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	out := teamsResponse{Boundary: boundary(), Teams: []teamRow{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		filters := windowFilters([]model.Filter{subjectFilter(subjectSession)}, since, hasSince, until, hasUntil)
		buckets, trunc, err := aggregateBy(r.Context(), sc, filters, byTeam)
		if err != nil {
			return err
		}
		out.Truncated = trunc
		for team, a := range buckets {
			out.Teams = append(out.Teams, teamRow{Team: team, Totals: a.totals()})
		}
		sortByActivity(out.Teams, func(t teamRow) (int64, string) { return activityScore(t.Totals), t.Team })
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDevelopers returns the per-developer ROI breakdown from the admin Analytics lens.
// It exposes the developer email, so it is gated by the deny-closed permDeveloperRead. The
// result is capped at the top-N most active developers (default 100).
func (m *Module) handleDevelopers(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	topN := limitParam(r, 100)
	out := developersResponse{Boundary: boundary(), Developers: []developerRow{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		filters := windowFilters([]model.Filter{subjectFilter(subjectDeveloper)}, since, hasSince, until, hasUntil)
		buckets, trunc, err := aggregateBy(r.Context(), sc, filters, bySubjectRef)
		if err != nil {
			return err
		}
		out.Truncated = trunc
		for dev, a := range buckets {
			out.Developers = append(out.Developers, developerRow{Developer: dev, Totals: a.totals()})
		}
		sortByActivity(out.Developers, func(d developerRow) (int64, string) { return activityScore(d.Totals), d.Developer })
		if len(out.Developers) > topN {
			out.Developers = out.Developers[:topN]
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDiscrepancy returns the official-vs-observed daily comparison over the requested
// window. Unlike the ingest-time finding path, it recomputes from adoption.metric at read
// time and never reads the marker table: markers are only the emission dedup guard.
func (m *Module) handleDiscrepancy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	out := discrepancyResponse{
		Days:       []discrepancyDay{},
		Thresholds: discrepancyThresholdDTO(),
		Boundary:   boundary(),
	}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		days, trunc, err := compareDiscrepancyWindow(r.Context(), sc, windowFilters(nil, since, hasSince, until, hasUntil))
		if err != nil {
			return err
		}
		out.Days = days
		out.Truncated = trunc
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	stampWindow(&out.Since, &out.Until, since, hasSince, until, hasUntil)
	writeJSON(w, http.StatusOK, out)
}

// --- grouping keys + helpers -------------------------------------------------

func bySubjectKind(r model.Record) string { return r.String(colSubjectKind) }
func byDay(r model.Record) string         { return r.String(colDay) }
func byTeam(r model.Record) string        { return r.String(colTeam) }
func bySubjectRef(r model.Record) string  { return r.String(colSubjectRef) }

func bucketOrEmpty(buckets map[string]*agg, key string) *agg {
	if b := buckets[key]; b != nil {
		return b
	}
	return newAgg()
}

// lensSubjectKind maps the lens query param to a subject kind. Empty defaults to the
// authoritative analytics (per-developer) lens.
func lensSubjectKind(lens string) (string, bool) {
	switch lens {
	case "", "analytics":
		return subjectDeveloper, true
	case "telemetry":
		return subjectSession, true
	default:
		return "", false
	}
}

func lensName(subjectKind string) string {
	if subjectKind == subjectSession {
		return "telemetry"
	}
	return "analytics"
}

// activityScore is the ranking signal for the team/developer lists: net lines of code +
// commits + PRs (the productivity headline), so the busiest subjects sort first.
func activityScore(t adoptionTotals) int64 {
	return t.LinesNet + t.Commits + t.PullRequests + t.Sessions
}

// sortByActivity sorts a slice descending by an activity score, then ascending by a stable
// tiebreaker name.
func sortByActivity[T any](rows []T, score func(T) (int64, string)) {
	sort.Slice(rows, func(i, j int) bool {
		si, ni := score(rows[i])
		sj, nj := score(rows[j])
		if si != sj {
			return si > sj
		}
		return ni < nj
	})
}

// stampWindow renders the effective since/until back onto a response.
func stampWindow(since, until *string, s time.Time, hasSince bool, u time.Time, hasUntil bool) {
	if hasSince {
		*since = s.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		*until = u.UTC().Format(time.RFC3339)
	}
}
