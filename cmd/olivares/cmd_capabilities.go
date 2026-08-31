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
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// `olivares capabilities` — WHAT THIS ESTATE CAN ACTUALLY DO, and where each ability came from.
//
// ⚠ "Zero CLI coverage" would be wrong here, and checking it is what produced this shape.
// /v1/m/capabilities serves ELEVEN routes; three of them — the tool pins — are already operator
// surface under `olivares mcp pins ls|approve|rm` (cmd_mcp.go). Two more, `servers` and `wiring`,
// are touched only by e2e tests. The rest had nothing.
//
// So this adds the DISCOVERY half and nothing else: what servers are connected, what tools and
// skills they bring. Two deliberate omissions:
//
//   - `configs` is CRUD with a write half (POST/PUT/DELETE). Its reads belong with its writes,
//     which need a confirmation story, not with a listing.
//
// `wiring` was the other omission and is now here: it returned a GRAPH rather than a list, so it
// waited until the precedent this tree already set for one (`accessmap graph`) had been read
// instead of being invented in passing. What that reading changed is written at the verb.
const capabilitiesNS = "capabilities"

func newCapabilitiesCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:   "capabilities",
		Short: "What this estate can do: connected servers, and the tools and skills they bring",
		Long: "capabilities is the discovered surface — the MCP servers this estate talks to and the\n" +
			"tools and skills each one contributes. The namespace is the same word the API uses\n" +
			"(/v1/m/capabilities), so a route and its verb are one translation apart.\n\n" +
			"Tool PINS live under `olivares mcp pins`: they were already operator surface and are\n" +
			"not duplicated here.",
		Example: "  olivares capabilities servers ls\n" +
			"  olivares capabilities tools --server-id 018f0000-0000-7000-8000-000000000001",
	}
	flags.addPersistent(root)
	root.AddCommand(capabilitiesServersCmd(flags), capabilitiesSkillsCmd(flags),
		capabilitiesToolsCmd(flags), capabilitiesWiringCmd(flags))
	return root
}

// cliMCPServer mirrors modules/capabilities/servers.go serverDTO.
type cliMCPServer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Transport  string `json:"transport"`
	Endpoint   string `json:"endpoint,omitempty"`
	Version    string `json:"version,omitempty"`
	Status     string `json:"status"`
	Connection string `json:"connection"`
	ToolCount  int    `json:"tool_count"`
	HasConfig  bool   `json:"has_config"`
}

type cliMCPServerList struct {
	Items   []cliMCPServer `json:"items"`
	Cursor  string         `json:"cursor,omitempty"`
	HasMore bool           `json:"has_more,omitempty"`
}

// cliMCPServerDetail mirrors serverDetailDTO: the row plus what it brings. The counts are what
// the text view shows; `-o json` carries the whole document, so a field this struct does not
// model still reaches the operator.
type cliMCPServerDetail struct {
	cliMCPServer
	Tools     []cliMCPTool  `json:"tools"`
	Skills    []cliMCPSkill `json:"skills"`
	Resources []string      `json:"resources"`
}

type cliMCPSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Source      string `json:"source,omitempty"`
	Version     string `json:"version,omitempty"`
	MCPServerID string `json:"mcp_server_id,omitempty"`
	Status      string `json:"status"`
}

type cliMCPSkillList struct {
	Items   []cliMCPSkill `json:"items"`
	Cursor  string        `json:"cursor,omitempty"`
	HasMore bool          `json:"has_more,omitempty"`
}

// cliMCPTool carries the two HINTS on purpose. `destructive_hint` and `annotation_trust` are the
// difference between "this tool can read" and "this tool can delete, and we are trusting the
// server's own word for it" — the fact an operator most needs before wiring an agent to it.
type cliMCPTool struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Kind            string `json:"kind,omitempty"`
	MCPServerID     string `json:"mcp_server_id,omitempty"`
	ReadOnlyHint    bool   `json:"read_only_hint"`
	DestructiveHint bool   `json:"destructive_hint"`
	AnnotationTrust string `json:"annotation_trust"`
}

type cliMCPToolList struct {
	Items   []cliMCPTool `json:"items"`
	Cursor  string       `json:"cursor,omitempty"`
	HasMore bool         `json:"has_more,omitempty"`
}

func capabilitiesServersCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "servers",
		Short: "The MCP servers this estate talks to",
		Long: "The MCP servers registered for this tenant: what each one is, whether the plane can\n" +
			"currently reach it, and which tools it advertises. `ls` answers which servers exist;\n" +
			"`get` answers what one of them offers.",
		Example: "  olivares capabilities servers ls\n" +
			"  olivares capabilities servers get filesystem",
	}
	cmd.AddCommand(capabilitiesServersListCmd(flags), capabilitiesServersGetCmd(flags))
	return cmd
}

func capabilitiesServersListCmd(flags *authClientFlags) *cobra.Command {
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the connected MCP servers",
		Long: "List every MCP server this tenant has, with its transport, declared status and the\n" +
			"DERIVED connection state. Those two are not the same fact: `status` is what the record\n" +
			"says, `connection` is what the plane last observed.",
		Example: "  olivares capabilities servers ls\n  olivares capabilities servers ls -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			// handleListServers passes listQuery(r) straight to the repository, so both flags
			// are read. `skills` and `tools` below do the same; `wiring` is not a list at all.
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: capabilitiesNS, method: http.MethodGet, path: "/servers", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliMCPServerList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no MCP servers")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ID\tNAME\tTRANSPORT\tSTATUS\tCONNECTION\tTOOLS\tCONFIG\tENDPOINT"); err != nil {
					return err
				}
				for _, s := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
						observeCell(s.ID), observeCell(s.Name), observeCell(s.Transport),
						observeCell(s.Status), observeCell(s.Connection), s.ToolCount,
						observeBool(s.HasConfig, "yes", "no"), observeCell(s.Endpoint)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(out, observePage{Cursor: list.Cursor, HasMore: list.HasMore}, cmd.CommandPath())
			}, observeJSON(res.raw))
		},
	}
	addObservePageFlags(cmd, page)
	return cmd
}

func capabilitiesServersGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <server-id>",
		Short: "Show one MCP server and what it brings",
		Long: "Show a single server with the counts of what it contributes. The full document —\n" +
			"every tool, skill and consumer — is what `-o json` emits; the text view is the summary\n" +
			"an operator reads first.",
		Example: "  olivares capabilities servers get 018f0000-0000-7000-8000-000000000001",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: capabilitiesNS, method: http.MethodGet,
				path: "/servers/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var d cliMCPServerDetail
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ID\tNAME\tTRANSPORT\tSTATUS\tCONNECTION\tTOOLS\tSKILLS\tRESOURCES\tENDPOINT"); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
					observeCell(d.ID), observeCell(d.Name), observeCell(d.Transport),
					observeCell(d.Status), observeCell(d.Connection),
					len(d.Tools), len(d.Skills), len(d.Resources),
					observeCell(d.Endpoint)); err != nil {
					return err
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

func capabilitiesSkillsCmd(flags *authClientFlags) *cobra.Command {
	var serverID string
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "The skills the connected servers contribute",
		Long: "List the skills available in this tenant and which server each came from. Use\n" +
			"--server-id to narrow to one server, which is the filter the engine reads.",
		Example: "  olivares capabilities skills\n" +
			"  olivares capabilities skills --server-id 018f0000-0000-7000-8000-000000000001",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if serverID != "" {
				q.Set("server_id", serverID)
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: capabilitiesNS, method: http.MethodGet, path: "/skills", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliMCPSkillList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no skills")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ID\tNAME\tSOURCE\tVERSION\tSTATUS\tSERVER"); err != nil {
					return err
				}
				for _, s := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(s.ID), observeCell(s.Name), observeCell(s.Source),
						observeCell(s.Version), observeCell(s.Status),
						observeCell(s.MCPServerID)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(out, observePage{Cursor: list.Cursor, HasMore: list.HasMore}, cmd.CommandPath())
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&serverID, "server-id", "", "only skills from this MCP server")
	addObservePageFlags(cmd, page)
	return cmd
}

func capabilitiesToolsCmd(flags *authClientFlags) *cobra.Command {
	var serverID string
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:   "tools",
		Short: "The tools the connected servers expose, with their destructive hints",
		Long: "List the tools available in this tenant. The two hint columns are the point: READ-ONLY\n" +
			"and DESTRUCTIVE are the server's own declaration about what a tool does, and TRUST says\n" +
			"how much weight the plane gives that declaration. Read them before wiring an agent.",
		Example: "  olivares capabilities tools\n" +
			"  olivares capabilities tools --server-id 018f0000-0000-7000-8000-000000000001 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if serverID != "" {
				q.Set("server_id", serverID)
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: capabilitiesNS, method: http.MethodGet, path: "/tools", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliMCPToolList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no tools")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ID\tNAME\tKIND\tREAD-ONLY\tDESTRUCTIVE\tTRUST\tSERVER"); err != nil {
					return err
				}
				for _, t := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(t.ID), observeCell(t.Name), observeCell(t.Kind),
						observeBool(t.ReadOnlyHint, "yes", "no"),
						observeBool(t.DestructiveHint, "YES", "no"),
						observeCell(t.AnnotationTrust), observeCell(t.MCPServerID)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(out, observePage{Cursor: list.Cursor, HasMore: list.HasMore}, cmd.CommandPath())
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&serverID, "server-id", "", "only tools from this MCP server")
	addObservePageFlags(cmd, page)
	return cmd
}

// cliWiring* mirror the DTOs in modules/capabilities/wiring.go.
type cliWiringNode struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type cliWiringEdge struct {
	OriginKind      string   `json:"origin_kind"`
	OriginRef       string   `json:"origin_ref"`
	CapabilityKind  string   `json:"capability_kind"`
	CapabilityRef   string   `json:"capability_ref"`
	ToolRef         string   `json:"tool_ref,omitempty"`
	SignalSources   []string `json:"signal_sources"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	OccurrenceCount int64    `json:"occurrence_count"`
}

type cliWiringGraph struct {
	Nodes     []cliWiringNode `json:"nodes"`
	Edges     []cliWiringEdge `json:"edges"`
	Truncated bool            `json:"truncated,omitempty"`
	Note      string          `json:"note"`
}

// capabilitiesWiringCmd — WHO IS ACTUALLY USING WHAT, which none of the three verbs above answers.
//
// `servers`, `skills` and `tools` say what EXISTS. This says what is CONNECTED: one edge per
// origin→capability pair the plane has observed, with its signal sources and how often.
//
// TWO PROPERTIES OF THIS ROUTE THAT THE OPERATOR HAS TO KNOW, both read off handleWiring rather
// than assumed from the family — and the reason this verb waited:
//
//   - IT DOES NOT PAGE. handleWiring builds `model.Query{Limit: listCap}` with a fixed cap and
//     never calls listQuery, so it reads neither `limit` nor `cursor`. Declaring those flags here
//     would be worse than omitting them: the engine would ignore them and `--cursor` would hand
//     back the first page forever, which is a wrong answer rather than a missing one.
//   - SO `truncated` HAS NO "NEXT". The response says whether the cap cut the graph and offers no
//     cursor to continue with. The only remedy is to narrow with the four filters, so the text
//     view says that out loud instead of leaving the operator to find out.
//
// Presentation follows `accessmap graph`, the precedent already in this tree: the EDGES as a
// table, then one summary line carrying both counts. No ASCII art, and the nodes are a count and
// not a second table — a node with no edge is not what anyone opened this for.
func capabilitiesWiringCmd(flags *authClientFlags) *cobra.Command {
	var originKind, originRef, capabilityKind, capabilityRef string
	cmd := &cobra.Command{
		Use:   "wiring",
		Short: "Who is actually using which capability, as observed edges",
		Long: "The capability-connection graph: one edge per origin→capability pair the plane has\n" +
			"observed, with its signal sources, first and last sighting and occurrence count. This\n" +
			"is a different question from `servers`/`tools`, which say what EXISTS, and from the\n" +
			"R/RW access graph of `accessmap`: this one is about capability USE, not permission.\n\n" +
			"This route does not page. It returns up to an engine-set cap and tells you whether it\n" +
			"cut the graph; if it did, narrow with the filters — there is no next page to ask for.",
		Example: "  olivares capabilities wiring\n" +
			"  olivares capabilities wiring --origin-kind agent --capability-kind tool\n" +
			"  olivares capabilities wiring --capability-ref stripe.refund -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for _, f := range []struct{ name, val string }{
				{"origin_kind", originKind}, {"origin_ref", originRef},
				{"capability_kind", capabilityKind}, {"capability_ref", capabilityRef},
			} {
				if f.val != "" {
					q.Set(f.name, f.val)
				}
			}
			res, err := observeCall{
				flags: flags, ns: capabilitiesNS, method: http.MethodGet, path: "/wiring", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var g cliWiringGraph
			if err := res.decode(&g); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(g.Edges) == 0 {
					_, err := fmt.Fprintln(out, "no capability is wired to anything yet")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw,
					"ORIGIN\tKIND\tCAPABILITY\tKIND\tTOOL\tSIGNALS\tSEEN\tFIRST\tLAST"); err != nil {
					return err
				}
				for _, e := range g.Edges {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
						observeCell(e.OriginRef), observeCell(e.OriginKind),
						observeCell(e.CapabilityRef), observeCell(e.CapabilityKind),
						observeCell(e.ToolRef), observeCell(strings.Join(e.SignalSources, ",")),
						e.OccurrenceCount, observeCell(e.FirstSeen),
						observeCell(e.LastSeen)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(out, "%d node(s), %d edge(s)\n",
					len(g.Nodes), len(g.Edges)); err != nil {
					return err
				}
				// NOT observeTruncationNote: that one tells the caller to continue with --cursor,
				// and this route has none. Announcing "there is more" without saying how to reach
				// it would send a script round the same page forever.
				if g.Truncated {
					if _, err := fmt.Fprintln(out,
						"⚠ the engine cut this graph at its cap and offers no cursor — narrow it "+
							"with --origin-kind/--origin-ref/--capability-kind/--capability-ref"); err != nil {
						return err
					}
				}
				// The server's `note` is a CONSTANT — the same sentence on every response,
				// saying this is not the R/RW access graph. A fixed paragraph under every
				// table is noise, and the only precedent in this tree for printing a
				// server note (eventing egress) prints one that carries information:
				// COVERAGE INCOMPLETE, and only when it is. So that sentence lives in the
				// help above, where it is read once, and the field stays in -o json.
				return nil
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&originKind, "origin-kind", "", "only edges from this kind of origin")
	cmd.Flags().StringVar(&originRef, "origin-ref", "", "only edges from this origin")
	cmd.Flags().StringVar(&capabilityKind, "capability-kind", "", "only edges to this kind of capability")
	cmd.Flags().StringVar(&capabilityRef, "capability-ref", "", "only edges to this capability")
	return cmd
}
