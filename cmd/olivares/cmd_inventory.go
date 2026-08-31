// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

	"github.com/spf13/cobra"
)

// The inventory namespace from a terminal: the catalog of entities the engine has
// observed, what kinds they are, and whether each is still being seen.

const inventoryNS = "inventory"

func newInventoryCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "inventory",
		Short: "List the observed entity catalog and its coverage summary",
		Long: "inventory is the catalog of entities the engine has actually OBSERVED — agents,\n" +
			"tools, resources, skills, models, providers — with the signal sources that saw\n" +
			"each one and whether it is still being seen.\n\n" +
			"It is an observation record, not a declaration: an entity is here because\n" +
			"something reported it, and a `stale` status means nothing has reported it\n" +
			"lately, not that it was removed.",
		Example: "  olivares inventory summary\n" +
			"  olivares inventory entities ls --kind agent\n" +
			"  olivares inventory entities get agent ag-7 -o json",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newInventorySummaryCmd(&flags),
		newInventoryEntitiesCmd(&flags),
	)
	return root
}

// ---- summary --------------------------------------------------------------------

type inventoryKindCount struct {
	Active int `json:"active"`
	Stale  int `json:"stale"`
	Total  int `json:"total"`
}

type inventorySummary struct {
	ByKind    map[string]*inventoryKindCount `json:"by_kind"`
	BySource  map[string]int                 `json:"by_source"`
	Total     int                            `json:"total"`
	Truncated bool                           `json:"truncated,omitempty"`
}

func newInventorySummaryCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "summary",
		Short: "Count catalog entities by kind and by signal source",
		Long: "summary counts the catalog two ways: by entity kind (with the active/stale\n" +
			"split) and by the signal source that observed them.\n\n" +
			"THE ENGINE CAPS THIS SCAN. When it does, the response says so and this command\n" +
			"prints it loudly: an inventory total read as complete when it is a floor is how\n" +
			"an estate concludes it has fewer agents than it has.",
		Example: "  olivares inventory summary\n  olivares inventory summary -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: inventoryNS, method: http.MethodGet, path: "/summary",
			}.do(cmd)
			if err != nil {
				return err
			}
			var s inventorySummary
			if err := res.decode(&s); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "%d entities in the catalog\n", s.Total); err != nil {
					return err
				}
				if s.Truncated {
					if _, err := fmt.Fprintln(w,
						"TRUNCATED: the engine capped this scan, so every count below is a FLOOR, not a total"); err != nil {
						return err
					}
				}
				if len(s.ByKind) > 0 {
					tw := newTabWriter(w)
					if _, err := fmt.Fprintln(tw, "KIND\tACTIVE\tSTALE\tTOTAL"); err != nil {
						return err
					}
					// Sorted: a map ranged directly reorders every run, which makes
					// two invocations impossible to diff.
					for _, k := range observeSortedKeys(s.ByKind) {
						c := s.ByKind[k]
						if c == nil {
							continue
						}
						if _, err := fmt.Fprintf(tw, "%s\t%d\t%d\t%d\n",
							observeCell(k), c.Active, c.Stale, c.Total); err != nil {
							return err
						}
					}
					if err := tw.Flush(); err != nil {
						return err
					}
				}
				if len(s.BySource) == 0 {
					_, err := fmt.Fprintln(w, "no signal source has reported an entity yet")
					return err
				}
				st := newTabWriter(w)
				if _, err := fmt.Fprintln(st, "SIGNAL SOURCE\tENTITIES"); err != nil {
					return err
				}
				for _, k := range observeSortedKeys(s.BySource) {
					if _, err := fmt.Fprintf(st, "%s\t%d\n", observeCell(k), s.BySource[k]); err != nil {
						return err
					}
				}
				return st.Flush()
			}, observeJSON(res.raw))
		},
	}
}

// observeSortedKeys returns a map's keys in a stable order, so repeated invocations of
// the same command produce byte-identical tables and a diff means something.
func observeSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- entities -------------------------------------------------------------------

type inventoryEntry struct {
	Kind            string   `json:"kind"`
	EntityID        string   `json:"entity_id"`
	Name            string   `json:"name"`
	Ref             string   `json:"ref,omitempty"`
	Status          string   `json:"status"`
	SignalSources   []string `json:"signal_sources"`
	Hosts           []string `json:"hosts,omitempty"`
	FirstSeen       string   `json:"first_seen"`
	LastSeen        string   `json:"last_seen"`
	OccurrenceCount int64    `json:"occurrence_count"`
}

type inventoryEntryList struct {
	Items []inventoryEntry `json:"items"`
	observePage
}

type inventoryEntryDetail struct {
	Entry  inventoryEntry `json:"entry"`
	Detail map[string]any `json:"detail,omitempty"`
}

func newInventoryEntitiesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "entities",
		Aliases: []string{"entity"},
		Short:   "List and open catalog entities",
		Long: "entities lists the observed catalog, filtered by kind and status, and opens one\n" +
			"entry with the projection of the core entity it overlays.",
		Example: "  olivares inventory entities ls\n" +
			"  olivares inventory entities ls --kind agent --status active\n" +
			"  olivares inventory entities get agent ag-7",
	}
	cmd.AddCommand(newInventoryEntitiesListCmd(flags), newInventoryEntitiesGetCmd(flags))
	return cmd
}

func newInventoryEntitiesListCmd(flags *authClientFlags) *cobra.Command {
	var kind, status string
	var page observePageFlags
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List catalog entities",
		Long: "ls lists catalog entries newest-cursor-first, optionally narrowed by --kind and\n" +
			"--status. The signal-source column is the answer to \"how do we know this\n" +
			"exists\", which is usually the next question after \"what is this\".",
		Example: "  olivares inventory entities ls\n" +
			"  olivares inventory entities ls --kind tool --limit 50\n" +
			"  olivares inventory entities ls --status stale -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if kind != "" {
				q.Set("kind", kind)
			}
			if status != "" {
				q.Set("status", status)
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: inventoryNS, method: http.MethodGet, path: "/entities", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list inventoryEntryList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "no catalog entities match this query")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "KIND\tID\tNAME\tSTATUS\tSIGNALS\tSEEN\tLAST SEEN"); err != nil {
					return err
				}
				for _, e := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
						observeCell(e.Kind), observeCell(e.EntityID),
						observeCell(firstNonEmptyCLI(e.Name, e.Ref)), observeCell(e.Status),
						observeCell(observeJoinList(e.SignalSources)), e.OccurrenceCount,
						observeCell(e.LastSeen)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares inventory entities ls")
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "filter by entity kind (agent, tool, resource, skill, model, provider)")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (active, stale)")
	addObservePageFlags(cmd, &page)
	return cmd
}

// observeJoinList renders a string slice for a table cell without Go's bracket syntax.
func observeJoinList(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func newInventoryEntitiesGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <kind> <id>",
		Short: "Show one catalog entity and the core entity it overlays",
		Long: "get takes the KIND and the ID because the route is keyed on both: an id is only\n" +
			"unique within its kind, so `get ag-7` alone could not address a row.\n\n" +
			"The `detail` block is a minimal projection of the underlying core entity and\n" +
			"differs by kind, so it is rendered as the engine sent it rather than flattened\n" +
			"into columns that would only fit one kind.",
		Example: "  olivares inventory entities get agent ag-7\n" +
			"  olivares inventory entities get provider anthropic -o json",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: inventoryNS, method: http.MethodGet,
				path: "/entities" + observeIDPath(args[0], args[1]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var d inventoryEntryDetail
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "kind\t%s\n", observeCell(d.Entry.Kind))
				fmt.Fprintf(tw, "id\t%s\n", observeCell(d.Entry.EntityID))
				fmt.Fprintf(tw, "name\t%s\n", observeCell(d.Entry.Name))
				fmt.Fprintf(tw, "ref\t%s\n", observeCell(d.Entry.Ref))
				fmt.Fprintf(tw, "status\t%s\n", observeCell(d.Entry.Status))
				fmt.Fprintf(tw, "signals\t%s\n", observeCell(observeJoinList(d.Entry.SignalSources)))
				fmt.Fprintf(tw, "hosts\t%s\n", observeCell(observeJoinList(d.Entry.Hosts)))
				fmt.Fprintf(tw, "first seen\t%s\n", observeCell(d.Entry.FirstSeen))
				fmt.Fprintf(tw, "last seen\t%s\n", observeCell(d.Entry.LastSeen))
				fmt.Fprintf(tw, "observations\t%d\n", d.Entry.OccurrenceCount)
				if err := tw.Flush(); err != nil {
					return err
				}
				if len(d.Detail) == 0 {
					_, err := fmt.Fprintln(w,
						"no core entity backs this catalog row: it is an observation with nothing to overlay")
					return err
				}
				// render-exempt: this IS the text branch renderOut invoked, and the
				// detail block's shape is per-kind — flattening it into fixed columns
				// would fit one kind and misrepresent the rest.
				body, merr := json.MarshalIndent(d.Detail, "", "  ")
				if merr != nil {
					_, err := fmt.Fprintf(w, "detail: %v\n", d.Detail)
					return err
				}
				_, err := fmt.Fprintf(w, "detail:\n%s\n", body)
				return err
			}, observeJSON(res.raw))
		},
	}
}
