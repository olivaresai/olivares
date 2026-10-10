// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

const workspacesPath = "/v1/workspaces"

// `olivares workspaces` is the CLI half of the scoping plane's workspace reads.
// Workspaces are the containers of the organization tree (agents, sessions,
// resources and agent groups hang in one); the console has managed them since
// And the shell only completed their names (cmd_completion.go) — there was
// no verb to READ one.
//
// The read verbs are thin clients over the tenant-scoped routes
// (core/api/server.go:835): `ls` reads GET /v1/workspaces, and `get` reads
// GET /v1/workspaces/{id} plus its summary, the published form of the
// workspace's one contents read (store.ReadWorkspaceContents, #216) — the
// counts come from the engine's confined scope, including the rows without a
// workspace that belong to the default one, so the CLI never stitches a
// contents answer of its own. The organization tree verbs — `tree`,
// `set-parent` and `place` — are the Business department verbs
// (the workspaceCommands edition port); the default build adds none.
//
// IT DECIDES NOTHING ABOUT AUTHORIZATION. Both read routes are tenant:read, and the
// concealment is the engine's: a workspace-confined caller sees only its own
// workspace in the list, and naming another one answers 404, not a
// distinguishable refusal (handlers_scoping.go). There is no client-side check
// to drift from that.
func newWorkspacesCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:     "workspaces",
		Aliases: []string{"workspace"},
		Short:   "Workspaces: list, show",
		Long: "Read the workspaces of your organization. A workspace is the container agents,\n" +
			"sessions, resources and agent groups live in. `ls` lists the tenant's workspaces;\n" +
			"`get` shows one workspace with the counts of what it holds, from the engine's one\n" +
			"contents read — the same numbers the console's workspace page shows.\n\n" +
			"An operator confined to a workspace sees only that one, by the engine's decision.",
		Example: `  olivares workspaces ls
  olivares workspaces get 018f2c2e-0000-7000-8000-000000000042
  olivares workspaces show 018f2c2e-0000-7000-8000-000000000042 -o json`,
	}
	flags.addPersistent(root)
	client := bootstrapClient{flags: flags, surface: "workspaces"}
	root.AddCommand(workspacesListCmd(client), workspacesGetCmd(client))
	if thisEdition.workspaceCommands != nil {
		root.AddCommand(thisEdition.workspaceCommands(client)...)
	}
	return root
}

// cliWorkspaceRow mirrors core/api WorkspaceDTO (handlers_scoping.go).
type cliWorkspaceRow struct {
	ID        string         `json:"id"`
	TenantID  string         `json:"tenant_id"`
	Name      string         `json:"name"`
	Slug      string         `json:"slug"`
	Status    string         `json:"status"`
	IsDefault bool           `json:"is_default"`
	ParentID  string         `json:"parent_id,omitempty"`
	Settings  map[string]any `json:"settings,omitempty"`
	CreatedAt string         `json:"created_at"`
	UpdatedAt string         `json:"updated_at"`
	Version   int64          `json:"version"`
}

type cliWorkspaceList struct {
	Items   []cliWorkspaceRow `json:"items"`
	Cursor  string            `json:"cursor,omitempty"`
	HasMore bool              `json:"has_more,omitempty"`
}

// cliWorkspaceSummary mirrors core/api WorkspaceSummaryDTO (handlers_scoping.go),
// the published form of the workspace's contents read.
type cliWorkspaceSummary struct {
	WorkspaceID         string `json:"workspace_id"`
	AgentCount          int    `json:"agent_count"`
	SessionCount        int    `json:"session_count"`
	ResourceCount       int    `json:"resource_count"`
	GroupCount          int    `json:"group_count"`
	AgentCountCapped    bool   `json:"agent_count_capped"`
	SessionCountCapped  bool   `json:"session_count_capped"`
	ResourceCountCapped bool   `json:"resource_count_capped"`
	GroupCountCapped    bool   `json:"group_count_capped"`
}

func workspacesListCmd(client bootstrapClient) *cobra.Command {
	var limit int
	var cursor string
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the workspaces of the tenant",
		Long: "List the workspaces of your organization with their id, handle, lifecycle status and\n" +
			"which one is the default — the resolution target for every entity created without a\n" +
			"workspace. A workspace-confined caller sees only its own; that concealment is the\n" +
			"engine's, not this client's.",
		Example: `  olivares workspaces ls
  olivares workspaces ls -o json
  olivares workspaces ls --limit 100`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := client.expect(cmd, http.MethodGet,
				workspacesPath+listQuerySuffix(limit, cursor), nil, http.StatusOK)
			if err != nil {
				return err
			}
			var list cliWorkspaceList
			if err := decodeBootstrapJSON("workspaces", raw, &list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no workspaces exist yet")
					return err
				}
				tbl := termrender.Table{Header: []string{"id", "name", "slug", "status", "default", "created"}}
				for _, w := range list.Items {
					tbl.Rows = append(tbl.Rows, []string{
						safeCLIValue(w.ID, ""),
						safeCLIValue(w.Name, ""),
						safeCLIValue(w.Slug, ""),
						safeCLIValue(w.Status, ""),
						flagCell(w.IsDefault),
						safeCLIValue(w.CreatedAt, ""),
					})
				}
				renderTo(out).Table(tbl)
				return writeMorePages(out, list.HasMore, list.Cursor)
			}, json.RawMessage(raw))
		},
	}
	addListPageFlags(cmd, &limit, &cursor)
	return cmd
}

func workspacesGetCmd(client bootstrapClient) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <workspace-id>",
		Aliases: []string{"show"},
		Short:   "Show one workspace and what it holds",
		Long: "Show one workspace: its metadata, then the counts of what it holds — agents,\n" +
			"sessions, resources and agent groups. The counts are the workspace's one contents\n" +
			"read through the engine's confined scope (the same read the console's workspace page\n" +
			"and GET /v1/workspaces/{id}/summary publish), so rows created without a workspace are\n" +
			"counted in the default one, here as everywhere.\n\n" +
			"A count followed by + is a FLOOR, not a total: the read carries one store page and the\n" +
			"kind holds at least that many rows. Naming a workspace outside your confinement answers\n" +
			"\"not found\", by the engine's concealment rule.",
		Example: `  olivares workspaces get 018f2c2e-0000-7000-8000-000000000042
  olivares workspaces show 018f2c2e-0000-7000-8000-000000000042 -o json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeWorkspaces,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := bootstrapPathID(args[0])
			wsRaw, err := client.expect(cmd, http.MethodGet, workspacesPath+"/"+id, nil, http.StatusOK)
			if err != nil {
				return err
			}
			var ws cliWorkspaceRow
			if err := decodeBootstrapJSON("workspaces", wsRaw, &ws); err != nil {
				return err
			}
			sumRaw, err := client.expect(cmd, http.MethodGet, workspacesPath+"/"+id+"/summary", nil, http.StatusOK)
			if err != nil {
				return err
			}
			var sum cliWorkspaceSummary
			if err := decodeBootstrapJSON("workspaces", sumRaw, &sum); err != nil {
				return err
			}
			// Both published payloads travel verbatim; the two keys name the two
			// reads they answer. No third shape to keep in agreement with the API.
			jsonVal := map[string]json.RawMessage{
				"workspace": json.RawMessage(wsRaw),
				"summary":   json.RawMessage(sumRaw),
			}
			return renderOut(cmd, func(out io.Writer) error {
				fields := []termrender.Field{
					{Key: "id", Value: safeCLIValue(ws.ID, "")},
					{Key: "name", Value: safeCLIValue(ws.Name, "")},
					{Key: "slug", Value: safeCLIValue(ws.Slug, "")},
					{Key: "status", Value: safeCLIValue(ws.Status, "")},
					{Key: "default", Value: flagCell(ws.IsDefault)},
					{Key: "tenant", Value: safeCLIValue(ws.TenantID, "")},
					{Key: "created", Value: safeCLIValue(ws.CreatedAt, "")},
					{Key: "updated", Value: safeCLIValue(ws.UpdatedAt, "")},
					{Key: "version", Value: countCell(ws.Version)},
					{Key: "agents", Value: workspaceContentsCell(sum.AgentCount, sum.AgentCountCapped)},
					{Key: "sessions", Value: workspaceContentsCell(sum.SessionCount, sum.SessionCountCapped)},
					{Key: "resources", Value: workspaceContentsCell(sum.ResourceCount, sum.ResourceCountCapped)},
					{Key: "agent groups", Value: workspaceContentsCell(sum.GroupCount, sum.GroupCountCapped)},
				}
				if ws.ParentID != "" {
					fields = append(fields, termrender.Field{Key: "parent", Value: safeCLIValue(ws.ParentID, "")})
				}
				if len(ws.Settings) > 0 {
					settings, merr := json.Marshal(ws.Settings)
					if merr != nil {
						return merr
					}
					fields = append(fields, termrender.Field{Key: "settings", Value: string(settings)})
				}
				renderTo(out).Fields(fields)
				if sum.AgentCountCapped || sum.SessionCountCapped || sum.ResourceCountCapped || sum.GroupCountCapped {
					_, werr := fmt.Fprintln(out,
						"+ marks a floor, not a total: the kind holds at least that many rows")
					return werr
				}
				return nil
			}, jsonVal)
		},
	}
	return cmd
}

// workspaceContentsCell renders one contents count, marking a truncated page as
// the floor it is (the Long text and the trailing legend say what + means).
func workspaceContentsCell(n int, capped bool) string {
	if capped {
		return countCell(n) + "+"
	}
	return countCell(n)
}
