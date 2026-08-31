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

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The access-map namespace from a terminal: who can reach what, where observed
// access has drifted from what was granted, and which chains an attacker could
// walk. Seven routes, all GET, all privileged and all self-audited engine-side
// (modules/access-map/module.go:151-161) — asking these questions is itself
// recorded, which is why the CLI adds no local caching or replay.

const accessMapNS = "accessmap"

// accessMapEdgeFilters are the edge columns the graph and drift views accept.
// They mirror edgeFilterColumns (modules/access-map/api.go:19) exactly; the
// store rejects an unlisted column, so a typo is caught before SQL is built.
// Declaring them once here keeps `graph` and `drift` from drifting apart.
var accessMapEdgeFilters = []struct {
	flag  string
	param string
	help  string
}{
	{"origin-kind", "origin_kind", "filter by origin kind (agent, session, identity)"},
	{"origin-id", "origin_id", "filter by origin id"},
	{"resource-id", "resource_id", "filter by resource id"},
	{"mode", "mode", "filter by access mode (r, rw)"},
	{"confidence", "confidence", "filter by attribution confidence"},
	{"signal-source", "signal_source", "filter by the signal that produced the edge"},
}

type accessMapFilterFlags struct {
	values map[string]*string
	page   observePageFlags
}

func addAccessMapFilterFlags(cmd *cobra.Command, f *accessMapFilterFlags) {
	f.values = make(map[string]*string, len(accessMapEdgeFilters))
	for _, col := range accessMapEdgeFilters {
		v := new(string)
		cmd.Flags().StringVar(v, col.flag, "", col.help)
		f.values[col.param] = v
	}
	addObservePageFlags(cmd, &f.page)
}

func (f accessMapFilterFlags) query() (url.Values, error) {
	q := url.Values{}
	for param, v := range f.values {
		if v != nil && *v != "" {
			q.Set(param, *v)
		}
	}
	if err := f.page.apply(q); err != nil {
		return nil, err
	}
	return q, nil
}

func newAccessMapCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:     "accessmap",
		Aliases: []string{"access-map"},
		Short:   "Query the access graph, least-privilege drift and attack paths",
		Long: "accessmap answers reachability questions over the observed access graph:\n" +
			"which origins touch which resources, where OBSERVED access has diverged from\n" +
			"what was PERMITTED, and which chains an attacker could walk from an agent to\n" +
			"a sensitive resource.\n\n" +
			"Every verb here is a read, and every read is audited by the engine before it\n" +
			"answers — asking is itself a recorded event, so these are not free queries to\n" +
			"run in a loop.",
		Example: "  olivares accessmap graph --origin-kind agent\n" +
			"  olivares accessmap drift -o json\n" +
			"  olivares accessmap attack-paths summary\n" +
			"  olivares accessmap attack-paths escalation --agent-id ag-7",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newAccessMapGraphCmd(&flags),
		newAccessMapNeighborsCmd(&flags),
		newAccessMapDriftCmd(&flags),
		newAccessMapAttackPathsCmd(&flags),
	)
	return root
}

// ---- graph ----------------------------------------------------------------------

type accessMapNode struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

type accessMapEdge struct {
	ID           string `json:"id"`
	OriginKind   string `json:"origin_kind"`
	OriginID     string `json:"origin_id"`
	OriginRef    string `json:"origin_ref,omitempty"`
	ResourceID   string `json:"resource_id"`
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceRef  string `json:"resource_ref,omitempty"`
	ToolRef      string `json:"tool_ref,omitempty"`
	Mode         string `json:"mode"`
	SignalSource string `json:"signal_source"`
	Confidence   string `json:"confidence"`
	Bridged      bool   `json:"bridged"`
	CoverageTier string `json:"coverage_tier,omitempty"`
	// AttributionTier is the honest firmness of the origin→identity attribution
	// (firm / approximate / unknown). The engine's DTO carries a standing
	// instruction beside it — "the UI must NOT render approximate/unknown as if it
	// were firm" (modules/access-map/dto.go:36-40) — so it gets its own column
	// rather than being folded into CONF, which measures a different thing.
	AttributionTier string `json:"attribution_tier,omitempty"`
	Observed        bool   `json:"observed"`
	Permitted       bool   `json:"permitted"`
	OccurrenceCount int64  `json:"occurrence_count"`
	FirstSeen       string `json:"first_seen"`
	LastSeen        string `json:"last_seen"`
}

type accessMapGraph struct {
	Nodes []accessMapNode `json:"nodes"`
	Edges []accessMapEdge `json:"edges"`
	observePage
}

// accessMapEndpoint renders the resource side of an edge: the human ref when the
// engine redacted one in, the id otherwise. Printing a bare id when a ref exists
// makes an operator go look the id up; printing a ref that is absent would be an
// invention.
func accessMapEndpoint(ref, id string) string {
	if ref != "" {
		return observeCell(ref)
	}
	return observeCell(id)
}

func renderAccessMapGraph(cmd *cobra.Command, raw []byte, g accessMapGraph, empty, cmdPath string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if len(g.Edges) == 0 {
			_, err := fmt.Fprintln(w, empty)
			return err
		}
		tw := newTabWriter(w)
		if _, err := fmt.Fprintln(tw, "ORIGIN\tKIND\tRESOURCE\tMODE\tCONF\tATTRIB\tSIGNAL\tSEEN\tLAST"); err != nil {
			return err
		}
		for _, e := range g.Edges {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
				accessMapEndpoint(e.OriginRef, e.OriginID), observeCell(e.OriginKind),
				accessMapEndpoint(e.ResourceRef, e.ResourceID), observeCell(e.Mode),
				observeCell(e.Confidence), observeCell(e.AttributionTier),
				observeCell(e.SignalSource),
				e.OccurrenceCount, observeCell(e.LastSeen)); err != nil {
				return err
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "%d node(s), %d edge(s)\n", len(g.Nodes), len(g.Edges)); err != nil {
			return err
		}
		return observeTruncationNote(w, g.observePage, cmdPath)
	}, observeJSON(raw))
}

func newAccessMapGraphCmd(flags *authClientFlags) *cobra.Command {
	var f accessMapFilterFlags
	cmd := &cobra.Command{
		Use:   "graph",
		Short: "List the access graph as nodes and edges",
		Long: "graph returns the observed access graph: one edge per origin→resource pair the\n" +
			"engine has evidence for, with the signal that produced it and how confident the\n" +
			"attribution is. Filters narrow it by any supported edge column; unlisted columns\n" +
			"are rejected by the store rather than silently ignored.",
		Example: "  olivares accessmap graph\n" +
			"  olivares accessmap graph --origin-kind agent --mode rw\n" +
			"  olivares accessmap graph --limit 50 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: accessMapNS, method: http.MethodGet, path: "/graph", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var g accessMapGraph
			if err := res.decode(&g); err != nil {
				return err
			}
			return renderAccessMapGraph(cmd, res.raw, g, "no access edges match this query", "olivares accessmap graph")
		},
	}
	addAccessMapFilterFlags(cmd, &f)
	return cmd
}

// ---- neighbors ------------------------------------------------------------------

func newAccessMapNeighborsCmd(flags *authClientFlags) *cobra.Command {
	var id, kind, direction string
	cmd := &cobra.Command{
		Use:   "neighbors",
		Short: "List the edges touching one node",
		Long: "neighbors returns the edges incident to a single node, in a direction. It is the\n" +
			"drill-down for a node found in `graph`: what does this agent reach, or who\n" +
			"reaches this resource.\n\n" +
			"--id is required by the route; passing it is checked here so a call that could\n" +
			"not succeed never leaves the machine.",
		Example: "  olivares accessmap neighbors --id ag-7\n" +
			"  olivares accessmap neighbors --id res-3 --direction incoming -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch direction {
			case "", "both", "outgoing", "incoming":
			default:
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("--direction must be one of outgoing, incoming, both (got %q)", direction))
			}
			q := url.Values{}
			if id != "" {
				q.Set("id", id)
			}
			if err := requireObserveQuery(q, "id", "--id"); err != nil {
				return err
			}
			if kind != "" {
				q.Set("kind", kind)
			}
			if direction != "" {
				q.Set("direction", direction)
			}
			res, err := observeCall{flags: flags, ns: accessMapNS, method: http.MethodGet, path: "/neighbors", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var g accessMapGraph
			if err := res.decode(&g); err != nil {
				return err
			}
			return renderAccessMapGraph(cmd, res.raw, g, "no edges touch this node", "olivares accessmap neighbors")
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "node id to expand (required)")
	cmd.Flags().StringVar(&kind, "kind", "", "node kind, when the id alone is ambiguous")
	cmd.Flags().StringVar(&direction, "direction", "", "outgoing, incoming or both (default both)")
	return cmd
}

// ---- drift ----------------------------------------------------------------------

type accessMapDriftRow struct {
	Kind    string        `json:"kind"`
	Pending bool          `json:"reconciliation_pending,omitempty"`
	Edge    accessMapEdge `json:"edge"`
}

type accessMapDrift struct {
	UnexpectedAccesses []accessMapDriftRow `json:"unexpected_accesses"`
	UnusedGrants       []accessMapDriftRow `json:"unused_grants"`
	UnexpectedCount    int                 `json:"unexpected_count"`
	UnusedCount        int                 `json:"unused_count"`
	InventoryCount     int                 `json:"inventory_count"`
	Truncated          bool                `json:"truncated,omitempty"`
}

func newAccessMapDriftCmd(flags *authClientFlags) *cobra.Command {
	var f accessMapFilterFlags
	cmd := &cobra.Command{
		Use:   "drift",
		Short: "Show permitted-vs-observed least-privilege drift",
		Long: "drift is the reconciliation between what was GRANTED and what was OBSERVED.\n" +
			"Unexpected accesses — observed but never permitted — are the headline; unused\n" +
			"grants are the cleanup list.\n\n" +
			"A truncated answer is reported as such rather than presented as complete: an\n" +
			"under-count of unexpected access reads as good news, which is the one direction\n" +
			"this command must never fail in.",
		Example: "  olivares accessmap drift\n" +
			"  olivares accessmap drift --origin-kind agent -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := f.query()
			if err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: accessMapNS, method: http.MethodGet, path: "/drift", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var d accessMapDrift
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w,
					"%d unexpected access(es), %d unused grant(s), %d inventoried grant(s)\n",
					d.UnexpectedCount, d.UnusedCount, d.InventoryCount); err != nil {
					return err
				}
				if d.Truncated {
					if _, err := fmt.Fprintln(w,
						"TRUNCATED: the engine capped this reconciliation, so the counts above are a FLOOR, not a total"); err != nil {
						return err
					}
				}
				if len(d.UnexpectedAccesses) == 0 && len(d.UnusedGrants) == 0 {
					_, err := fmt.Fprintln(w, "no drift rows returned")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "DRIFT\tORIGIN\tRESOURCE\tMODE\tCONF\tPENDING\tLAST"); err != nil {
					return err
				}
				rows := append(append([]accessMapDriftRow{}, d.UnexpectedAccesses...), d.UnusedGrants...)
				for _, r := range rows {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(r.Kind),
						accessMapEndpoint(r.Edge.OriginRef, r.Edge.OriginID),
						accessMapEndpoint(r.Edge.ResourceRef, r.Edge.ResourceID),
						observeCell(r.Edge.Mode), observeCell(r.Edge.Confidence),
						observeBool(r.Pending, "yes", "no"), observeCell(r.Edge.LastSeen)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
	addAccessMapFilterFlags(cmd, &f)
	return cmd
}

// ---- attack paths ----------------------------------------------------------------

// accessMapAttackStep is ONE NODE of a chain, not an edge between two.
//
// Written from the engine's attackStepDTO (attackpath.go:39): node_kind,
// node_id, node_name, mode, tool_id. The first draft of this file modeled a step
// as a from/to pair, which reads plausibly and is wrong — it would have rendered
// every path as a list of empty arrows, because none of those field names exist
// on the wire.
type accessMapAttackStep struct {
	NodeKind string `json:"node_kind"`
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name,omitempty"`
	Mode     string `json:"mode,omitempty"`
	ToolID   string `json:"tool_id,omitempty"`
}

type accessMapAttackPath struct {
	Kind           string                `json:"kind"`
	Steps          []accessMapAttackStep `json:"steps"`
	MaxSensitivity string                `json:"max_sensitivity,omitempty"`
	Attribution    string                `json:"attribution"`
	MinConfidence  string                `json:"min_confidence"`
}

type accessMapAttackPaths struct {
	Paths []accessMapAttackPath `json:"paths"`
}

type accessMapAttackSummary struct {
	TotalAgents      int `json:"total_agents"`
	ReachablePaths   int `json:"reachable_paths"`
	EscalationPaths  int `json:"escalation_paths"`
	ExfilRoutes      int `json:"exfil_routes"`
	CriticalAgents   int `json:"critical_agents"`
	SensitiveTargets int `json:"sensitive_targets"`
}

// accessMapStepName names one node of a path, preferring the human name over the id.
func accessMapStepName(s accessMapAttackStep) string {
	name := s.NodeName
	if name == "" {
		name = s.NodeID
	}
	if name == "" {
		name = s.NodeKind
	}
	if s.Mode != "" {
		name += "(" + s.Mode + ")"
	}
	return name
}

// A path's HOPS is the number of EDGES, which is one less than the number of
// nodes. Reporting the node count as hops would inflate every single-edge path
// into a two-hop chain, and "two hops away" is how an operator decides whether
// something is adjacent.
func attackPathHops(steps int) int {
	if steps <= 0 {
		return 0
	}
	return steps - 1
}

func renderAttackPaths(cmd *cobra.Command, raw []byte, p accessMapAttackPaths, empty string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if len(p.Paths) == 0 {
			_, err := fmt.Fprintln(w, empty)
			return err
		}
		tw := newTabWriter(w)
		if _, err := fmt.Fprintln(tw, "KIND\tHOPS\tSENSITIVITY\tATTRIBUTION\tMIN-CONF\tPATH"); err != nil {
			return err
		}
		for _, path := range p.Paths {
			names := make([]string, 0, len(path.Steps))
			for _, s := range path.Steps {
				names = append(names, accessMapStepName(s))
			}
			if _, err := fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
				observeCell(path.Kind), attackPathHops(len(path.Steps)),
				observeCell(path.MaxSensitivity),
				observeCell(path.Attribution), observeCell(path.MinConfidence),
				observeCell(strings.Join(names, " -> "))); err != nil {
				return err
			}
		}
		return tw.Flush()
	}, observeJSON(raw))
}

func newAccessMapAttackPathsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "attack-paths",
		Aliases: []string{"attackpaths"},
		Short:   "Reachability, privilege-escalation and exfiltration analyses",
		Long: "attack-paths runs the four graph analyses over the same AccessEdge data the\n" +
			"`graph` view shows: what one agent can reach, where it could escalate, which\n" +
			"routes could carry data out of one resource, and the estate-wide summary.\n\n" +
			"They answer from OBSERVED and PERMITTED evidence, so an empty answer means the\n" +
			"engine saw no such path in the data it has — not that none can exist.",
		Example: "  olivares accessmap attack-paths summary\n" +
			"  olivares accessmap attack-paths reachability --agent-id ag-7\n" +
			"  olivares accessmap attack-paths exfil --resource-id res-3 -o json",
	}
	cmd.AddCommand(
		newAttackPathAgentCmd(flags, "reachability", "/attack-paths/reachability",
			"List the resources one agent can reach",
			"reachability walks agent→resource and agent→tool→resource chains from one agent\nand lists where they end. It is the blast radius of that single agent."),
		newAttackPathAgentCmd(flags, "escalation", "/attack-paths/escalation",
			"List the privilege-escalation chains open to one agent",
			"escalation lists the chains by which one agent could obtain access it was not\ngranted directly. An empty answer is evidence about the graph the engine holds,\nnot a proof of impossibility."),
		newAttackPathExfilCmd(flags),
		newAttackPathSummaryCmd(flags),
	)
	return cmd
}

// newAttackPathAgentCmd builds the two analyses keyed on --agent-id. They differ
// only in route and wording, so they share one constructor rather than one copy
// each — a copy is where the two would eventually stop refusing alike.
func newAttackPathAgentCmd(flags *authClientFlags, name, path, short, long string) *cobra.Command {
	var agentID string
	cmd := &cobra.Command{
		Use:     name,
		Short:   short,
		Long:    long + "\n\n--agent-id is required by the route and is checked before the request is sent.",
		Example: "  olivares accessmap attack-paths " + name + " --agent-id ag-7\n  olivares accessmap attack-paths " + name + " --agent-id ag-7 -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if agentID != "" {
				q.Set("agent_id", agentID)
			}
			if err := requireObserveQuery(q, "agent_id", "--agent-id"); err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: accessMapNS, method: http.MethodGet, path: path, query: q}.do(cmd)
			if err != nil {
				return err
			}
			var p accessMapAttackPaths
			if err := res.decode(&p); err != nil {
				return err
			}
			return renderAttackPaths(cmd, res.raw, p, "no "+name+" paths found for this agent in the recorded access graph")
		},
	}
	cmd.Flags().StringVar(&agentID, "agent-id", "", "the agent to analyze (required)")
	return cmd
}

func newAttackPathExfilCmd(flags *authClientFlags) *cobra.Command {
	var resourceID string
	cmd := &cobra.Command{
		Use:   "exfil",
		Short: "List the exfiltration routes out of one resource",
		Long: "exfil lists the routes by which data in one resource could leave — the readers\n" +
			"of that resource and where each of them can write.\n\n" +
			"--resource-id is required by the route and is checked before the request is sent.",
		Example: "  olivares accessmap attack-paths exfil --resource-id res-3\n" +
			"  olivares accessmap attack-paths exfil --resource-id res-3 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if resourceID != "" {
				q.Set("resource_id", resourceID)
			}
			if err := requireObserveQuery(q, "resource_id", "--resource-id"); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: accessMapNS, method: http.MethodGet, path: "/attack-paths/exfil", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var p accessMapAttackPaths
			if err := res.decode(&p); err != nil {
				return err
			}
			return renderAttackPaths(cmd, res.raw, p, "no exfiltration routes found out of this resource in the recorded access graph")
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource-id", "", "the resource to analyze (required)")
	return cmd
}

func newAttackPathSummaryCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "summary",
		Short: "Show the estate-wide attack-surface counts",
		Long: "summary counts the estate: agents, reachable paths, escalation chains, exfil\n" +
			"routes, and how many agents and targets are in the critical/sensitive classes.\n" +
			"It is the number to trend, not to act on — the per-agent verbs say what to do.",
		Example: "  olivares accessmap attack-paths summary\n  olivares accessmap attack-paths summary -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: accessMapNS, method: http.MethodGet, path: "/attack-paths/summary",
			}.do(cmd)
			if err != nil {
				return err
			}
			var s accessMapAttackSummary
			if err := res.decode(&s); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "agents\t%d\n", s.TotalAgents)
				fmt.Fprintf(tw, "reachable paths\t%d\n", s.ReachablePaths)
				fmt.Fprintf(tw, "escalation paths\t%d\n", s.EscalationPaths)
				fmt.Fprintf(tw, "exfil routes\t%d\n", s.ExfilRoutes)
				fmt.Fprintf(tw, "critical agents\t%d\n", s.CriticalAgents)
				fmt.Fprintf(tw, "sensitive targets\t%d\n", s.SensitiveTargets)
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}
