// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The adoption namespace from a terminal: five read-only views over how the
// estate is actually using Claude.
//
// TWO PROPERTIES OF THIS DATA THAT THE CLI MUST CARRY, NOT SMOOTH OVER.
//
//  1. THE TWO LENSES ARE NEVER SUMMED. `analytics` counts per DEVELOPER from the
//     admin Analytics feed; `telemetry` counts per SESSION from OTLP. They are two
//     vantage points on the same activity, not two halves of it
//     (modules/claudeadoption/api.go:50-53). Adding them double-counts. The
//     summary view therefore prints them as two rows and never a total, and
//     `discrepancy` exists precisely because they disagree.
//
//  2. `developers` EXPOSES PII. It carries the developer's identity and rides a
//     separate, deny-closed permission from the other four
//     (modules/claudeadoption/api.go:47). A caller entitled to the team rollups is
//     NOT thereby entitled to the per-person drill-down, and a 403 from this one
//     verb while the others answer is the system working, not a misconfiguration.
//
// PAGINATION: adoption takes `limit` as a TOP-N and has no cursor at all
// (dto.go:207). --cursor is deliberately absent here; see addObservePageFlags.
// TWO of the five routes read it, with DIFFERENT caps — GET /summary at 10
// (api.go:60) and GET /developers at 100 (api.go:161) — and the namespace-level
// sentence above is exactly what hid the second one: `developers` shipped with
// no --limit because "the namespace takes a top-N" does not say which routes do.

const adoptionNS = "adoption"

func newAdoptionCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "adoption",
		Short: "Report Claude adoption by org, team, trend and developer",
		Long: "adoption reports how the estate is using Claude, over a time window.\n\n" +
			"It has two non-additive lenses and they are never summed: `analytics` counts\n" +
			"per developer from the admin feed, `telemetry` counts per session from OTLP.\n" +
			"They are two vantage points on the same activity, so adding them\n" +
			"double-counts — `discrepancy` is the view that measures how far apart they are.\n\n" +
			"`developers` exposes per-person identity and takes its own, stricter permission:\n" +
			"a caller who can read the team rollups may still, correctly, be refused it.",
		Example: "  olivares adoption summary --since 2026-08-01T00:00:00Z\n" +
			"  olivares adoption teams -o json\n" +
			"  olivares adoption trend --lens telemetry\n" +
			"  olivares adoption discrepancy",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newAdoptionSummaryCmd(&flags),
		newAdoptionTrendCmd(&flags),
		newAdoptionTeamsCmd(&flags),
		newAdoptionDevelopersCmd(&flags),
		newAdoptionDiscrepancyCmd(&flags),
	)
	return root
}

// ---- shared window flags ----------------------------------------------------------

// adoptionWindowFlags are the RFC3339 window every adoption view accepts, plus
// the top-N limit the TWO routes that read it accept (summary and developers).
type adoptionWindowFlags struct {
	since string
	until string
	limit int
}

// addAdoptionWindowFlags declares the window flags, plus --limit ON THE ROUTES
// THAT READ IT and nowhere else.
//
// engineTopN is that route's own default, not the namespace's: limitParam
// (claudeadoption/dto.go:207) is called twice with two different caps — 10 for
// GET /summary (api.go:60) and 100 for GET /developers (api.go:161) — and the
// other three routes never call it. Passing 0 declares no flag.
//
// The default is a PARAMETER rather than a sentence in the help text because the
// first version of this helper carried one hard-coded "10" for both call sites
// it could ever have. A shared string that is true of one caller is how a verb
// comes to document its neighbour's behavior.
func addAdoptionWindowFlags(cmd *cobra.Command, f *adoptionWindowFlags, engineTopN int) {
	cmd.Flags().StringVar(&f.since, "since", "", "window start, RFC3339 (default: the engine's window)")
	cmd.Flags().StringVar(&f.until, "until", "", "window end, RFC3339 (default: now)")
	if engineTopN > 0 {
		cmd.Flags().IntVar(&f.limit, "limit", 0, fmt.Sprintf(
			"top-N rows (0 = the engine's default of %d for this route). NOT a page size: this namespace has no cursor",
			engineTopN))
	}
}

// query validates the window HERE rather than letting the engine answer 400.
//
// The engine parses these as RFC3339 and rejects anything else
// (modules/claudeadoption/api.go:55-58). Parsing them locally turns "2026-08-01"
// — the shape everyone types first — into an exit 2 that names the format, with
// no request sent, instead of an exit 1 carrying a remote parse complaint. It can
// only refuse: a value that parses is passed through verbatim, not reformatted,
// so the engine still sees exactly what the operator typed.
func (f adoptionWindowFlags) query() (url.Values, error) {
	q := url.Values{}
	for _, w := range []struct{ flag, param, val string }{
		{"--since", "since", f.since},
		{"--until", "until", f.until},
	} {
		if w.val == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, w.val); err != nil {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
				"%s must be RFC3339 (for example 2026-08-01T00:00:00Z), got %q", w.flag, w.val))
		}
		q.Set(w.param, w.val)
	}
	if f.limit < 0 {
		return nil, exitcode.New(exitcode.Usage, fmt.Errorf("--limit must not be negative, got %d", f.limit))
	}
	if f.limit > 0 {
		q.Set("limit", strconv.Itoa(f.limit))
	}
	return q, nil
}

// ---- shared DTOs -------------------------------------------------------------------

type adoptionTotals struct {
	Sessions       int64    `json:"sessions"`
	LinesAdded     int64    `json:"lines_added"`
	LinesRemoved   int64    `json:"lines_removed"`
	LinesNet       int64    `json:"lines_net"`
	Commits        int64    `json:"commits"`
	PullRequests   int64    `json:"pull_requests"`
	ToolsAccepted  int64    `json:"tools_accepted"`
	ToolsRejected  int64    `json:"tools_rejected"`
	AcceptanceRate *float64 `json:"acceptance_rate"`
	Tokens         int64    `json:"tokens"`
}

type adoptionLens struct {
	Totals adoptionTotals `json:"totals"`
}

type adoptionBoundary struct {
	ClaudeAPIOnly bool     `json:"claude_api_only"`
	Excludes      []string `json:"excludes"`
}

// adoptionRate renders the acceptance rate, which the engine sends as null when
// there were NO tool decisions at all. Printing 0% there would report perfect
// rejection where the truth is that nothing was asked.
func adoptionRate(v *float64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.1f%%", *v*100)
}

// adoptionBoundaryNote states the measurement boundary under every view. The
// engine attaches it to all five responses because these numbers are Claude-API
// activity only: read as "AI adoption" they would understate every estate that
// uses another provider too.
func adoptionBoundaryNote(w io.Writer, b adoptionBoundary) error {
	if !b.ClaudeAPIOnly && len(b.Excludes) == 0 {
		return nil
	}
	if _, err := fmt.Fprint(w, "boundary: Claude API activity only"); err != nil {
		return err
	}
	if len(b.Excludes) > 0 {
		if _, err := fmt.Fprintf(w, "; excludes %v", b.Excludes); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

// adoptionTruncatedNote says when the aggregation was capped. An adoption figure
// read as a total when it is a floor understates usage, and understated usage is
// the direction that gets a rollout canceled.
func adoptionTruncatedNote(w io.Writer, truncated bool) error {
	if !truncated {
		return nil
	}
	_, err := fmt.Fprintln(w,
		"TRUNCATED: the engine capped this aggregation, so every figure above is a FLOOR, not a total")
	return err
}

func adoptionTotalsRows(tw io.Writer, label string, t adoptionTotals) {
	fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%d\n",
		label, t.Sessions, t.LinesNet, t.Commits, t.PullRequests,
		t.ToolsAccepted+t.ToolsRejected, adoptionRate(t.AcceptanceRate), t.Tokens)
}

const adoptionTotalsHeader = "LENS\tSESSIONS\tLINES(net)\tCOMMITS\tPRS\tTOOL-CALLS\tACCEPTED\tTOKENS"

// ---- summary ------------------------------------------------------------------------

type adoptionSummary struct {
	Since      string           `json:"since,omitempty"`
	Until      string           `json:"until,omitempty"`
	Analytics  adoptionLens     `json:"analytics"`
	Telemetry  adoptionLens     `json:"telemetry"`
	Developers int              `json:"developers"`
	Teams      int              `json:"teams"`
	Boundary   adoptionBoundary `json:"boundary"`
	Truncated  bool             `json:"truncated,omitempty"`
}

func newAdoptionSummaryCmd(flags *authClientFlags) *cobra.Command {
	var f adoptionWindowFlags
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Show both adoption lenses over one window",
		Long: "summary reports the window through both lenses side by side, plus the distinct\n" +
			"developer and team counts.\n\n" +
			"THE TWO ROWS ARE NOT ADDED and this command will not add them. `analytics` is\n" +
			"per developer from the admin feed; `telemetry` is per session from OTLP. They\n" +
			"describe the same activity from two vantage points, so a total would\n" +
			"double-count it.",
		Example: "  olivares adoption summary\n" +
			"  olivares adoption summary --since 2026-08-01T00:00:00Z --until 2026-08-15T00:00:00Z\n" +
			"  olivares adoption summary -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: adoptionNS, method: http.MethodGet, path: "/summary", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var s adoptionSummary
			if err := res.decode(&s); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "window %s .. %s — %d developer(s), %d team(s)\n",
					observeCell(s.Since), observeCell(s.Until), s.Developers, s.Teams); err != nil {
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, adoptionTotalsHeader); err != nil {
					return err
				}
				adoptionTotalsRows(tw, "analytics", s.Analytics.Totals)
				adoptionTotalsRows(tw, "telemetry", s.Telemetry.Totals)
				if err := tw.Flush(); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(w,
					"the two lenses are NOT additive: they are two views of the same activity"); err != nil {
					return err
				}
				if err := adoptionTruncatedNote(w, s.Truncated); err != nil {
					return err
				}
				return adoptionBoundaryNote(w, s.Boundary)
			}, observeJSON(res.raw))
		},
	}
	addAdoptionWindowFlags(cmd, &f, 10)
	return cmd
}

// ---- trend --------------------------------------------------------------------------

type adoptionTrendDay struct {
	Day    string         `json:"day"`
	Totals adoptionTotals `json:"totals"`
}

type adoptionTrend struct {
	Lens      string             `json:"lens"`
	Days      []adoptionTrendDay `json:"days"`
	Boundary  adoptionBoundary   `json:"boundary"`
	Truncated bool               `json:"truncated,omitempty"`
}

func newAdoptionTrendCmd(flags *authClientFlags) *cobra.Command {
	var f adoptionWindowFlags
	var lens string
	cmd := &cobra.Command{
		Use:   "trend",
		Short: "Show a per-day series for ONE lens",
		Long: "trend is a per-day series for a SINGLE lens, because the two lenses are not\n" +
			"additive and a combined series would be meaningless. --lens selects which one;\n" +
			"the engine defaults to analytics.",
		Example: "  olivares adoption trend\n" +
			"  olivares adoption trend --lens telemetry\n" +
			"  olivares adoption trend --since 2026-08-01T00:00:00Z -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Mirrors lensSubjectKind (modules/claudeadoption/api.go:91-95). It
			// only refuses; a valid value goes to the engine untouched.
			switch lens {
			case "", "analytics", "telemetry":
			default:
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--lens must be analytics or telemetry, got %q", lens))
			}
			q, err := f.query()
			if err != nil {
				return err
			}
			if lens != "" {
				q.Set("lens", lens)
			}
			res, err := observeCall{flags: flags, ns: adoptionNS, method: http.MethodGet, path: "/trend", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var t adoptionTrend
			if err := res.decode(&t); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "lens: %s\n", observeCell(t.Lens)); err != nil {
					return err
				}
				if len(t.Days) == 0 {
					if _, err := fmt.Fprintln(w, "no activity recorded in this window"); err != nil {
						return err
					}
					return adoptionBoundaryNote(w, t.Boundary)
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "DAY\tSESSIONS\tLINES(net)\tCOMMITS\tPRS\tACCEPTED\tTOKENS"); err != nil {
					return err
				}
				for _, d := range t.Days {
					if _, err := fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\t%d\n",
						observeCell(d.Day), d.Totals.Sessions, d.Totals.LinesNet,
						d.Totals.Commits, d.Totals.PullRequests,
						adoptionRate(d.Totals.AcceptanceRate), d.Totals.Tokens); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if err := adoptionTruncatedNote(w, t.Truncated); err != nil {
					return err
				}
				return adoptionBoundaryNote(w, t.Boundary)
			}, observeJSON(res.raw))
		},
	}
	addAdoptionWindowFlags(cmd, &f, 0)
	cmd.Flags().StringVar(&lens, "lens", "", "analytics or telemetry (default analytics)")
	return cmd
}

// ---- teams / developers --------------------------------------------------------------

type adoptionTeamRow struct {
	Team   string         `json:"team"`
	Totals adoptionTotals `json:"totals"`
}

type adoptionTeams struct {
	Teams     []adoptionTeamRow `json:"teams"`
	Boundary  adoptionBoundary  `json:"boundary"`
	Truncated bool              `json:"truncated,omitempty"`
}

type adoptionDeveloperRow struct {
	Developer string         `json:"developer"`
	Totals    adoptionTotals `json:"totals"`
}

type adoptionDevelopers struct {
	Developers []adoptionDeveloperRow `json:"developers"`
	Boundary   adoptionBoundary       `json:"boundary"`
	Truncated  bool                   `json:"truncated,omitempty"`
}

func newAdoptionTeamsCmd(flags *authClientFlags) *cobra.Command {
	var f adoptionWindowFlags
	cmd := &cobra.Command{
		Use:   "teams",
		Short: "Break adoption down by team",
		Long: "teams is the per-team rollup over the window, from the telemetry lens. It\n" +
			"carries no per-person identity, which is why it takes the ordinary read\n" +
			"permission rather than the stricter one `developers` requires.\n\n" +
			"An unassigned team arrives as an empty name and is shown as (unassigned)\n" +
			"rather than as a blank row.",
		Example: "  olivares adoption teams\n  olivares adoption teams --since 2026-08-01T00:00:00Z -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: adoptionNS, method: http.MethodGet, path: "/teams", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var t adoptionTeams
			if err := res.decode(&t); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(t.Teams) == 0 {
					if _, err := fmt.Fprintln(w, "no team recorded activity in this window"); err != nil {
						return err
					}
					return adoptionBoundaryNote(w, t.Boundary)
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, adoptionTotalsHeader); err != nil {
					return err
				}
				for _, row := range t.Teams {
					name := row.Team
					if name == "" {
						name = "(unassigned)"
					}
					adoptionTotalsRows(tw, observeCell(name), row.Totals)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if err := adoptionTruncatedNote(w, t.Truncated); err != nil {
					return err
				}
				return adoptionBoundaryNote(w, t.Boundary)
			}, observeJSON(res.raw))
		},
	}
	addAdoptionWindowFlags(cmd, &f, 0)
	return cmd
}

func newAdoptionDevelopersCmd(flags *authClientFlags) *cobra.Command {
	var f adoptionWindowFlags
	cmd := &cobra.Command{
		Use:   "developers",
		Short: "Break adoption down by developer (privileged: exposes identity)",
		Long: "developers is the per-person drill-down, and it is the one view in this\n" +
			"namespace that exposes a developer's identity.\n\n" +
			"IT TAKES ITS OWN, STRICTER PERMISSION. A caller entitled to `summary`, `trend`,\n" +
			"`teams` and `discrepancy` is not thereby entitled to this one: a 403 here while\n" +
			"the other four answer is the deny-closed split working as designed, not a\n" +
			"broken role.\n\n" +
			"THIS IS A TOP-N, NOT A ROSTER. The engine sorts by activity and returns the most\n" +
			"active 100 by default (api.go:161,174-176); a tenant with more developers than\n" +
			"that gets the head of the list. The cut sets NO field in the response — the\n" +
			"`truncated` flag reports the aggregation cap, which is a different limit — so\n" +
			"neither this command nor -o json can tell you it happened. Raise --limit if the\n" +
			"count matters; an under-reported roster is the direction that reads as good news.",
		Example: "  olivares adoption developers\n" +
			"  olivares adoption developers --limit 500\n" +
			"  olivares adoption developers --since 2026-08-01T00:00:00Z -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: adoptionNS, method: http.MethodGet, path: "/developers", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var d adoptionDevelopers
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(d.Developers) == 0 {
					if _, err := fmt.Fprintln(w, "no developer recorded activity in this window"); err != nil {
						return err
					}
					return adoptionBoundaryNote(w, d.Boundary)
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, adoptionTotalsHeader); err != nil {
					return err
				}
				for _, row := range d.Developers {
					adoptionTotalsRows(tw, observeCell(row.Developer), row.Totals)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if err := adoptionTruncatedNote(w, d.Truncated); err != nil {
					return err
				}
				return adoptionBoundaryNote(w, d.Boundary)
			}, observeJSON(res.raw))
		},
	}
	addAdoptionWindowFlags(cmd, &f, 100)
	return cmd
}

// ---- discrepancy -----------------------------------------------------------------------

type adoptionDiscrepancyMetric struct {
	Name      string  `json:"name"`
	Analytics int64   `json:"analytics"`
	Telemetry int64   `json:"telemetry"`
	Ratio     float64 `json:"ratio"`
	Direction string  `json:"direction"`
	Material  bool    `json:"material"`
}

type adoptionDiscrepancyDay struct {
	Day      string                      `json:"day"`
	Metrics  []adoptionDiscrepancyMetric `json:"metrics"`
	Material bool                        `json:"material"`
}

type adoptionDiscrepancy struct {
	Since     string                   `json:"since,omitempty"`
	Until     string                   `json:"until,omitempty"`
	Days      []adoptionDiscrepancyDay `json:"days"`
	Boundary  adoptionBoundary         `json:"boundary"`
	Truncated bool                     `json:"truncated,omitempty"`
}

func newAdoptionDiscrepancyCmd(flags *authClientFlags) *cobra.Command {
	var f adoptionWindowFlags
	cmd := &cobra.Command{
		Use:     "discrepancy",
		Aliases: []string{"discrepancies"},
		Short:   "Measure how far the two lenses disagree",
		Long: "discrepancy is the view that exists BECAUSE the two lenses disagree. For each\n" +
			"day and metric it reports both figures, their ratio, which way the gap runs,\n" +
			"and whether the engine considers it material.\n\n" +
			"A material gap is a data-collection question — a connector not reporting, a\n" +
			"session not attributed — and not a fact about how much Claude was used.",
		Example: "  olivares adoption discrepancy\n" +
			"  olivares adoption discrepancy --since 2026-08-01T00:00:00Z -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: adoptionNS, method: http.MethodGet, path: "/discrepancy", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var d adoptionDiscrepancy
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(d.Days) == 0 {
					if _, err := fmt.Fprintln(w, "no day in this window carried both lenses, so nothing can be compared"); err != nil {
						return err
					}
					return adoptionBoundaryNote(w, d.Boundary)
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "DAY\tMETRIC\tANALYTICS\tTELEMETRY\tRATIO\tDIRECTION\tMATERIAL"); err != nil {
					return err
				}
				material := 0
				for _, day := range d.Days {
					if day.Material {
						material++
					}
					for _, m := range day.Metrics {
						if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%.2f\t%s\t%s\n",
							observeCell(day.Day), observeCell(m.Name), m.Analytics, m.Telemetry,
							m.Ratio, observeCell(m.Direction),
							observeBool(m.Material, "YES", "no")); err != nil {
							return err
						}
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(w, "%d of %d day(s) carry a material discrepancy\n",
					material, len(d.Days)); err != nil {
					return err
				}
				if err := adoptionTruncatedNote(w, d.Truncated); err != nil {
					return err
				}
				return adoptionBoundaryNote(w, d.Boundary)
			}, observeJSON(res.raw))
		},
	}
	addAdoptionWindowFlags(cmd, &f, 0)
	return cmd
}
