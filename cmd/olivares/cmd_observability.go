// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// The observability namespace from a terminal: whether telemetry is arriving,
// what the audit ledger says one trace did, and what the running binary
// measurably is.
//
// A NOTE THIS LANE MUST NOT FLATTEN. Everything under `traces` is derived from
// the AUDIT LEDGER, not from OTel spans (modules/observability/dto.go:77-92).
// duration_ms is the ledger-event window, span status is always "unset" because
// the ledger stores no OTel status, and services is always ["olivares"]. The
// engine refuses to fabricate those fields; this CLI must not present them as
// though it had. Where a column would read as an OTel figure, the help says what
// it actually is.

const observabilityNS = "observability"

func newObservabilityCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:     "observability",
		Aliases: []string{"obs"},
		Short:   "Inspect ingestion health, ledger traces and binary attestation",
		Long: "observability answers three separate questions: is telemetry arriving and under\n" +
			"which standards, what did one correlated trace do, and what is the measured\n" +
			"identity of the running binary.\n\n" +
			"Everything under `traces` is derived from the AUDIT LEDGER rather than from OTel\n" +
			"spans. Durations are the ledger-event window, not span durations, and the span\n" +
			"status is always \"unset\" because the ledger does not store one. The engine\n" +
			"refuses to invent those figures and so does this command.",
		Example: "  olivares observability ingestion-health\n" +
			"  olivares observability traces ls --limit 20\n" +
			"  olivares observability traces get 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  olivares observability attestation -o json",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newObservabilityIngestionCmd(&flags),
		newObservabilityTracesCmd(&flags),
		newObservabilityAttestationCmd(&flags),
	)
	return root
}

// ---- ingestion health --------------------------------------------------------------

type obsIngestionStandard struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	Direction    string `json:"direction"`
	Maturity     string `json:"maturity"`
	Version      string `json:"version"`
	OptInGate    string `json:"opt_in_gate,omitempty"`
	OptInActive  *bool  `json:"opt_in_active,omitempty"`
	RecordsTotal *int64 `json:"records_total,omitempty"`
	LastSeen     string `json:"last_seen,omitempty"`
	Status       string `json:"status"`
}

type obsIngestionSource struct {
	Name         string           `json:"name"`
	RecordsTotal int64            `json:"records_total"`
	FirstSeen    string           `json:"first_seen"`
	LastSeen     string           `json:"last_seen"`
	Kinds        map[string]int64 `json:"kinds"`
}

type obsIngestionHealth struct {
	Standards   []obsIngestionStandard `json:"standards"`
	EngineScope bool                   `json:"engine_scope"`
	Sources     []obsIngestionSource   `json:"sources"`
	Since       string                 `json:"since"`
}

// obsTristate renders a *bool that is deliberately three-valued.
//
// opt_in_active is nil when the gate's state is UNKNOWABLE from inside the
// engine (it lives in per-source connector config), and the engine goes out of
// its way to send nil rather than false (dto.go:36-37). Rendering nil as "no"
// would convert "I cannot see" into "it is off" — the exact confusion exit code
// 8 exists to prevent elsewhere in this CLI.
func obsTristate(v *bool) string {
	if v == nil {
		return "unknown"
	}
	return observeBool(*v, "yes", "no")
}

// obsCount renders a *int64 the engine omits rather than guesses. A missing
// count is "-", never 0: "no records attributable to this standard" and "zero
// records arrived" are different facts.
func obsCount(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

func newObservabilityIngestionCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "ingestion-health",
		Aliases: []string{"ingestion"},
		Short:   "Report per-standard and per-source telemetry ingestion",
		Long: "ingestion-health answers \"is anything actually arriving, and under which\n" +
			"standard\". It has two halves: the standards table (what this build speaks and\n" +
			"whether each profile's opt-in gate is on) and the live per-source counters.\n\n" +
			"THE COUNTERS ARE ENGINE-WIDE, NOT PER-TENANT, and accumulate from the module's\n" +
			"start instant — they reset on restart, exactly like /metrics. An opt-in gate\n" +
			"whose state the engine cannot observe reads `unknown`, never `no`.",
		Example: "  olivares observability ingestion-health\n" +
			"  olivares observability ingestion-health -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: observabilityNS, method: http.MethodGet, path: "/ingestion-health",
			}.do(cmd)
			if err != nil {
				return err
			}
			var h obsIngestionHealth
			if err := res.decode(&h); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "counters accumulate since %s", observeCell(h.Since)); err != nil {
					return err
				}
				if h.EngineScope {
					if _, err := fmt.Fprint(w, " and are ENGINE-WIDE, not scoped to this tenant"); err != nil {
						return err
					}
				}
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "STANDARD\tDIR\tVERSION\tSTATUS\tOPT-IN\tRECORDS\tLAST SEEN"); err != nil {
					return err
				}
				for _, s := range h.Standards {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(firstNonEmptyCLI(s.Label, s.ID)), observeCell(s.Direction),
						observeCell(s.Version), observeCell(s.Status), obsTristate(s.OptInActive),
						obsCount(s.RecordsTotal), observeCell(s.LastSeen)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if len(h.Sources) == 0 {
					_, err := fmt.Fprintln(w, "no source has published an observation since the engine started")
					return err
				}
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
				st := newTabWriter(w)
				if _, err := fmt.Fprintln(st, "SOURCE\tRECORDS\tKINDS\tFIRST SEEN\tLAST SEEN"); err != nil {
					return err
				}
				for _, s := range h.Sources {
					if _, err := fmt.Fprintf(st, "%s\t%d\t%s\t%s\t%s\n",
						observeCell(s.Name), s.RecordsTotal, observeCell(obsKinds(s.Kinds)),
						observeCell(s.FirstSeen), observeCell(s.LastSeen)); err != nil {
						return err
					}
				}
				return st.Flush()
			}, observeJSON(res.raw))
		},
	}
}

// obsKinds renders the per-kind counter map in a stable order. A map ranged
// directly would print a different column every run, which makes a diff between
// two invocations meaningless.
func obsKinds(kinds map[string]int64) string {
	if len(kinds) == 0 {
		return ""
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", k, kinds[k]))
	}
	return strings.Join(parts, " ")
}

// ---- traces ---------------------------------------------------------------------

type obsTraceListItem struct {
	TraceID    string   `json:"trace_id"`
	RootName   string   `json:"root_name"`
	StartedAt  string   `json:"started_at"`
	DurationMS int64    `json:"duration_ms"`
	SpanCount  int      `json:"span_count"`
	AgentCount int      `json:"agent_count"`
	Status     string   `json:"status"`
	Services   []string `json:"services"`
}

type obsTraceList struct {
	Items []obsTraceListItem `json:"items"`
	observePage
}

// obsTraceSpan mirrors traceSpanDTO (modules/observability/dto.go:100).
//
// start_ms is an OFFSET FROM THE TRACE START in milliseconds, not a timestamp —
// the first draft of this file modeled it as `started_at` and would have printed
// an empty column for every span. kind is the constant "ledger" and status the
// constant "unset"; both are honest labels the engine refuses to dress up as
// OTel values, so neither earns a column here.
type obsTraceSpan struct {
	SpanID     string `json:"span_id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	StartMS    int64  `json:"start_ms"`
	DurationMS int64  `json:"duration_ms"`
	Actor      string `json:"actor,omitempty"`
	ActorKind  string `json:"actor_kind,omitempty"`
	EntityRef  string `json:"entity_ref,omitempty"`
}

type obsTraceDetail struct {
	TraceID    string         `json:"trace_id"`
	StartedAt  string         `json:"started_at,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	Spans      []obsTraceSpan `json:"spans"`
}

func newObservabilityTracesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "traces",
		Short: "List, open and export ledger-derived traces",
		Long: "traces correlates audit-ledger events by their trace_id stamp. The LIST is the\n" +
			"shallow read every viewer sees; opening one trace and exporting it are the\n" +
			"deeper read on their own permission, so a stricter role model can withhold the\n" +
			"drill-down without hiding the list.\n\n" +
			"Durations are the ledger-event window (last minus first), NOT OTel span\n" +
			"durations, and the status column is always \"unset\" because the ledger stores no\n" +
			"span status.",
		Example: "  olivares observability traces ls\n" +
			"  olivares observability traces get 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  olivares observability traces export 4bf92f3577b34da6a3ce929d0e0e4736 -o json",
	}
	cmd.AddCommand(
		newObservabilityTracesListCmd(flags),
		newObservabilityTracesGetCmd(flags),
		newObservabilityTracesExportCmd(flags),
	)
	return cmd
}

func newObservabilityTracesListCmd(flags *authClientFlags) *cobra.Command {
	var page observePageFlags
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List correlated traces",
		Long: "ls lists traces newest first. The engine rejects an unusable --limit or\n" +
			"--cursor with a 400 rather than substituting a default, so a paging bug\n" +
			"surfaces as an error instead of as a silently different page.",
		Example: "  olivares observability traces ls\n" +
			"  olivares observability traces ls --limit 20 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: observabilityNS, method: http.MethodGet, path: "/traces", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list obsTraceList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "no traces in the audit ledger for this tenant")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "TRACE\tROOT\tSTARTED\tWINDOW(ms)\tSPANS\tACTORS"); err != nil {
					return err
				}
				for _, it := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\n",
						observeCell(it.TraceID), observeCell(it.RootName), observeCell(it.StartedAt),
						it.DurationMS, it.SpanCount, it.AgentCount); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares observability traces ls")
			}, observeJSON(res.raw))
		},
	}
	addObservePageFlags(cmd, &page)
	return cmd
}

func newObservabilityTracesGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <trace-id>",
		Short: "Show one trace's spans",
		Long: "get opens one trace: one row per distinct span_id, each grouping the ledger\n" +
			"events that engine span produced. An event with no valid span_id still widens\n" +
			"the trace window but yields no row, which is why the span count can be lower\n" +
			"than the number of events behind it.",
		Example: "  olivares observability traces get 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  olivares observability traces get 4bf92f3577b34da6a3ce929d0e0e4736 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: observabilityNS, method: http.MethodGet,
				path: "/traces" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var d obsTraceDetail
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "trace %s started %s, ledger window %d ms, %d span(s)\n",
					observeCell(d.TraceID), observeCell(d.StartedAt), d.DurationMS, len(d.Spans)); err != nil {
					return err
				}
				if len(d.Spans) == 0 {
					_, err := fmt.Fprintln(w, "no span carried a usable span_id in this trace")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "SPAN\tNAME\tACTOR\tENTITY\t+MS\tDUR(ms)"); err != nil {
					return err
				}
				for _, s := range d.Spans {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\n",
						observeCell(s.SpanID), observeCell(s.Name),
						observeCell(s.Actor), observeCell(s.EntityRef),
						s.StartMS, s.DurationMS); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

func newObservabilityTracesExportCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "export <trace-id>",
		Short: "Export one trace as OTLP-compatible JSON",
		Long: "export emits the OTLP/JSON form of one trace, for Jaeger, Tempo or Datadog.\n" +
			"The document is the engine's, not this command's: it is written through\n" +
			"unmodified so that what a collector ingests is byte-for-byte what the control\n" +
			"plane produced.",
		Example: "  olivares observability traces export 4bf92f3577b34da6a3ce929d0e0e4736\n" +
			"  olivares observability traces export 4bf92f3577b34da6a3ce929d0e0e4736 > trace.json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: observabilityNS, method: http.MethodGet,
				path: "/traces" + observeIDPath(args[0]) + "/export",
			}.do(cmd)
			if err != nil {
				return err
			}
			// No table: an OTLP document has no rows worth flattening, and a
			// hand-picked subset would be a different document. Both output modes
			// therefore carry the engine's JSON.
			return observeValue(cmd, res.raw, "")
		},
	}
}

// ---- attestation -----------------------------------------------------------------

type obsAttestation struct {
	Binary struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildDate string `json:"build_date"`
		GoVersion string `json:"go_version"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
		// The wire name is fips140, not fips (modules/observability/dto.go:175).
		FIPS struct {
			Enabled bool   `json:"enabled"`
			Version string `json:"version,omitempty"`
		} `json:"fips140"`
		// SelfSHA256 is the stream hash of the executable the engine is running
		// from. It is OMITTED on error rather than sent empty, so an absent value
		// means "could not be measured", which the note explains.
		SelfSHA256   string `json:"self_sha256,omitempty"`
		SelfHashNote string `json:"self_hash_note,omitempty"`
		Status       string `json:"status"`
	} `json:"binary"`
	CapturedAt string `json:"captured_at"`
}

func newObservabilityAttestationCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "attestation",
		Short: "Show the measured attestation of the running binary",
		Long: "attestation reports what the engine can MEASURE about itself — version, commit,\n" +
			"build date, Go toolchain, platform and FIPS mode — separately from what is only\n" +
			"DECLARED about its pipeline, and separately again from the measured ABSENCE of\n" +
			"a release signature.\n\n" +
			"The three blocks are kept apart on purpose and this command does not merge\n" +
			"them: use -o json to read the release and pipeline blocks in full, which is\n" +
			"where the distinction between measured and declared is visible.",
		Example: "  olivares observability attestation\n  olivares observability attestation -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: observabilityNS, method: http.MethodGet, path: "/attestation",
			}.do(cmd)
			if err != nil {
				return err
			}
			var a obsAttestation
			if err := res.decode(&a); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "version\t%s\n", observeCell(a.Binary.Version))
				fmt.Fprintf(tw, "commit\t%s\n", observeCell(a.Binary.Commit))
				fmt.Fprintf(tw, "built\t%s\n", observeCell(a.Binary.BuildDate))
				fmt.Fprintf(tw, "go\t%s\n", observeCell(a.Binary.GoVersion))
				fmt.Fprintf(tw, "platform\t%s/%s\n", observeCell(a.Binary.OS), observeCell(a.Binary.Arch))
				fmt.Fprintf(tw, "fips mode\t%s\n",
					observeBool(a.Binary.FIPS.Enabled, "on "+a.Binary.FIPS.Version, "off"))
				if a.Binary.SelfSHA256 != "" {
					fmt.Fprintf(tw, "self sha256\t%s\n", observeCell(a.Binary.SelfSHA256))
				} else {
					fmt.Fprintf(tw, "self sha256\tNOT MEASURED (%s)\n", observeCell(a.Binary.SelfHashNote))
				}
				fmt.Fprintf(tw, "captured\t%s\n", observeCell(a.CapturedAt))
				if err := tw.Flush(); err != nil {
					return err
				}
				_, err := fmt.Fprintln(w,
					"the release and pipeline blocks (measured absence vs declared provenance) are in -o json")
				return err
			}, observeJSON(res.raw))
		},
	}
}
