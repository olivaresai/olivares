// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The health namespace from a terminal: what is up, what broke and when, what
// depends on what, and the checks that decide all three.
//
// THE ONE THING TO UNDERSTAND BEFORE READING THE VERBS: a CHECK is the declared
// subject, and a STATUS is the same row projected through its runtime state
// (modules/health/api.go:66 — "a check IS the status row"). `status` and `checks
// ls` therefore return the same shape from the same table; they differ in which
// filters they offer and in what the operator is asking. `status` asks "what is
// the state of things"; `checks ls` asks "what have we declared we are watching".
//
// TWO CONTRACTS THIS FILE HONORS EXACTLY.
//
//   - PATCH SEMANTICS ON UPDATE. sla_target_ppm is a POINTER on the engine's
//     updateCheckInput, and the comment beside it records why (checks.go:33-37):
//     an int64 could not tell "omitted" from "0" and silently zeroed the SLA
//     target on every partial update. This CLI therefore sends the field ONLY
//     when the operator actually passed --sla-target-ppm, using pflag's Changed.
//     Sending it always would re-introduce the bug the pointer was added to fix.
//   - A REPORT CANNOT POST "unknown". validReportState admits healthy, degraded
//     and down only (checks.go:59-61): "unknown" is a state the system infers
//     from silence, never one a prober may assert.

const healthNS = "health"

func newHealthCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "health",
		Short: "Watch subject health, incidents, SLA and dependencies",
		Long: "health is the reliability plane: the current state of every monitored subject,\n" +
			"the append-only ledger of transitions, the incidents those transitions opened,\n" +
			"the observed dependency graph, and the checks that define what is watched.\n\n" +
			"A CHECK IS THE DECLARED SUBJECT AND A STATUS IS THAT SAME ROW SEEN THROUGH ITS\n" +
			"RUNTIME STATE. `status` and `checks ls` read the same table; they differ in the\n" +
			"question being asked and in the filters each offers.",
		Example: "  olivares health status\n" +
			"  olivares health incidents ls --state open\n" +
			"  olivares health sla --subject-kind agent --subject-ref ag-7\n" +
			"  olivares health checks create --name nightly --subject-kind agent --subject-ref ag-7 --interval 3600",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newHealthStatusCmd(&flags),
		newHealthWatchCmd(&flags),
		newHealthSLACmd(&flags),
		newHealthDependenciesCmd(&flags),
		newHealthEventsCmd(&flags),
		newHealthIncidentsCmd(&flags),
		newHealthChecksCmd(&flags),
	)
	return root
}

// ---- shared DTOs ---------------------------------------------------------------

type healthStatus struct {
	ID                      string `json:"id"`
	Name                    string `json:"name,omitempty"`
	SubjectKind             string `json:"subject_kind"`
	SubjectRef              string `json:"subject_ref"`
	State                   string `json:"state"`
	DesiredStatus           string `json:"desired_status"`
	ExpectedIntervalSeconds int64  `json:"expected_interval_seconds"`
	GraceFactor             int64  `json:"grace_factor"`
	SLATargetPPM            int64  `json:"sla_target_ppm"`
	SLABreachOpen           bool   `json:"sla_breach_open"`
	OwnerActor              string `json:"owner_actor,omitempty"`
	LastCheckedAt           string `json:"last_checked_at,omitempty"`
	LastSeenAt              string `json:"last_seen_at,omitempty"`
	LastLatencyMS           int64  `json:"last_latency_ms"`
	CreatedAt               string `json:"created_at,omitempty"`
}

type healthStatusList struct {
	Items []healthStatus `json:"items"`
	observePage
}

// healthSubjectFilters are the two filters shared by most read verbs here.
type healthSubjectFilters struct {
	subjectKind string
	subjectRef  string
	page        observePageFlags
}

func addHealthSubjectFlags(cmd *cobra.Command, f *healthSubjectFilters, withRef bool) {
	cmd.Flags().StringVar(&f.subjectKind, "subject-kind", "", "filter by subject kind (agent, mcp)")
	if withRef {
		cmd.Flags().StringVar(&f.subjectRef, "subject-ref", "", "filter by subject reference")
	}
	addObservePageFlags(cmd, &f.page)
}

func (f healthSubjectFilters) query() (url.Values, error) {
	q := url.Values{}
	if f.subjectKind != "" {
		q.Set("subject_kind", f.subjectKind)
	}
	if f.subjectRef != "" {
		q.Set("subject_ref", f.subjectRef)
	}
	if err := f.page.apply(q); err != nil {
		return nil, err
	}
	return q, nil
}

// healthSubject renders the kind:ref pair that identifies what is being watched.
func healthSubject(kind, ref string) string {
	if kind == "" {
		return observeCell(ref)
	}
	return observeCell(kind + ":" + ref)
}

func renderHealthStatusList(cmd *cobra.Command, raw []byte, list healthStatusList, empty, cmdPath string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if len(list.Items) == 0 {
			_, err := fmt.Fprintln(w, empty)
			return err
		}
		tw := newTabWriter(w)
		if _, err := fmt.Fprintln(tw, "ID\tSUBJECT\tNAME\tSTATE\tDESIRED\tSLA BREACH\tLATENCY(ms)\tLAST SEEN"); err != nil {
			return err
		}
		for _, s := range list.Items {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				observeCell(s.ID), healthSubject(s.SubjectKind, s.SubjectRef),
				observeCell(s.Name), observeCell(s.State), observeCell(s.DesiredStatus),
				observeBool(s.SLABreachOpen, "OPEN", "no"), s.LastLatencyMS,
				observeCell(s.LastSeenAt)); err != nil {
				return err
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		return observeTruncationNote(w, list.observePage, cmdPath)
	}, observeJSON(raw))
}

// ---- status --------------------------------------------------------------------

func newHealthStatusCmd(flags *authClientFlags) *cobra.Command {
	var f healthSubjectFilters
	var state string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current health of every monitored subject",
		Long: "status is the current state of each monitored subject: healthy, degraded, down,\n" +
			"or unknown when nothing has reported within the expected interval times the\n" +
			"grace factor.\n\n" +
			"UNKNOWN IS NOT DOWN. It means no probe has arrived in time — the subject may be\n" +
			"fine and the prober broken. The engine keeps them distinct and so does this\n" +
			"column.",
		Example: "  olivares health status\n" +
			"  olivares health status --state down\n" +
			"  olivares health status --subject-kind agent -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			if state != "" {
				q.Set("state", state)
			}
			res, err := observeCall{flags: flags, ns: healthNS, method: http.MethodGet, path: "/status", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var list healthStatusList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderHealthStatusList(cmd, res.raw, list,
				"nothing is being monitored yet: declare a subject with `health checks create`",
				"olivares health status")
		},
	}
	addHealthSubjectFlags(cmd, &f, false)
	cmd.Flags().StringVar(&state, "state", "", "filter by state (healthy, degraded, down, unknown)")
	return cmd
}

// ---- watch (SSE) ---------------------------------------------------------------

func newHealthWatchCmd(flags *authClientFlags) *cobra.Command {
	var subjectRef string
	cmd := &cobra.Command{
		Use:     "watch",
		Aliases: []string{"stream"},
		Short:   "Follow health changes as they happen (one JSON object per line)",
		Long: "watch opens the live health stream and prints ONE JSON OBJECT PER LINE, so it\n" +
			"pipes into jq or a log shipper without any parsing of the server-sent-event\n" +
			"framing. Keepalive comments are consumed and never emitted, so a quiet stream\n" +
			"produces no output rather than noise.\n\n" +
			"IT IS NOT BOUNDED BY --timeout, and that is deliberate. http.Client.Timeout\n" +
			"covers reading the BODY, so an ordinary request deadline does not cut a slow\n" +
			"request short — it kills a healthy stream mid-flight. This command therefore\n" +
			"asks the transport for no overall deadline; it runs until you interrupt it or\n" +
			"the server closes the stream. (--timeout is still accepted and still applies to\n" +
			"every other verb in this namespace.)\n\n" +
			"Opening the stream is a privileged, audited read: the engine records who\n" +
			"started watching and what they scoped it to.",
		Example: "  olivares health watch\n" +
			"  olivares health watch --subject-ref ag-7\n" +
			"  olivares health watch | jq -r '.subject_ref + \" \" + .state'",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if subjectRef != "" {
				q.Set("subject_ref", subjectRef)
			}
			return streamHealth(cmd, flags, q)
		},
	}
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "follow one subject instead of every subject in the tenant")
	return cmd
}

// streamHealth consumes the SSE stream and re-emits the data payloads as NDJSON.
//
// It does NOT go through observeCall: that helper reads the whole body into
// memory under a size cap, which is exactly wrong for a stream that is meant to
// stay open. The connection setup is the same — same resolution, same transport,
// same refusals — but the body is consumed incrementally and never buffered.
func streamHealth(cmd *cobra.Command, flags *authClientFlags, q url.Values) error {
	opts, err := flags.resolutionOptions(cmd)
	if err != nil {
		return err
	}
	resolved, err := resolveCLIConfig(opts)
	if err != nil {
		return redactCoded(err, flags.token)
	}
	switch {
	case resolved.Server == "":
		return missingCLIValueError("server", "--server", "OLIVARES_SERVER_URL", resolved)
	case resolved.Token == "":
		return missingCLIValueError("token", "--token", "OLIVARES_TOKEN", resolved)
	case resolved.Tenant == "":
		return missingCLIValueError("tenant", "--tenant", "OLIVARES_TENANT", resolved)
	}
	// Unbounded, NOT Timeout: this is a long-lived stream.
	//
	// The distinction is load-bearing and the transport says why beside the field
	// (clitransport.go:41-51). Timeout==0 does NOT mean "no deadline" here — it
	// means "unspecified" and is replaced by the ten-second default. And
	// http.Client.Timeout covers reading the BODY, so that default does not cut a
	// slow request short: it kills a perfectly healthy SSE attach ten seconds in.
	// Shipped exactly this bug on `agent session attach`, which is why the
	// Unbounded field exists at all. The first draft of this command reproduced
	// it, and told the operator to pass --timeout 0 — a remedy that does nothing.
	// The caller's context, not a deadline, is what ends this stream.
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved, Insecure: flags.insecure, Unbounded: true, Stderr: cmd.ErrOrStderr(),
	})
	if err != nil {
		return exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	target := resolved.Server + observeBase + healthNS + "/stream"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, target, nil)
	if err != nil {
		return exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "text/event-stream")
	resp, err := cliDo(client, req)
	if err != nil {
		return exitcode.Or(exitcode.Server, redactCoded(err, resolved.Token))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		// A refusal arrives as an ordinary bounded body, so it is safe to read
		// and classify exactly like every other verb in this lane.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return observeHTTPError(resp.StatusCode, body)
	}
	out := cmd.OutOrStdout()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		// ":" is an SSE comment — the connected banner and the keepalive pings.
		// "event:" names the type, which NDJSON does not carry. Only "data:"
		// lines hold a payload.
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if _, err := fmt.Fprintln(out, payload); err != nil {
			return exitcode.New(exitcode.Err, fmt.Errorf("write stream line: %w", err))
		}
	}
	if err := scanner.Err(); err != nil {
		// A stream that ENDED is not a stream that failed: the deadline or an
		// interrupt closing it is the ordinary way this command finishes.
		if cmd.Context().Err() != nil {
			return nil
		}
		return exitcode.New(exitcode.Server, fmt.Errorf("health stream ended: %w", err))
	}
	return nil
}

// ---- sla ------------------------------------------------------------------------

type healthSLA struct {
	SubjectKind     string  `json:"subject_kind"`
	SubjectRef      string  `json:"subject_ref"`
	WindowSeconds   int64   `json:"window_seconds"`
	ObservedSeconds int64   `json:"observed_seconds"`
	HasData         bool    `json:"has_data"`
	UptimePPM       int64   `json:"uptime_ppm"`
	UptimePercent   float64 `json:"uptime_percent"`
	DowntimeSeconds int64   `json:"downtime_seconds"`
	DegradedSeconds int64   `json:"degraded_seconds"`
	CurrentState    string  `json:"current_state"`
	HasCheck        bool    `json:"has_check"`
	SLATargetPPM    int64   `json:"sla_target_ppm"`
	Breaching       bool    `json:"breaching"`
}

func newHealthSLACmd(flags *authClientFlags) *cobra.Command {
	var subjectKind, subjectRef string
	var windowSeconds int64
	var strict bool
	cmd := &cobra.Command{
		Use:   "sla",
		Short: "Report observed uptime for one subject against its target",
		Long: "sla reports the uptime actually observed for ONE subject over a window, and\n" +
			"whether that breaches the target its check declares.\n\n" +
			"HAS-DATA IS THE FIELD TO READ FIRST. A subject with no observed history has no\n" +
			"uptime — not 100%, not 0%. The engine judges a breach only when there is\n" +
			"history, and this command refuses to print a percentage that would be an\n" +
			"artifact of an empty window: with no data it says so and exits 8, because a\n" +
			"reliability sweep must not count an unmeasured subject as meeting its SLA.\n\n" +
			"--subject-kind and --subject-ref are both required by the route.",
		Example: "  olivares health sla --subject-kind agent --subject-ref ag-7\n" +
			"  olivares health sla --subject-kind mcp --subject-ref srv-1 --window 86400\n" +
			"  olivares health sla --subject-kind agent --subject-ref ag-7 --strict=false -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if subjectKind != "" {
				q.Set("subject_kind", subjectKind)
			}
			if subjectRef != "" {
				q.Set("subject_ref", subjectRef)
			}
			if err := requireObserveQuery(q, "subject_kind", "--subject-kind"); err != nil {
				return err
			}
			if err := requireObserveQuery(q, "subject_ref", "--subject-ref"); err != nil {
				return err
			}
			if windowSeconds < 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--window must not be negative, got %d", windowSeconds))
			}
			if windowSeconds > 0 {
				q.Set("window_seconds", strconv.FormatInt(windowSeconds, 10))
			}
			res, err := observeCall{flags: flags, ns: healthNS, method: http.MethodGet, path: "/sla", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var s healthSLA
			if err := res.decode(&s); err != nil {
				return err
			}
			if err := renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "subject\t%s\n", healthSubject(s.SubjectKind, s.SubjectRef))
				fmt.Fprintf(tw, "window\t%d s (observed %d s)\n", s.WindowSeconds, s.ObservedSeconds)
				fmt.Fprintf(tw, "current state\t%s\n", observeCell(s.CurrentState))
				if !s.HasData {
					fmt.Fprintf(tw, "uptime\tNO DATA — nothing was observed in this window\n")
				} else {
					fmt.Fprintf(tw, "uptime\t%.4f%% (%d ppm)\n", s.UptimePercent, s.UptimePPM)
					fmt.Fprintf(tw, "downtime\t%d s\n", s.DowntimeSeconds)
					fmt.Fprintf(tw, "degraded\t%d s\n", s.DegradedSeconds)
				}
				if s.HasCheck {
					fmt.Fprintf(tw, "target\t%d ppm\n", s.SLATargetPPM)
					fmt.Fprintf(tw, "breaching\t%s\n", observeBool(s.Breaching, "YES", "no"))
				} else {
					fmt.Fprintf(tw, "target\tno check declares one for this subject\n")
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if !s.HasData {
					_, werr := fmt.Fprintln(w,
						"this is NOT a report of perfect uptime: no observation exists to compute one from")
					return werr
				}
				return nil
			}, observeJSON(res.raw)); err != nil {
				return err
			}
			if strict && !s.HasData {
				return exitcode.New(exitcode.Indeterminate, nil)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "", "the subject's kind: agent or mcp (required)")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "the subject's reference (required)")
	cmd.Flags().Int64Var(&windowSeconds, "window", 0, "window in seconds (0 = the engine's default)")
	cmd.Flags().BoolVar(&strict, "strict", true,
		"exit 8 (indeterminate) when no observation exists in the window; --strict=false exits 0 instead")
	return cmd
}

// ---- dependencies -------------------------------------------------------------------

type healthDepNode struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Health string `json:"health"`
}

type healthDepEdge struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Target        string `json:"target"`
	FromKind      string `json:"from_kind"`
	ToKind        string `json:"to_kind"`
	Relation      string `json:"relation"`
	ObservedCount int64  `json:"observed_count"`
	LastSeenAt    string `json:"last_seen_at"`
}

type healthDepGraph struct {
	Nodes []healthDepNode `json:"nodes"`
	Edges []healthDepEdge `json:"edges"`
	observePage
}

func newHealthDependenciesCmd(flags *authClientFlags) *cobra.Command {
	var page observePageFlags
	cmd := &cobra.Command{
		Use:     "dependencies",
		Aliases: []string{"deps"},
		Short:   "Show the observed dependency graph",
		Long: "dependencies is the graph of what has been observed calling what, with the\n" +
			"current health of each end. It is what turns \"this agent is down\" into \"and\n" +
			"these three things depend on it\".\n\n" +
			"It records what WAS observed. An edge absent here means nothing reported that\n" +
			"call path, not that the dependency cannot exist.",
		Example: "  olivares health dependencies\n" +
			"  olivares health dependencies --limit 100 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: healthNS, method: http.MethodGet, path: "/dependencies", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var g healthDepGraph
			if err := res.decode(&g); err != nil {
				return err
			}
			health := map[string]string{}
			for _, n := range g.Nodes {
				health[n.ID] = n.Health
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(g.Edges) == 0 {
					_, err := fmt.Fprintln(w, "no dependency has been observed yet")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "FROM\tHEALTH\tRELATION\tTO\tHEALTH\tOBSERVED\tLAST SEEN"); err != nil {
					return err
				}
				for _, e := range g.Edges {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
						healthSubject(e.FromKind, e.Source), observeCell(health[e.Source]),
						observeCell(e.Relation),
						healthSubject(e.ToKind, e.Target), observeCell(health[e.Target]),
						e.ObservedCount, observeCell(e.LastSeenAt)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(w, "%d node(s), %d edge(s)\n", len(g.Nodes), len(g.Edges)); err != nil {
					return err
				}
				return observeTruncationNote(w, g.observePage, "olivares health dependencies")
			}, observeJSON(res.raw))
		},
	}
	addObservePageFlags(cmd, &page)
	return cmd
}

// ---- events -------------------------------------------------------------------------

type healthEvent struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	CheckRef    string `json:"check_ref,omitempty"`
	State       string `json:"state"`
	PrevState   string `json:"prev_state"`
	Cause       string `json:"cause"`
	LatencyMS   int64  `json:"latency_ms"`
	OccurredAt  string `json:"occurred_at"`
}

type healthEventList struct {
	Items []healthEvent `json:"items"`
	observePage
}

func newHealthEventsCmd(flags *authClientFlags) *cobra.Command {
	var f healthSubjectFilters
	cmd := &cobra.Command{
		Use:     "events",
		Aliases: []string{"transitions"},
		Short:   "List the append-only reliability transition ledger",
		Long: "events is the append-only ledger of state transitions: every time a subject\n" +
			"changed health, what it changed from and to, and what caused the change.\n\n" +
			"It is the evidence behind an incident. Nothing here is ever edited or deleted,\n" +
			"which is why it can be trusted as the timeline in a post-mortem.",
		Example: "  olivares health events\n" +
			"  olivares health events --subject-kind agent --subject-ref ag-7\n" +
			"  olivares health events --limit 200 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: healthNS, method: http.MethodGet, path: "/events", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var list healthEventList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "no health transition has been recorded for this query")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "OCCURRED\tSUBJECT\tFROM\tTO\tCAUSE\tLATENCY(ms)"); err != nil {
					return err
				}
				for _, e := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n",
						observeCell(e.OccurredAt), healthSubject(e.SubjectKind, e.SubjectRef),
						observeCell(e.PrevState), observeCell(e.State),
						observeCell(e.Cause), e.LatencyMS); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares health events")
			}, observeJSON(res.raw))
		},
	}
	addHealthSubjectFlags(cmd, &f, true)
	return cmd
}

// ---- incidents ------------------------------------------------------------------------

type healthIncident struct {
	ID          string `json:"id"`
	SubjectKind string `json:"subject_kind"`
	SubjectRef  string `json:"subject_ref"`
	CheckRef    string `json:"check_ref,omitempty"`
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	State       string `json:"state"`
	OpenedAt    string `json:"opened_at"`
	ResolvedAt  string `json:"resolved_at,omitempty"`
	Summary     string `json:"summary,omitempty"`
}

type healthIncidentList struct {
	Items []healthIncident `json:"items"`
	observePage
}

func newHealthIncidentsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "incidents",
		Aliases: []string{"incident"},
		Short:   "List, open and resolve health incidents",
		Long: "incidents are opened by the engine when a subject's transitions warrant one, and\n" +
			"closed either by recovery or by an operator declaring them resolved.",
		Example: "  olivares health incidents ls --state open\n" +
			"  olivares health incidents get inc-1\n" +
			"  olivares health incidents resolve inc-1",
	}
	cmd.AddCommand(
		newHealthIncidentsListCmd(flags),
		newHealthIncidentsGetCmd(flags),
		newHealthIncidentsResolveCmd(flags),
	)
	return cmd
}

func renderHealthIncident(w io.Writer, i healthIncident) error {
	tw := newTabWriter(w)
	fmt.Fprintf(tw, "id\t%s\n", observeCell(i.ID))
	fmt.Fprintf(tw, "subject\t%s\n", healthSubject(i.SubjectKind, i.SubjectRef))
	fmt.Fprintf(tw, "kind\t%s\n", observeCell(i.Kind))
	fmt.Fprintf(tw, "severity\t%s\n", observeCell(i.Severity))
	fmt.Fprintf(tw, "state\t%s\n", observeCell(i.State))
	fmt.Fprintf(tw, "opened\t%s\n", observeCell(i.OpenedAt))
	fmt.Fprintf(tw, "resolved\t%s\n", observeCell(i.ResolvedAt))
	fmt.Fprintf(tw, "check\t%s\n", observeCell(i.CheckRef))
	fmt.Fprintf(tw, "summary\t%s\n", observeCell(i.Summary))
	return tw.Flush()
}

func newHealthIncidentsListCmd(flags *authClientFlags) *cobra.Command {
	var f healthSubjectFilters
	var state string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List health incidents",
		Long: "ls lists incidents, optionally narrowed by state and subject. --state open is\n" +
			"the working set: what is broken right now and has not been closed.",
		Example: "  olivares health incidents ls\n" +
			"  olivares health incidents ls --state open\n" +
			"  olivares health incidents ls --subject-kind agent --subject-ref ag-7 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			if state != "" {
				q.Set("state", state)
			}
			res, err := observeCall{flags: flags, ns: healthNS, method: http.MethodGet, path: "/incidents", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var list healthIncidentList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "no incident matches this query")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "ID\tSUBJECT\tKIND\tSEVERITY\tSTATE\tOPENED\tRESOLVED"); err != nil {
					return err
				}
				for _, i := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(i.ID), healthSubject(i.SubjectKind, i.SubjectRef),
						observeCell(i.Kind), observeCell(i.Severity), observeCell(i.State),
						observeCell(i.OpenedAt), observeCell(i.ResolvedAt)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares health incidents ls")
			}, observeJSON(res.raw))
		},
	}
	addHealthSubjectFlags(cmd, &f, true)
	cmd.Flags().StringVar(&state, "state", "", "filter by state (open, resolved)")
	return cmd
}

func newHealthIncidentsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <incident-id>",
		Short:   "Show one incident",
		Long:    "get shows one incident in full, including the check that raised it and the\nsummary of what changed.",
		Example: "  olivares health incidents get inc-1\n  olivares health incidents get inc-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: healthNS, method: http.MethodGet,
				path: "/incidents" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var i healthIncident
			if err := res.decode(&i); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				return renderHealthIncident(w, i)
			}, observeJSON(res.raw))
		},
	}
}

func newHealthIncidentsResolveCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "resolve <incident-id>",
		Short: "Declare an incident resolved",
		Long: "resolve closes an open incident. It is admin-tier and audited.\n\n" +
			"IT IS IDEMPOTENT AND IT DOES NOT FIX ANYTHING. Resolving an already-resolved\n" +
			"incident changes nothing and still succeeds; and closing the record does not\n" +
			"change the subject's health, which is derived from probes. If the subject is\n" +
			"still down, the engine will open another incident.\n\n" +
			"It takes no --yes: closing an incident destroys no data, the transition ledger\n" +
			"keeps the whole history, and a stuck-open incident is a thing an operator needs\n" +
			"to be able to clear from a script.",
		Example: "  olivares health incidents resolve inc-1\n  olivares health incidents resolve inc-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: healthNS, method: http.MethodPost,
				path: "/incidents" + observeIDPath(args[0]) + "/resolve",
			}.do(cmd)
			if err != nil {
				return err
			}
			var i healthIncident
			if err := res.decode(&i); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				// The engine only transitions an incident that was open, and
				// returns the row either way. Reporting "resolved it" over a row
				// that was already resolved would claim an act that did not occur.
				if i.ResolvedAt == "" {
					if _, err := fmt.Fprintf(w,
						"incident %s is in state %q and carries no resolved timestamp — it was NOT closed by this call\n",
						observeCell(i.ID), observeCell(i.State)); err != nil {
						return err
					}
				} else if _, err := fmt.Fprintf(w, "incident %s is resolved (at %s)\n",
					observeCell(i.ID), observeCell(i.ResolvedAt)); err != nil {
					return err
				}
				return renderHealthIncident(w, i)
			}, observeJSON(res.raw))
		},
	}
}

// ---- checks ---------------------------------------------------------------------------

type healthCheckInput struct {
	Name                    string `json:"name,omitempty"`
	SubjectKind             string `json:"subject_kind,omitempty"`
	SubjectRef              string `json:"subject_ref,omitempty"`
	ExpectedIntervalSeconds int64  `json:"expected_interval_seconds"`
	GraceFactor             int64  `json:"grace_factor"`
	// SLATargetPPM is a POINTER and it is sent ONLY when the operator passed the
	// flag. The engine's updateCheckInput made this field a pointer precisely
	// because an int64 could not tell "omitted" from "0" and zeroed the stored
	// SLA target on every partial update (modules/health/checks.go:33-37).
	// Marshaling it unconditionally here would put that bug back on the wire.
	SLATargetPPM  *int64 `json:"sla_target_ppm,omitempty"`
	DesiredStatus string `json:"desired_status,omitempty"`
}

type healthReportInput struct {
	State     string `json:"state"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

// healthReportStates mirrors validReportState (modules/health/checks.go:59-61).
// "unknown" is absent on purpose: it is what the engine INFERS from silence, and
// a prober asserting it would be claiming to have observed an absence.
var healthReportStates = []string{"healthy", "degraded", "down"}

// healthDesiredStatuses mirrors validDesiredStatus (checks.go:53-55).
var healthDesiredStatuses = []string{"active", "paused", "retired"}

// healthSubjectKinds mirrors validSubjectKind (checks.go:50).
var healthSubjectKinds = []string{"agent", "mcp"}

func inList(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func newHealthChecksCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "checks",
		Aliases: []string{"check"},
		Short:   "Declare, inspect, probe and retire health checks",
		Long: "checks are the declarations of what is monitored: a subject, how often it is\n" +
			"expected to report, how much grace before silence counts as unknown, and the\n" +
			"SLA target its uptime is judged against.\n\n" +
			"Declaring and probing are write-tier; deleting is admin-tier.",
		Example: "  olivares health checks ls\n" +
			"  olivares health checks create --name nightly --subject-kind agent --subject-ref ag-7 --interval 3600\n" +
			"  olivares health checks report chk-1 --state healthy --latency 42\n" +
			"  olivares health checks rm chk-1 --yes",
	}
	cmd.AddCommand(
		newHealthChecksListCmd(flags),
		newHealthChecksGetCmd(flags),
		newHealthChecksCreateCmd(flags),
		newHealthChecksUpdateCmd(flags),
		newHealthChecksReportCmd(flags),
		newHealthChecksRemoveCmd(flags),
	)
	return cmd
}

func newHealthChecksListCmd(flags *authClientFlags) *cobra.Command {
	var f healthSubjectFilters
	var desiredStatus string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List declared checks",
		Long: "ls lists what has been DECLARED as monitored, with each check's current runtime\n" +
			"state folded in — a check and its status are the same row seen two ways.\n\n" +
			"--desired-status separates the checks that are meant to be running from those\n" +
			"paused or retired, which is the difference between \"down\" and \"switched off\".",
		Example: "  olivares health checks ls\n" +
			"  olivares health checks ls --desired-status active\n" +
			"  olivares health checks ls --subject-kind mcp -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			if desiredStatus != "" {
				q.Set("desired_status", desiredStatus)
			}
			res, err := observeCall{flags: flags, ns: healthNS, method: http.MethodGet, path: "/checks", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var list healthStatusList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderHealthStatusList(cmd, res.raw, list,
				"no check has been declared: nothing in this tenant is being monitored",
				"olivares health checks ls")
		},
	}
	addHealthSubjectFlags(cmd, &f, false)
	cmd.Flags().StringVar(&desiredStatus, "desired-status", "", "filter by lifecycle status (active, paused, retired)")
	return cmd
}

func renderHealthCheck(cmd *cobra.Command, raw []byte, c healthStatus, headline string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if headline != "" {
			if _, err := fmt.Fprintln(w, headline); err != nil {
				return err
			}
		}
		tw := newTabWriter(w)
		fmt.Fprintf(tw, "id\t%s\n", observeCell(c.ID))
		fmt.Fprintf(tw, "name\t%s\n", observeCell(c.Name))
		fmt.Fprintf(tw, "subject\t%s\n", healthSubject(c.SubjectKind, c.SubjectRef))
		fmt.Fprintf(tw, "state\t%s\n", observeCell(c.State))
		fmt.Fprintf(tw, "desired status\t%s\n", observeCell(c.DesiredStatus))
		fmt.Fprintf(tw, "expected interval\t%d s\n", c.ExpectedIntervalSeconds)
		fmt.Fprintf(tw, "grace factor\t%d\n", c.GraceFactor)
		fmt.Fprintf(tw, "sla target\t%d ppm\n", c.SLATargetPPM)
		fmt.Fprintf(tw, "sla breach open\t%s\n", observeBool(c.SLABreachOpen, "YES", "no"))
		fmt.Fprintf(tw, "owner\t%s\n", observeCell(c.OwnerActor))
		fmt.Fprintf(tw, "last checked\t%s\n", observeCell(c.LastCheckedAt))
		fmt.Fprintf(tw, "last seen\t%s\n", observeCell(c.LastSeenAt))
		fmt.Fprintf(tw, "last latency\t%d ms\n", c.LastLatencyMS)
		return tw.Flush()
	}, observeJSON(raw))
}

func newHealthChecksGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <check-id>",
		Short:   "Show one check",
		Long:    "get shows one check's declaration and its current runtime state together.",
		Example: "  olivares health checks get chk-1\n  olivares health checks get chk-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: healthNS, method: http.MethodGet,
				path: "/checks" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var c healthStatus
			if err := res.decode(&c); err != nil {
				return err
			}
			return renderHealthCheck(cmd, res.raw, c, "")
		},
	}
}

func newHealthChecksCreateCmd(flags *authClientFlags) *cobra.Command {
	var name, subjectKind, subjectRef, desiredStatus string
	var interval, grace, slaTarget int64
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Declare a new monitored subject",
		Long: "create declares a subject to monitor. --interval is how often a probe is\n" +
			"expected; --grace multiplies it before silence is read as unknown.\n\n" +
			"THE SUBJECT IS THE NATURAL KEY, and it cannot be changed afterwards: `update`\n" +
			"changes configuration, never what is being watched. Declaring the same subject\n" +
			"twice conflicts rather than duplicating it.",
		Example: "  olivares health checks create --name nightly --subject-kind agent --subject-ref ag-7 --interval 3600\n" +
			"  olivares health checks create --name mcp-probe --subject-kind mcp --subject-ref srv-1 --interval 60 --grace 3 --sla-target-ppm 999000",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if subjectKind == "" || subjectRef == "" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--subject-kind and --subject-ref are both required: a check is defined by its subject"))
			}
			if !inList(subjectKind, healthSubjectKinds) {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--subject-kind must be one of %v, got %q", healthSubjectKinds, subjectKind))
			}
			if desiredStatus != "" && !inList(desiredStatus, healthDesiredStatuses) {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--desired-status must be one of %v, got %q", healthDesiredStatuses, desiredStatus))
			}
			if interval <= 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--interval must be a positive number of seconds: without an expected interval, silence can never become unknown"))
			}
			if grace < 0 || slaTarget < 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--grace and --sla-target-ppm must not be negative"))
			}
			in := healthCheckInput{
				Name: name, SubjectKind: subjectKind, SubjectRef: subjectRef,
				ExpectedIntervalSeconds: interval, GraceFactor: grace,
				DesiredStatus: desiredStatus,
			}
			if cmd.Flags().Changed("sla-target-ppm") {
				in.SLATargetPPM = &slaTarget
			}
			res, err := observeCall{
				flags: flags, ns: healthNS, method: http.MethodPost, path: "/checks", body: in,
			}.do(cmd)
			if err != nil {
				return err
			}
			var c healthStatus
			if err := res.decode(&c); err != nil {
				return err
			}
			if strings.TrimSpace(c.ID) == "" {
				return exitcode.New(exitcode.Server, fmt.Errorf(
					"the control plane answered HTTP %d but returned no check id, so nothing can be confirmed as declared",
					res.status))
			}
			return renderHealthCheck(cmd, res.raw, c, "declared check "+observeCell(c.ID))
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a human name for the check")
	cmd.Flags().StringVar(&subjectKind, "subject-kind", "", "the subject's kind: agent or mcp (required)")
	cmd.Flags().StringVar(&subjectRef, "subject-ref", "", "the subject's reference (required)")
	cmd.Flags().Int64Var(&interval, "interval", 0, "expected seconds between probes (required, positive)")
	cmd.Flags().Int64Var(&grace, "grace", 0, "multiplier on the interval before silence reads as unknown")
	cmd.Flags().Int64Var(&slaTarget, "sla-target-ppm", 0, "uptime target in parts per million (999000 = 99.9%)")
	cmd.Flags().StringVar(&desiredStatus, "desired-status", "", "lifecycle status: active, paused or retired")
	return cmd
}

func newHealthChecksUpdateCmd(flags *authClientFlags) *cobra.Command {
	var name, desiredStatus string
	var interval, grace, slaTarget int64
	cmd := &cobra.Command{
		Use:   "update <check-id>",
		Short: "Change a check's configuration",
		Long: "update changes a check's name, interval, grace factor, SLA target or lifecycle\n" +
			"status. It never changes the SUBJECT — that is the natural key — and never the\n" +
			"runtime state, which only probes move.\n\n" +
			"--sla-target-ppm IS SENT ONLY IF YOU PASS IT. The engine models this field as\n" +
			"optional precisely so an update that does not mention it keeps the stored\n" +
			"target; sending 0 by default would silently erase every SLA target in the\n" +
			"estate, one partial update at a time.\n\n" +
			"Pausing rather than deleting is usually what you want for a subject that is\n" +
			"temporarily out of service: it keeps the history.",
		Example: "  olivares health checks update chk-1 --interval 600\n" +
			"  olivares health checks update chk-1 --desired-status paused\n" +
			"  olivares health checks update chk-1 --sla-target-ppm 995000 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if desiredStatus != "" && !inList(desiredStatus, healthDesiredStatuses) {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--desired-status must be one of %v, got %q", healthDesiredStatuses, desiredStatus))
			}
			if interval < 0 || grace < 0 || slaTarget < 0 {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--interval, --grace and --sla-target-ppm must not be negative"))
			}
			in := healthCheckInput{
				Name: name, ExpectedIntervalSeconds: interval, GraceFactor: grace,
				DesiredStatus: desiredStatus,
			}
			if cmd.Flags().Changed("sla-target-ppm") {
				in.SLATargetPPM = &slaTarget
			}
			res, err := observeCall{
				flags: flags, ns: healthNS, method: http.MethodPut,
				path: "/checks" + observeIDPath(args[0]), body: in,
			}.do(cmd)
			if err != nil {
				return err
			}
			var c healthStatus
			if err := res.decode(&c); err != nil {
				return err
			}
			return renderHealthCheck(cmd, res.raw, c, "updated check "+observeCell(c.ID))
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "a human name for the check")
	cmd.Flags().Int64Var(&interval, "interval", 0, "expected seconds between probes")
	cmd.Flags().Int64Var(&grace, "grace", 0, "multiplier on the interval before silence reads as unknown")
	cmd.Flags().Int64Var(&slaTarget, "sla-target-ppm", 0,
		"uptime target in parts per million; SENT ONLY IF PASSED, so omitting it keeps the stored target")
	cmd.Flags().StringVar(&desiredStatus, "desired-status", "", "lifecycle status: active, paused or retired")
	return cmd
}

func newHealthChecksReportCmd(flags *authClientFlags) *cobra.Command {
	var state, detail string
	var latency int64
	cmd := &cobra.Command{
		Use:   "report <check-id>",
		Short: "Post a probe result against a check",
		Long: "report posts an active probe result. It is how an external checker, a CI step or\n" +
			"an agent itself tells the control plane what it observed.\n\n" +
			"A REPORT CANNOT POST \"unknown\". Only healthy, degraded and down are\n" +
			"assertable: unknown is what the engine INFERS when nothing reports in time, and\n" +
			"a prober claiming it would be asserting an absence it cannot have observed.\n\n" +
			"--detail is a short, non-sensitive note. The engine stores only its HASH, so it\n" +
			"is useful for correlating identical failures and useless as a place to put\n" +
			"diagnostics you expect to read back.",
		Example: "  olivares health checks report chk-1 --state healthy --latency 42\n" +
			"  olivares health checks report chk-1 --state down --detail \"connection refused\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !inList(state, healthReportStates) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--state must be one of %v, got %q — \"unknown\" is inferred from silence and cannot be reported",
					healthReportStates, state))
			}
			if latency < 0 {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--latency must not be negative, got %d", latency))
			}
			res, err := observeCall{
				flags: flags, ns: healthNS, method: http.MethodPost,
				path: "/checks" + observeIDPath(args[0]) + "/report",
				body: healthReportInput{State: state, LatencyMS: latency, Detail: detail},
			}.do(cmd)
			if err != nil {
				return err
			}
			var c healthStatus
			if err := res.decode(&c); err != nil {
				return err
			}
			return renderHealthCheck(cmd, res.raw, c,
				fmt.Sprintf("reported %s for check %s", observeCell(state), observeCell(args[0])))
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "the observed state: healthy, degraded or down (required)")
	cmd.Flags().Int64Var(&latency, "latency", 0, "observed latency in milliseconds")
	cmd.Flags().StringVar(&detail, "detail", "", "a short, non-sensitive note (the engine stores only its hash)")
	return cmd
}

func newHealthChecksRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <check-id>",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete a check (admin-tier)",
		Long: "rm deletes a check. The subject stops being monitored: no further transitions\n" +
			"are recorded and no incident will be opened for it again.\n\n" +
			"CONSIDER `update --desired-status retired` INSTEAD. Retiring stops the\n" +
			"monitoring and keeps the declaration, so the SLA history stays attributable to\n" +
			"something. A deleted check leaves its past events in the ledger with nothing\n" +
			"left to explain what they were watching.\n\n" +
			"Requires --yes in any non-interactive session.",
		Example: "  olivares health checks rm chk-1 --yes\n  olivares health checks rm chk-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete health check %s, which stops monitoring its subject", args[0])); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: healthNS, method: http.MethodDelete,
				path: "/checks" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w,
					"deleted check %s — its subject is no longer monitored\n", observeCell(args[0]))
				return werr
			}, observeJSON(res.raw))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}
