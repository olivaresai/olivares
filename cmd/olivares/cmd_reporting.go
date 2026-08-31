// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The reporting namespace from a terminal: the report catalog, report generation,
// scheduled runs, tenant branding and custom templates.
//
// ═══ THIS IS THE ONE NAMESPACE IN THE LANE THAT DOES NOT SPEAK JSON THROUGHOUT ═══
//
// Three routes answer with a RENDERED ARTIFACT and not a document:
//
//	GET /reports/{type}               HTML, or PDF with ?format=pdf   (api.go:393)
//	GET /templates/{type}             HTML                            (enterprise.go:307)
//	GET /schedules/{id}/runs/{rid}    the run's artifact when it stored one,
//	                                  and JSON metadata when it did not
//	                                  (enterprise.go:228-253)
//
// and one route TAKES one: PUT /templates/{type} stores a raw HTML template body,
// not a JSON envelope (enterprise.go:312).
//
// So the verbs over those routes do not pretend to render a table. They take
// --out (a path, or `-` for stdout), write the engine's bytes verbatim, and print
// a receipt naming the size and the content type the server actually sent. The
// receipt goes to stderr when the artifact goes to stdout, so a pipe carries the
// document alone.
//
// Writing a PDF into a terminal by default would be a mangled screen, and
// deciding by isatty would make the output shape depend on where it was run —
// which is the kind of contract a script cannot rely on. --out is therefore
// required on those verbs, and says so.
//
// ═══ THE ENTERPRISE BOUNDARY ═══
//
// Everything except GET /reports and GET /reports/{type} is an enterprise seam
// and answers 501 when the add-on is not linked (writeNotWired, enterprise.go:109).
// That is a PRODUCT BOUNDARY, not a fault: observeHTTPError reports it as "not
// wired in this build" rather than "request failed", because an operator reading
// the latter goes hunting for a bug that does not exist.
//
// PAGINATION: none anywhere in this namespace. No route here reads a cursor or a
// limit, so --cursor is deliberately absent; see addObservePageFlags.

const reportingNS = "reporting"

// reportingTypes mirrors the catalog in modules/reporting/api.go:36-68 and the
// constants at types.go:18-22. It is used ONLY to make a typo fail locally with a
// message that names the five values — the engine remains the authority, and
// `reports ls` reads the live catalog rather than this list.
var reportingTypes = []string{
	"compliance-evidence", "audit-summary", "finops-report", "access-review", "executive-summary",
}

func newReportingCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:     "reporting",
		Aliases: []string{"reports"},
		Short:   "Generate reports and manage schedules, branding and templates",
		Long: "reporting generates the governance reports — compliance evidence, audit summary,\n" +
			"FinOps, access review and the executive summary — and manages the schedules,\n" +
			"branding and custom templates around them.\n\n" +
			"REPORTS ARE DOCUMENTS, NOT JSON. `reports get`, `templates get` and a schedule\n" +
			"run's artifact are HTML or PDF, so those verbs write bytes to --out and print a\n" +
			"receipt rather than rendering a table.\n\n" +
			"Everything beyond the catalog and generation is an enterprise capability. In a\n" +
			"build without the add-on those verbs report that they are not wired, which is a\n" +
			"product boundary and not a failure.",
		Example: "  olivares reporting reports ls\n" +
			"  olivares reporting reports get compliance-evidence --out evidence.html\n" +
			"  olivares reporting schedules ls\n" +
			"  olivares reporting branding get -o json",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newReportingReportsCmd(&flags),
		newReportingEnterpriseCmd(&flags),
		newReportingSchedulesCmd(&flags),
		newReportingBrandingCmd(&flags),
		newReportingTemplatesCmd(&flags),
	)
	return root
}

// reportingWindowFlags are the generation parameters GET /reports/{type} accepts.
type reportingWindowFlags struct {
	format    string
	locale    string
	from      string
	to        string
	framework string
	team      string
}

func addReportingWindowFlags(cmd *cobra.Command, f *reportingWindowFlags) {
	cmd.Flags().StringVar(&f.format, "format", "", "html (default) or pdf")
	cmd.Flags().StringVar(&f.locale, "locale", "", "i18n locale for the rendered report (default en)")
	cmd.Flags().StringVar(&f.from, "from", "", "window start: RFC3339 or YYYY-MM-DD")
	cmd.Flags().StringVar(&f.to, "to", "", "window end: RFC3339 or YYYY-MM-DD")
	cmd.Flags().StringVar(&f.framework, "framework", "", "compliance-evidence only: filter by framework")
	cmd.Flags().StringVar(&f.team, "team", "", "finops-report only: filter by team")
}

// query validates locally what the engine would SILENTLY ABSORB.
//
// This is the interesting half. parseTime (api.go:371-382) falls back to a
// DEFAULT WINDOW when it cannot parse a date, and parseReportParams coerces any
// unrecognized format to HTML. Neither refuses. So `--from lastweek` produces a
// perfectly valid report over the wrong period, with nothing anywhere saying so,
// and `--format pdff` silently hands back HTML.
//
// A CLI cannot fix the engine's leniency and must not try — but it can refuse to
// be the thing that hands it an unparseable value. These checks only REJECT; a
// value that parses is forwarded verbatim.
func (f reportingWindowFlags) query() (url.Values, error) {
	q := url.Values{}
	if f.format != "" {
		if f.format != "html" && f.format != "pdf" {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
				"--format must be html or pdf, got %q (the engine treats any other value as html without saying so)", f.format))
		}
		q.Set("format", f.format)
	}
	for _, w := range []struct{ flag, param, val string }{
		{"--from", "from", f.from},
		{"--to", "to", f.to},
	} {
		if w.val == "" {
			continue
		}
		if !validReportDate(w.val) {
			return nil, exitcode.New(exitcode.Usage, fmt.Errorf(
				"%s must be RFC3339 or YYYY-MM-DD, got %q — an unparseable date is SILENTLY replaced by the engine's "+
					"default window, so the report would cover a period you did not ask for", w.flag, w.val))
		}
		q.Set(w.param, w.val)
	}
	for _, kv := range []struct{ k, v string }{
		{"locale", f.locale}, {"framework", f.framework}, {"team", f.team},
	} {
		if kv.v != "" {
			q.Set(kv.k, kv.v)
		}
	}
	return q, nil
}

// validReportDate accepts exactly the two layouts parseTime accepts.
func validReportDate(s string) bool {
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func validReportType(s string) bool { return inList(s, reportingTypes) }

// ---- reports ------------------------------------------------------------------------

type reportingMeta struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Formats     []string `json:"formats"`
}

type reportingCatalog struct {
	Items []reportingMeta `json:"items"`
}

func newReportingReportsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "reports",
		Short:   "List the report catalog and generate a report",
		Long:    "reports lists what can be generated and generates one. These two routes are the\nopen-core surface: they work without the enterprise add-on.",
		Example: "  olivares reporting reports ls\n  olivares reporting reports get audit-summary --out audit.html",
	}
	cmd.AddCommand(newReportingReportsListCmd(flags), newReportingReportsGetCmd(flags))
	return cmd
}

func newReportingReportsListCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the reports this build can generate",
		Long: "ls reads the LIVE catalog from the engine, with each report's supported output\n" +
			"formats. It is the authority on what `reports get` will accept — this command\n" +
			"does not carry its own copy of the list.",
		Example: "  olivares reporting reports ls\n  olivares reporting reports ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: reportingNS, method: http.MethodGet, path: "/reports"}.do(cmd)
			if err != nil {
				return err
			}
			var cat reportingCatalog
			if err := res.decode(&cat); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(cat.Items) == 0 {
					_, err := fmt.Fprintln(w, "this build offers no reports")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "TYPE\tFORMATS\tTITLE"); err != nil {
					return err
				}
				for _, m := range cat.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n",
						observeCell(m.Type), observeCell(observeJoinList(m.Formats)),
						observeCell(m.Title)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

func newReportingReportsGetCmd(flags *authClientFlags) *cobra.Command {
	var f reportingWindowFlags
	var out string
	cmd := &cobra.Command{
		Use:     "get <report-type>",
		Aliases: []string{"generate"},
		Short:   "Generate one report and write it to a file",
		Long: "get generates a report and writes the DOCUMENT — HTML, or PDF with --format pdf.\n\n" +
			"--out IS REQUIRED because the answer is not JSON: pass a path, or `-` for\n" +
			"stdout. With `-` the receipt goes to stderr, so `... --out - > report.html`\n" +
			"gets the document and nothing else.\n\n" +
			"PASS --from AND --to DELIBERATELY. The engine's date parser falls back to its\n" +
			"default window on anything it cannot read, without complaint, so an unparseable\n" +
			"date would produce a valid report over the wrong period. This command refuses\n" +
			"such a value instead of forwarding it.\n\n" +
			"A PDF request against a build with no PDF renderer is refused by the engine as\n" +
			"not implemented; the report is still available as HTML.",
		Example: "  olivares reporting reports get compliance-evidence --out evidence.html\n" +
			"  olivares reporting reports get finops-report --format pdf --from 2026-07-01 --to 2026-08-01 --out spend.pdf\n" +
			"  olivares reporting reports get audit-summary --out - > audit.html",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validReportType(args[0]) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"unknown report type %q: this build offers %v (confirm with `olivares reporting reports ls`)",
					args[0], reportingTypes))
			}
			q, err := f.query()
			if err != nil {
				return err
			}
			// --out is checked BEFORE the request: generating a report is real
			// work for the engine, and doing it only to refuse to write the result
			// wastes it.
			if strings.TrimSpace(out) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--out is required: a report is an HTML or PDF document, not JSON — pass a file path, or `-` for stdout"))
			}
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodGet,
				path: "/reports" + observeIDPath(args[0]), query: q,
				accept: "text/html, application/pdf, application/json;q=0.5",
			}.do(cmd)
			if err != nil {
				return err
			}
			return writeObserveArtifact(cmd, out, res, "the "+args[0]+" report")
		},
	}
	addReportingWindowFlags(cmd, &f)
	observeArtifactFlag(cmd, &out)
	return cmd
}

// ---- enterprise report engine --------------------------------------------------------

func newReportingEnterpriseCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enterprise",
		Short: "Read the enterprise posture, risk and evidence-bundle reports",
		Long: "enterprise exposes the three commercial report-engine views. They answer JSON\n" +
			"rather than a rendered document, and they are add-on capabilities: in a build\n" +
			"without the enterprise report engine each reports that it is not wired.",
		Example: "  olivares reporting enterprise posture\n" +
			"  olivares reporting enterprise risk -o json\n" +
			"  olivares reporting enterprise bundle",
	}
	cmd.AddCommand(
		newReportingEnterpriseViewCmd(flags, "posture", "Enterprise governance posture report",
			"posture is the enterprise report engine's governance posture view."),
		newReportingEnterpriseViewCmd(flags, "risk", "Enterprise risk report",
			"risk is the enterprise report engine's risk view."),
		newReportingEnterpriseViewCmd(flags, "bundle", "Enterprise evidence bundle",
			"bundle is the enterprise evidence bundle: the artifact set an auditor is given."),
	)
	return cmd
}

// newReportingEnterpriseViewCmd builds the three enterprise reads, which differ
// only in route and wording. Their payload shape is the add-on's and is not
// modeled here — it is rendered as the engine sent it, which stays correct when
// the add-on adds a field.
func newReportingEnterpriseViewCmd(flags *authClientFlags, name, short, long string) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		Long: long + "\n\nIt is an ENTERPRISE ADD-ON capability. A build without the commercial report\n" +
			"engine answers that it is not wired — a product boundary, not a fault, and the\n" +
			"rest of this namespace is unaffected.\n\n" +
			"The payload is the add-on's own document and is printed as the engine sent it\n" +
			"rather than flattened into columns, so a field the add-on adds tomorrow appears\n" +
			"without this command changing.",
		Example: "  olivares reporting enterprise " + name + "\n  olivares reporting enterprise " + name + " -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodGet, path: "/enterprise/" + name,
			}.do(cmd)
			if err != nil {
				return err
			}
			return observeValue(cmd, res.raw, "")
		},
	}
}

// ---- schedules ------------------------------------------------------------------------

type reportingSchedule struct {
	ID         string `json:"id"`
	ReportType string `json:"report_type"`
	Format     string `json:"format"`
	Cron       string `json:"cron"`
	Framework  string `json:"framework,omitempty"`
	Team       string `json:"team,omitempty"`
	Locale     string `json:"locale,omitempty"`
	Enabled    bool   `json:"enabled"`
}

type reportingScheduleList struct {
	Items []reportingSchedule `json:"items"`
}

// reportingRun is a schedule execution. Output is deliberately NOT modeled: the
// list route strips it and the single-run route returns the artifact bytes as the
// whole response body rather than as a field.
type reportingRun struct {
	ID         string `json:"id"`
	ScheduleID string `json:"schedule_id"`
	ReportType string `json:"report_type"`
	Format     string `json:"format"`
	RanAt      string `json:"ran_at"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

type reportingRunList struct {
	Items []reportingRun `json:"items"`
}

func newReportingSchedulesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "schedules",
		Aliases: []string{"schedule"},
		Short:   "Manage scheduled reports and read their runs",
		Long: "schedules run a report on a cron cadence and record one run per execution — a\n" +
			"failed render is a RECORDED FACT, never a silent skip, so the run list is the\n" +
			"honest history of what the schedule actually produced.",
		Example: "  olivares reporting schedules ls\n" +
			"  olivares reporting schedules create --report-type audit-summary --cron \"0 6 * * *\"\n" +
			"  olivares reporting schedules runs sch-1\n" +
			"  olivares reporting schedules rm sch-1 --yes",
	}
	cmd.AddCommand(
		newReportingSchedulesListCmd(flags),
		newReportingSchedulesCreateCmd(flags),
		newReportingSchedulesRemoveCmd(flags),
		newReportingSchedulesRunsCmd(flags),
		newReportingSchedulesRunGetCmd(flags),
	)
	return cmd
}

func renderReportingSchedules(cmd *cobra.Command, raw []byte, list reportingScheduleList, headline string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if headline != "" {
			if _, err := fmt.Fprintln(w, headline); err != nil {
				return err
			}
		}
		if len(list.Items) == 0 {
			_, err := fmt.Fprintln(w, "no report schedule is declared")
			return err
		}
		tw := newTabWriter(w)
		if _, err := fmt.Fprintln(tw, "ID\tREPORT\tFORMAT\tCRON\tENABLED\tFRAMEWORK\tTEAM\tLOCALE"); err != nil {
			return err
		}
		for _, s := range list.Items {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				observeCell(s.ID), observeCell(s.ReportType), observeCell(s.Format),
				observeCell(s.Cron), observeBool(s.Enabled, "yes", "NO"),
				observeCell(s.Framework), observeCell(s.Team), observeCell(s.Locale)); err != nil {
				return err
			}
		}
		return tw.Flush()
	}, observeJSON(raw))
}

func newReportingSchedulesListCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List report schedules",
		Long:    "ls lists the tenant's report schedules with their cadence and whether each is\nenabled. A disabled schedule keeps its definition and produces no runs.",
		Example: "  olivares reporting schedules ls\n  olivares reporting schedules ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: reportingNS, method: http.MethodGet, path: "/schedules"}.do(cmd)
			if err != nil {
				return err
			}
			var list reportingScheduleList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderReportingSchedules(cmd, res.raw, list, "")
		},
	}
}

func newReportingSchedulesCreateCmd(flags *authClientFlags) *cobra.Command {
	var reportType, format, cron, framework, team, locale string
	var enabled bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Schedule a report on a cron cadence",
		Long: "create declares a scheduled report. --cron is a standard five-field spec and the\n" +
			"engine parses it before storing, so an invalid one is refused rather than\n" +
			"silently never firing.\n\n" +
			"THE RESPONSE IS THE WHOLE SCHEDULE LIST, not the single row created — that is\n" +
			"the engine's shape (enterprise.go:195), so this command prints the list and\n" +
			"leaves you to spot the new entry rather than inventing an id it was not given.",
		Example: "  olivares reporting schedules create --report-type audit-summary --cron \"0 6 * * *\"\n" +
			"  olivares reporting schedules create --report-type compliance-evidence --cron \"0 0 1 * *\" --format pdf --framework soc2",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !validReportType(reportType) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--report-type must be one of %v, got %q", reportingTypes, reportType))
			}
			if strings.TrimSpace(cron) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--cron is required: a schedule with no cadence would never run"))
			}
			if format != "" && format != "html" && format != "pdf" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--format must be html or pdf, got %q", format))
			}
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodPost, path: "/schedules",
				body: reportingSchedule{
					ReportType: reportType, Format: format, Cron: cron,
					Framework: framework, Team: team, Locale: locale, Enabled: enabled,
				},
			}.do(cmd)
			if err != nil {
				return err
			}
			var list reportingScheduleList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderReportingSchedules(cmd, res.raw, list,
				"schedule accepted — the control plane answers with the full schedule list:")
		},
	}
	cmd.Flags().StringVar(&reportType, "report-type", "", "the report to generate (required)")
	cmd.Flags().StringVar(&format, "format", "", "html (default) or pdf")
	cmd.Flags().StringVar(&cron, "cron", "", "five-field cron spec, e.g. \"0 6 * * *\" (required)")
	cmd.Flags().StringVar(&framework, "framework", "", "compliance-evidence only: filter by framework")
	cmd.Flags().StringVar(&team, "team", "", "finops-report only: filter by team")
	cmd.Flags().StringVar(&locale, "locale", "", "i18n locale for the rendered report")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "whether the schedule may fire")
	return cmd
}

func newReportingSchedulesRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <schedule-id>",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete a report schedule",
		Long: "rm deletes a schedule. The report stops being generated — silently, in the sense\n" +
			"that nothing fails and reports simply stop arriving, which is how a compliance\n" +
			"cadence is discovered to have lapsed at audit time.\n\n" +
			"CONSIDER `create --enabled=false` OR LEAVING IT DISABLED instead, so the\n" +
			"cadence stays declared and visible.\n\n" +
			"Requires --yes in any non-interactive session.",
		Example: "  olivares reporting schedules rm sch-1 --yes\n  olivares reporting schedules rm sch-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete report schedule %s, after which that report stops being generated", args[0])); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodDelete,
				path: "/schedules" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w,
					"deleted schedule %s — that report is no longer generated on a cadence\n", observeCell(args[0]))
				return werr
			}, observeJSON(res.raw))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

func newReportingSchedulesRunsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "runs <schedule-id>",
		Short: "List a schedule's executions",
		Long: "runs lists what a schedule actually produced, one row per execution.\n\n" +
			"A FAILED RENDER IS A RECORDED RUN, not a gap: status `failed` with the reason\n" +
			"beside it. That is what makes this list usable as evidence that a cadence ran —\n" +
			"an empty stretch means the schedule did not fire, not that it fired quietly.\n\n" +
			"The artifacts are NOT in this list; the engine strips them. Fetch one with\n" +
			"`schedules run <schedule-id> <run-id> --out <file>`.",
		Example: "  olivares reporting schedules runs sch-1\n  olivares reporting schedules runs sch-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodGet,
				path: "/schedules" + observeIDPath(args[0]) + "/runs",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list reportingRunList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "this schedule has not run yet")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "RUN\tRAN AT\tREPORT\tFORMAT\tSTATUS\tERROR"); err != nil {
					return err
				}
				failed := 0
				for _, r := range list.Items {
					if r.Status != "ok" {
						failed++
					}
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(r.ID), observeCell(r.RanAt), observeCell(r.ReportType),
						observeCell(r.Format), observeCell(r.Status), observeCell(r.Error)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if failed > 0 {
					_, err := fmt.Fprintf(w, "%d of %d run(s) did NOT produce a report\n", failed, len(list.Items))
					return err
				}
				return nil
			}, observeJSON(res.raw))
		},
	}
}

func newReportingSchedulesRunGetCmd(flags *authClientFlags) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "run <schedule-id> <run-id>",
		Short: "Fetch one run's stored report artifact",
		Long: "run fetches what one execution produced.\n\n" +
			"THIS ROUTE ANSWERS TWO DIFFERENT THINGS and the command handles both: the\n" +
			"stored artifact (HTML or PDF) when the run produced one, and JSON metadata when\n" +
			"it did not — a failed run has no document. The receipt names the content type\n" +
			"the server actually sent, so a script can tell which it got without guessing\n" +
			"from the bytes.\n\n" +
			"--out is required for the same reason as `reports get`: the usual answer is a\n" +
			"document. Pass `-` for stdout.",
		Example: "  olivares reporting schedules run sch-1 run-7 --out run7.html\n" +
			"  olivares reporting schedules run sch-1 run-7 --out - > run7.html",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(out) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--out is required: a completed run answers with an HTML or PDF document — pass a file path, or `-` for stdout"))
			}
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodGet,
				path:   "/schedules" + observeIDPath(args[0]) + "/runs" + observeIDPath(args[1]),
				accept: "text/html, application/pdf, application/json;q=0.5",
			}.do(cmd)
			if err != nil {
				return err
			}
			what := "the report artifact of run " + args[1]
			if res.isJSON() {
				// The run stored no output, so what arrived is metadata. Say so:
				// writing JSON to `run7.pdf` without a word would look like a
				// corrupt report rather than a failed run.
				what = "run " + args[1] + " metadata (this run stored NO report artifact)"
			}
			return writeObserveArtifact(cmd, out, res, what)
		},
	}
	observeArtifactFlag(cmd, &out)
	return cmd
}

// ---- branding --------------------------------------------------------------------------

type reportingBranding struct {
	LogoPath       string `json:"logo_path,omitempty"`
	PrimaryColor   string `json:"primary_color,omitempty"`
	SecondaryColor string `json:"secondary_color,omitempty"`
	FooterText     string `json:"footer_text,omitempty"`
	CompanyName    string `json:"company_name,omitempty"`
}

func newReportingBrandingCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "branding",
		Short:   "Read and set the tenant's report branding",
		Long:    "branding is the logo, colors, company name and footer applied to every rendered\nreport for this tenant.",
		Example: "  olivares reporting branding get\n  olivares reporting branding set --company-name \"Acme\" --primary-color \"#0b5\"",
	}
	cmd.AddCommand(newReportingBrandingGetCmd(flags), newReportingBrandingSetCmd(flags))
	return cmd
}

func renderReportingBranding(cmd *cobra.Command, raw []byte, b reportingBranding, headline string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if headline != "" {
			if _, err := fmt.Fprintln(w, headline); err != nil {
				return err
			}
		}
		tw := newTabWriter(w)
		fmt.Fprintf(tw, "company name\t%s\n", observeCell(b.CompanyName))
		fmt.Fprintf(tw, "logo path\t%s\n", observeCell(b.LogoPath))
		fmt.Fprintf(tw, "primary color\t%s\n", observeCell(b.PrimaryColor))
		fmt.Fprintf(tw, "secondary color\t%s\n", observeCell(b.SecondaryColor))
		fmt.Fprintf(tw, "footer text\t%s\n", observeCell(b.FooterText))
		return tw.Flush()
	}, observeJSON(raw))
}

func newReportingBrandingGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get",
		Short:   "Show the tenant's report branding",
		Long:    "get shows the branding applied to rendered reports. Every field is optional, so\nblanks mean the built-in default is used, not that branding is broken.",
		Example: "  olivares reporting branding get\n  olivares reporting branding get -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: reportingNS, method: http.MethodGet, path: "/branding"}.do(cmd)
			if err != nil {
				return err
			}
			var b reportingBranding
			if err := res.decode(&b); err != nil {
				return err
			}
			return renderReportingBranding(cmd, res.raw, b, "")
		},
	}
}

func newReportingBrandingSetCmd(flags *authClientFlags) *cobra.Command {
	var b reportingBranding
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Replace the tenant's report branding",
		Long: "set REPLACES the whole branding object. It is not a patch: a field you do not\n" +
			"pass is cleared, because the engine stores exactly the document it is sent.\n\n" +
			"Read `branding get -o json` first and pass back everything you want to keep.\n" +
			"Passing no flags at all clears branding entirely, which is a legitimate way to\n" +
			"return to the defaults and a poor way to change one color.",
		Example: "  olivares reporting branding set --company-name \"Acme\" --primary-color \"#0b5fff\"\n" +
			"  olivares reporting branding set --company-name \"Acme\" --footer-text \"Confidential\" -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodPut, path: "/branding", body: b,
			}.do(cmd)
			if err != nil {
				return err
			}
			var stored reportingBranding
			if err := res.decode(&stored); err != nil {
				return err
			}
			return renderReportingBranding(cmd, res.raw, stored,
				"branding replaced — every field not passed is now empty:")
		},
	}
	cmd.Flags().StringVar(&b.CompanyName, "company-name", "", "company name shown on reports")
	cmd.Flags().StringVar(&b.LogoPath, "logo-path", "", "path to the logo the renderer should use")
	cmd.Flags().StringVar(&b.PrimaryColor, "primary-color", "", "primary brand color")
	cmd.Flags().StringVar(&b.SecondaryColor, "secondary-color", "", "secondary brand color")
	cmd.Flags().StringVar(&b.FooterText, "footer-text", "", "footer text for every page")
	return cmd
}

// ---- templates ---------------------------------------------------------------------------

func newReportingTemplatesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "templates",
		Aliases: []string{"template"},
		Short:   "Read, store and remove custom report templates",
		Long: "templates are operator-supplied HTML that replaces a report's built-in rendering\n" +
			"for this tenant. One template per report type.\n\n" +
			"THEY ARE RAW HTML, NOT JSON, in both directions: `get` writes the document to a\n" +
			"file and `set` reads one from a file or stdin.",
		Example: "  olivares reporting templates get audit-summary --out tmpl.html\n" +
			"  olivares reporting templates set audit-summary tmpl.html\n" +
			"  olivares reporting templates rm audit-summary --yes",
	}
	cmd.AddCommand(
		newReportingTemplatesGetCmd(flags),
		newReportingTemplatesSetCmd(flags),
		newReportingTemplatesRemoveCmd(flags),
	)
	return cmd
}

func newReportingTemplatesGetCmd(flags *authClientFlags) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "get <report-type>",
		Short: "Fetch the custom template stored for one report type",
		Long: "get writes the stored HTML template to --out (`-` for stdout).\n\n" +
			"NOT FOUND MEANS NO CUSTOM TEMPLATE IS STORED, and the report renders with its\n" +
			"built-in template. It is not an error state — it is the default state.\n\n" +
			"--out is required: the answer is an HTML document.",
		Example: "  olivares reporting templates get audit-summary --out tmpl.html\n" +
			"  olivares reporting templates get audit-summary --out -",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validReportType(args[0]) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"unknown report type %q: this build offers %v", args[0], reportingTypes))
			}
			if strings.TrimSpace(out) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--out is required: a template is an HTML document — pass a file path, or `-` for stdout"))
			}
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodGet,
				path: "/templates" + observeIDPath(args[0]), accept: "text/html, application/json;q=0.5",
			}.do(cmd)
			if err != nil {
				return err
			}
			return writeObserveArtifact(cmd, out, res, "the "+args[0]+" template")
		},
	}
	observeArtifactFlag(cmd, &out)
	return cmd
}

func newReportingTemplatesSetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "set <report-type> <template-file>",
		Short: "Store a custom HTML template for one report type",
		Long: "set stores an HTML template that replaces the built-in rendering of one report\n" +
			"for this tenant. The file is sent VERBATIM as the request body — this route\n" +
			"takes raw HTML, not a JSON envelope, so what is stored is byte-for-byte what is\n" +
			"in the file.\n\n" +
			"Pass `-` as the file to read the template from stdin.\n\n" +
			"An empty template is refused here rather than round-tripped: the engine rejects\n" +
			"it too, and there is no reading of \"store nothing\" that means anything other\n" +
			"than `templates rm`.",
		Example: "  olivares reporting templates set audit-summary tmpl.html\n" +
			"  cat tmpl.html | olivares reporting templates set audit-summary -",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validReportType(args[0]) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"unknown report type %q: this build offers %v", args[0], reportingTypes))
			}
			doc, err := readObserveDocument(cmd, args[1])
			if err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodPut,
				path: "/templates" + observeIDPath(args[0]),
				// Raw body, not JSON: encoding this as a JSON string would store
				// escaped text where the renderer expects markup.
				rawBody: doc, contentType: "text/html; charset=utf-8",
			}.do(cmd)
			if err != nil {
				return err
			}
			var stored struct {
				Stored     bool   `json:"stored"`
				ReportType string `json:"report_type"`
			}
			if err := res.decode(&stored); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if !stored.Stored {
					// Do not claim a store the engine did not confirm.
					_, werr := fmt.Fprintf(w,
						"the control plane answered HTTP %d but did not confirm the template was stored\n", res.status)
					return werr
				}
				_, werr := fmt.Fprintf(w, "stored a %d-byte custom template for %s\n",
					len(doc), observeCell(firstNonEmptyCLI(stored.ReportType, args[0])))
				return werr
			}, observeJSON(res.raw))
		},
	}
}

func newReportingTemplatesRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <report-type>",
		Aliases: []string{"delete", "remove"},
		Short:   "Remove the custom template for one report type",
		Long: "rm deletes the stored custom template. The report goes back to rendering with its\n" +
			"built-in template — reports keep working, they just look different.\n\n" +
			"The template itself is not recoverable from the control plane afterwards, so\n" +
			"fetch a copy with `templates get --out` first if it is not in version control.\n\n" +
			"Requires --yes in any non-interactive session.",
		Example: "  olivares reporting templates rm audit-summary --yes\n  olivares reporting templates rm audit-summary",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validReportType(args[0]) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"unknown report type %q: this build offers %v", args[0], reportingTypes))
			}
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete the custom %s template, which is not recoverable from the control plane afterwards", args[0])); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: reportingNS, method: http.MethodDelete,
				path: "/templates" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w,
					"removed the custom %s template — that report now renders with its built-in template\n",
					observeCell(args[0]))
				return werr
			}, observeJSON(res.raw))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}
