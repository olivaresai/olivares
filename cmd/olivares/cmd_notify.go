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
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The notify namespace from a terminal: the routing rules that decide which
// findings reach which destination, the append-only delivery ledger, and the
// durable outbox behind it.
//
// TWO VERBS HERE REACH THE OUTSIDE WORLD, and they are marked as such in their
// own help rather than being quietly grouped with the reads:
//
//   - `routes test` sends a REAL notification through a route's destination
//     (route.go:530-534). Somebody's pager can go off.
//   - `outbox redeliver` re-triggers delivery of a stored notification, and it
//     accepts ANY TERMINAL row — including `delivered`. That is deliberate
//     engine behavior for the ack-and-retry case (outbox_api.go:111-117), and it
//     means a careless redeliver can send a duplicate of something that already
//     arrived.
//
// Neither is DESTRUCTIVE — nothing is lost — so neither takes --yes; --yes is
// reserved for the one verb here that destroys state, `routes rm`. But an
// operator reading the help learns which commands leave the building.
//
// ROUTE UPDATES ARE REPLACEMENTS AND THE ENGINE KEEPS REVISIONS. `update` sends
// the whole predicate; a field you do not pass is cleared, not kept. That is
// survivable precisely because every change appends a revision and `routes
// restore` can put an earlier one back.

const notifyNS = "notify"

func newNotifyCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "notify",
		Short: "Author notification routes and inspect deliveries and the outbox",
		Long: "notify is the routing plane for findings: rules that select signals by type,\n" +
			"kind, severity, source and subject, and send them to a provisioned destination.\n\n" +
			"Authoring a route is a privileged, audited action, and every change appends a\n" +
			"revision — so an update that goes wrong can be put back with `routes restore`.\n\n" +
			"Two verbs actually reach the outside world: `routes test` sends a real\n" +
			"notification, and `outbox redeliver` re-sends a stored one. Both say so.",
		Example: "  olivares notify routes ls\n" +
			"  olivares notify routes create --name criticals --destination ops-slack --min-severity critical\n" +
			"  olivares notify evaluate --event-type finding.created --severity critical\n" +
			"  olivares notify outbox ls --status dead",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newNotifyRoutesCmd(&flags),
		newNotifyMatchTypesCmd(&flags),
		newNotifyDestinationsCmd(&flags),
		newNotifyEvaluateCmd(&flags),
		newNotifyDeliveriesCmd(&flags),
		newNotifyOutboxCmd(&flags),
	)
	return root
}

// ---- DTOs -------------------------------------------------------------------------

type notifyRoute struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Enabled               bool     `json:"enabled"`
	MatchTypes            []string `json:"match_types"`
	MatchKinds            []string `json:"match_kinds"`
	MinSeverity           string   `json:"min_severity,omitempty"`
	MatchSources          []string `json:"match_sources"`
	MatchSubjectKinds     []string `json:"match_subject_kinds"`
	Destination           string   `json:"destination"`
	DedupWindowSeconds    int64    `json:"dedup_window_seconds"`
	ThrottleWindowSeconds int64    `json:"throttle_window_seconds"`
	Priority              int64    `json:"priority"`
	OwnerActor            string   `json:"owner_actor,omitempty"`
	CreatedAt             string   `json:"created_at,omitempty"`
}

type notifyRouteList struct {
	Items []notifyRoute `json:"items"`
	observePage
}

// notifyRouteInput is the writable route. It mirrors createRouteInput, which the
// engine uses for BOTH create and update — an update is a full replacement.
//
// Enabled is a POINTER because the engine treats it as tri-state on update: nil
// keeps the stored value, non-nil sets it (route.go:236-238). A plain bool here
// would silently disable every route updated without --enabled.
type notifyRouteInput struct {
	Name                  string   `json:"name,omitempty"`
	Enabled               *bool    `json:"enabled,omitempty"`
	MatchTypes            []string `json:"match_types,omitempty"`
	MatchKinds            []string `json:"match_kinds,omitempty"`
	MinSeverity           string   `json:"min_severity,omitempty"`
	MatchSources          []string `json:"match_sources,omitempty"`
	MatchSubjectKinds     []string `json:"match_subject_kinds,omitempty"`
	Destination           string   `json:"destination"`
	DedupWindowSeconds    int64    `json:"dedup_window_seconds"`
	ThrottleWindowSeconds int64    `json:"throttle_window_seconds"`
	Priority              int64    `json:"priority"`
}

// notifySeverities mirrors validSeverity (modules/notify/route.go). An empty
// min_severity is legal and means "no floor".
var notifySeverities = []string{"info", "low", "medium", "high", "critical"}

func newNotifyRoutesCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "routes",
		Aliases: []string{"route"},
		Short:   "Author, inspect, test and roll back notification routes",
		Long: "routes are the rules: which signals go where. A route names a provisioned\n" +
			"destination and a predicate over event type, finding kind, severity floor,\n" +
			"source and subject kind.\n\n" +
			"Every write appends a revision, so `revisions` shows how a route got to its\n" +
			"current shape and `restore` puts an earlier shape back.",
		Example: "  olivares notify routes ls\n" +
			"  olivares notify routes create --name criticals --destination ops-slack --min-severity critical\n" +
			"  olivares notify routes revisions rt-1\n" +
			"  olivares notify routes rm rt-1 --yes",
	}
	cmd.AddCommand(
		newNotifyRoutesListCmd(flags),
		newNotifyRoutesGetCmd(flags),
		newNotifyRoutesCreateCmd(flags),
		newNotifyRoutesUpdateCmd(flags),
		newNotifyRoutesRemoveCmd(flags),
		newNotifyRoutesTestCmd(flags),
		newNotifyRoutesRevisionsCmd(flags),
		newNotifyRoutesRestoreCmd(flags),
	)
	return cmd
}

func renderNotifyRoute(cmd *cobra.Command, raw []byte, r notifyRoute, headline string) error {
	return renderOut(cmd, func(w io.Writer) error {
		if headline != "" {
			if _, err := fmt.Fprintln(w, headline); err != nil {
				return err
			}
		}
		tw := newTabWriter(w)
		fmt.Fprintf(tw, "id\t%s\n", observeCell(r.ID))
		fmt.Fprintf(tw, "name\t%s\n", observeCell(r.Name))
		fmt.Fprintf(tw, "enabled\t%s\n", observeBool(r.Enabled, "yes", "NO — this route cannot fire"))
		fmt.Fprintf(tw, "destination\t%s\n", observeCell(r.Destination))
		fmt.Fprintf(tw, "min severity\t%s\n", observeCell(r.MinSeverity))
		fmt.Fprintf(tw, "match types\t%s\n", observeCell(observeJoinList(r.MatchTypes)))
		fmt.Fprintf(tw, "match kinds\t%s\n", observeCell(observeJoinList(r.MatchKinds)))
		fmt.Fprintf(tw, "match sources\t%s\n", observeCell(observeJoinList(r.MatchSources)))
		fmt.Fprintf(tw, "match subjects\t%s\n", observeCell(observeJoinList(r.MatchSubjectKinds)))
		fmt.Fprintf(tw, "dedup window\t%d s\n", r.DedupWindowSeconds)
		fmt.Fprintf(tw, "throttle window\t%d s\n", r.ThrottleWindowSeconds)
		fmt.Fprintf(tw, "priority\t%d\n", r.Priority)
		fmt.Fprintf(tw, "owner\t%s\n", observeCell(r.OwnerActor))
		fmt.Fprintf(tw, "created\t%s\n", observeCell(r.CreatedAt))
		return tw.Flush()
	}, observeJSON(raw))
}

func newNotifyRoutesListCmd(flags *authClientFlags) *cobra.Command {
	var destination, enabled string
	var page observePageFlags
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List notification routes",
		Long: "ls lists the tenant's routes. The ENABLED column is the one to read first: a\n" +
			"disabled route still matches in `evaluate` but can never fire, which is the\n" +
			"usual explanation for \"the rule looks right and nothing arrives\".",
		Example: "  olivares notify routes ls\n" +
			"  olivares notify routes ls --destination ops-slack\n" +
			"  olivares notify routes ls --enabled false -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if destination != "" {
				q.Set("destination", destination)
			}
			if enabled != "" {
				if enabled != "true" && enabled != "false" {
					return exitcode.New(exitcode.Usage,
						fmt.Errorf("--enabled must be true or false, got %q", enabled))
				}
				q.Set("enabled", enabled)
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: notifyNS, method: http.MethodGet, path: "/routes", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var list notifyRouteList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "no notification route is declared: nothing will be routed anywhere")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "ID\tNAME\tENABLED\tDESTINATION\tMIN SEV\tTYPES\tPRIORITY"); err != nil {
					return err
				}
				for _, r := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
						observeCell(r.ID), observeCell(r.Name),
						observeBool(r.Enabled, "yes", "NO"), observeCell(r.Destination),
						observeCell(r.MinSeverity), observeCell(observeJoinList(r.MatchTypes)),
						r.Priority); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares notify routes ls")
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&destination, "destination", "", "only routes targeting this destination")
	cmd.Flags().StringVar(&enabled, "enabled", "", "only enabled (true) or only disabled (false) routes")
	addObservePageFlags(cmd, &page)
	return cmd
}

func newNotifyRoutesGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <route-id>",
		Short:   "Show one route's full predicate",
		Long:    "get shows every dimension of one route's predicate, plus its dedup and throttle\nwindows and who authored it.",
		Example: "  olivares notify routes get rt-1\n  olivares notify routes get rt-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodGet,
				path: "/routes" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var r notifyRoute
			if err := res.decode(&r); err != nil {
				return err
			}
			return renderNotifyRoute(cmd, res.raw, r, "")
		},
	}
}

// notifyRouteFlags is the predicate, declared once for create and update.
type notifyRouteFlags struct {
	name              string
	destination       string
	minSeverity       string
	matchTypes        []string
	matchKinds        []string
	matchSources      []string
	matchSubjectKinds []string
	dedupWindow       int64
	throttleWindow    int64
	priority          int64
	enabled           bool
}

func addNotifyRouteFlags(cmd *cobra.Command, f *notifyRouteFlags, nameRequired bool) {
	nameHelp := "the route's name"
	if nameRequired {
		nameHelp += " (required, unique in the tenant)"
	}
	cmd.Flags().StringVar(&f.name, "name", "", nameHelp)
	cmd.Flags().StringVar(&f.destination, "destination", "", "the provisioned destination to send to (required; see `notify destinations`)")
	cmd.Flags().StringVar(&f.minSeverity, "min-severity", "", "severity floor: info, low, medium, high or critical (empty = no floor)")
	cmd.Flags().StringSliceVar(&f.matchTypes, "match-type", nil, "event type to match, repeatable (see `notify match-types`)")
	cmd.Flags().StringSliceVar(&f.matchKinds, "match-kind", nil, "finding kind to match, repeatable")
	cmd.Flags().StringSliceVar(&f.matchSources, "match-source", nil, "signal source to match, repeatable")
	cmd.Flags().StringSliceVar(&f.matchSubjectKinds, "match-subject-kind", nil, "subject kind to match, repeatable")
	cmd.Flags().Int64Var(&f.dedupWindow, "dedup-window", 0, "seconds within which an identical signal is suppressed")
	cmd.Flags().Int64Var(&f.throttleWindow, "throttle-window", 0, "seconds within which this route sends at most once")
	cmd.Flags().Int64Var(&f.priority, "priority", 0, "ordering among matching routes")
	cmd.Flags().BoolVar(&f.enabled, "enabled", true, "whether the route may fire")
}

func (f notifyRouteFlags) input(cmd *cobra.Command) (notifyRouteInput, error) {
	if strings.TrimSpace(f.destination) == "" {
		return notifyRouteInput{}, exitcode.New(exitcode.Usage, fmt.Errorf(
			"--destination is required: a route with no destination has nowhere to send"))
	}
	if f.minSeverity != "" && !inList(f.minSeverity, notifySeverities) {
		return notifyRouteInput{}, exitcode.New(exitcode.Usage, fmt.Errorf(
			"--min-severity must be empty or one of %v, got %q", notifySeverities, f.minSeverity))
	}
	if f.dedupWindow < 0 || f.throttleWindow < 0 {
		return notifyRouteInput{}, exitcode.New(exitcode.Usage, fmt.Errorf(
			"--dedup-window and --throttle-window must not be negative"))
	}
	in := notifyRouteInput{
		Name: f.name, Destination: f.destination, MinSeverity: f.minSeverity,
		MatchTypes: f.matchTypes, MatchKinds: f.matchKinds,
		MatchSources: f.matchSources, MatchSubjectKinds: f.matchSubjectKinds,
		DedupWindowSeconds: f.dedupWindow, ThrottleWindowSeconds: f.throttleWindow,
		Priority: f.priority,
	}
	// Sent ONLY when stated. The engine reads a nil `enabled` as "keep what is
	// stored", so defaulting the flag to true and always sending it would
	// re-enable every route quietly updated for an unrelated reason.
	if cmd.Flags().Changed("enabled") {
		v := f.enabled
		in.Enabled = &v
	}
	return in, nil
}

func newNotifyRoutesCreateCmd(flags *authClientFlags) *cobra.Command {
	var f notifyRouteFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Declare a notification route",
		Long: "create declares a route. The name is the natural key in the tenant, so creating\n" +
			"the same name twice conflicts rather than replacing.\n\n" +
			"A ROUTE WITH NO MATCH FILTERS MATCHES EVERYTHING that clears the severity floor.\n" +
			"That is occasionally what you want and usually not; `notify evaluate` will tell\n" +
			"you what a predicate selects before anything is delivered.\n\n" +
			"The destination must be provisioned for this tenant. `notify destinations` lists\n" +
			"the ones you may name.",
		Example: "  olivares notify routes create --name criticals --destination ops-slack --min-severity critical\n" +
			"  olivares notify routes create --name agent-drift --destination sec-email --match-type finding.created --match-kind access_drift",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(f.name) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf("--name is required"))
			}
			in, err := f.input(cmd)
			if err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodPost, path: "/routes", body: in,
			}.do(cmd)
			if err != nil {
				return err
			}
			var r notifyRoute
			if err := res.decode(&r); err != nil {
				return err
			}
			if strings.TrimSpace(r.ID) == "" {
				return exitcode.New(exitcode.Server, fmt.Errorf(
					"the control plane answered HTTP %d but returned no route id, so nothing can be confirmed as created",
					res.status))
			}
			return renderNotifyRoute(cmd, res.raw, r, "created route "+observeCell(r.ID))
		},
	}
	addNotifyRouteFlags(cmd, &f, true)
	return cmd
}

func newNotifyRoutesUpdateCmd(flags *authClientFlags) *cobra.Command {
	var f notifyRouteFlags
	cmd := &cobra.Command{
		Use:   "update <route-id>",
		Short: "Replace a route's predicate",
		Long: "update REPLACES the predicate. Every match dimension you do not pass is CLEARED,\n" +
			"not kept — so an update that means to add one severity floor and forgets\n" +
			"--match-type widens the route.\n\n" +
			"Two fields behave differently and deliberately: --enabled is sent only if you\n" +
			"pass it (the engine keeps the stored value otherwise), and the route's NAME is\n" +
			"its natural key and is not changed here.\n\n" +
			"Read `routes get` first, and remember that `routes revisions` plus `routes\n" +
			"restore` will undo this if it goes wrong.",
		Example: "  olivares notify routes update rt-1 --destination ops-slack --min-severity high\n" +
			"  olivares notify routes update rt-1 --destination ops-slack --enabled=false",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := f.input(cmd)
			if err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodPut,
				path: "/routes" + observeIDPath(args[0]), body: in,
			}.do(cmd)
			if err != nil {
				return err
			}
			var r notifyRoute
			if err := res.decode(&r); err != nil {
				return err
			}
			return renderNotifyRoute(cmd, res.raw, r, "updated route "+observeCell(r.ID))
		},
	}
	addNotifyRouteFlags(cmd, &f, false)
	return cmd
}

func newNotifyRoutesRemoveCmd(flags *authClientFlags) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <route-id>",
		Aliases: []string{"delete", "remove"},
		Short:   "Delete a route (admin-tier)",
		Long: "rm deletes a route. Signals it used to select stop being delivered, silently —\n" +
			"nothing fails, notifications simply stop arriving, which is the hardest kind of\n" +
			"outage to notice.\n\n" +
			"CONSIDER `update --enabled=false` INSTEAD: a disabled route keeps its predicate\n" +
			"and can be turned back on. The delete is a hard delete of the row; the engine\n" +
			"does snapshot it as a revision first, but the route id is gone.\n\n" +
			"Requires --yes in any non-interactive session.",
		Example: "  olivares notify routes rm rt-1 --yes\n  olivares notify routes rm rt-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmDestructive(cmd, yes, fmt.Sprintf(
				"delete notification route %s, after which the signals it selected are delivered nowhere", args[0])); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodDelete,
				path: "/routes" + observeIDPath(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w,
					"deleted route %s — signals it selected now go nowhere\n", observeCell(args[0]))
				return werr
			}, observeJSON(res.raw))
		},
	}
	addYesFlag(cmd, &yes)
	return cmd
}

type notifyTestResult struct {
	Destination string `json:"destination"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
}

func newNotifyRoutesTestCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "test <route-id>",
		Short: "Send a REAL test notification through a route (admin-tier)",
		Long: "test sends a synthetic notification through the route's actual destination and\n" +
			"records the attempt in the delivery ledger.\n\n" +
			"THIS LEAVES THE BUILDING. A real message arrives at a real Slack channel, inbox\n" +
			"or webhook, and whoever watches that destination will see it. It is admin-tier\n" +
			"and audited for that reason.\n\n" +
			"It is not destructive, so it takes no --yes — but it is not a dry run either.\n" +
			"`notify evaluate` is the dry run: it answers which routes a signal would select\n" +
			"and sends nothing.",
		Example: "  olivares notify routes test rt-1\n  olivares notify routes test rt-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodPost,
				path: "/routes" + observeIDPath(args[0]) + "/test",
			}.do(cmd)
			if err != nil {
				return err
			}
			var t notifyTestResult
			if err := res.decode(&t); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "destination\t%s\n", observeCell(t.Destination))
				fmt.Fprintf(tw, "status\t%s\n", observeCell(t.Status))
				fmt.Fprintf(tw, "detail\t%s\n", observeCell(t.Detail))
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

type notifyRevision struct {
	ID        string          `json:"id"`
	Op        string          `json:"op"`
	Snapshot  json.RawMessage `json:"snapshot"`
	Actor     string          `json:"actor"`
	ActorKind string          `json:"actor_kind"`
	At        string          `json:"at"`
}

type notifyRevisionList struct {
	Items []notifyRevision `json:"items"`
	observePage
}

func newNotifyRoutesRevisionsCmd(flags *authClientFlags) *cobra.Command {
	var page observePageFlags
	cmd := &cobra.Command{
		Use:     "revisions <route-id>",
		Aliases: []string{"history"},
		Short:   "List a route's revision ledger",
		Long: "revisions lists every change made to a route, oldest first, with who made it and\n" +
			"a full snapshot of the route as it was AFTER that change.\n\n" +
			"The revision ids here are what `routes restore` takes. Use `-o json` to read the\n" +
			"snapshots in full: the table shows who and when, not the whole predicate.",
		Example: "  olivares notify routes revisions rt-1\n" +
			"  olivares notify routes revisions rt-1 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodGet,
				path: "/routes" + observeIDPath(args[0]) + "/revisions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list notifyRevisionList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "this route has no recorded revisions")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "REVISION\tOP\tACTOR\tKIND\tAT"); err != nil {
					return err
				}
				for _, r := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						observeCell(r.ID), observeCell(r.Op), observeCell(r.Actor),
						observeCell(r.ActorKind), observeCell(r.At)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(w,
					"the full snapshot of each revision is in -o json; `routes restore` takes a revision id"); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares notify routes revisions "+args[0])
			}, observeJSON(res.raw))
		},
	}
	addObservePageFlags(cmd, &page)
	return cmd
}

type notifyRestoreInput struct {
	RevisionID string `json:"revision_id"`
}

func newNotifyRoutesRestoreCmd(flags *authClientFlags) *cobra.Command {
	var revisionID string
	cmd := &cobra.Command{
		Use:   "restore <route-id>",
		Short: "Put a route back to an earlier revision",
		Long: "restore rewrites a route to the snapshot stored in one of its revisions. It is\n" +
			"the undo for an `update` that widened or broke a predicate.\n\n" +
			"IT IS ITSELF A CHANGE, and appends a revision of its own — so restoring is\n" +
			"reversible on the same terms, and the ledger records that a restore happened\n" +
			"rather than making the route look as though it was never changed.\n\n" +
			"--revision-id is required; `routes revisions` lists them.",
		Example: "  olivares notify routes restore rt-1 --revision-id rev-3\n" +
			"  olivares notify routes restore rt-1 --revision-id rev-3 -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(revisionID) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--revision-id is required: list them with `olivares notify routes revisions %s`", args[0]))
			}
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodPost,
				path: "/routes" + observeIDPath(args[0]) + "/restore",
				body: notifyRestoreInput{RevisionID: revisionID},
			}.do(cmd)
			if err != nil {
				return err
			}
			var r notifyRoute
			if err := res.decode(&r); err != nil {
				return err
			}
			return renderNotifyRoute(cmd, res.raw, r,
				"restored route "+observeCell(args[0])+" to revision "+observeCell(revisionID))
		},
	}
	cmd.Flags().StringVar(&revisionID, "revision-id", "", "the revision to restore (required)")
	return cmd
}

// ---- match-types / destinations ------------------------------------------------------

type notifyMatchType struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type notifyMatchTypes struct {
	MatchTypes []notifyMatchType `json:"match_types"`
}

func newNotifyMatchTypesCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "match-types",
		Aliases: []string{"types"},
		Short:   "List the event types a route may match",
		Long: "match-types is the vocabulary --match-type accepts. It is the authoritative\n" +
			"list this build routes: a type absent here is not one the engine will ever\n" +
			"deliver, whatever a route names.",
		Example: "  olivares notify match-types\n  olivares notify match-types -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: notifyNS, method: http.MethodGet, path: "/match-types"}.do(cmd)
			if err != nil {
				return err
			}
			var m notifyMatchTypes
			if err := res.decode(&m); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(m.MatchTypes) == 0 {
					_, err := fmt.Fprintln(w, "this build routes no event types")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "TYPE\tDESCRIPTION"); err != nil {
					return err
				}
				for _, t := range m.MatchTypes {
					if _, err := fmt.Fprintf(tw, "%s\t%s\n",
						observeCell(t.Type), observeCell(t.Description)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

type notifyDestinations struct {
	Destinations []string `json:"destinations"`
}

func newNotifyDestinationsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "destinations",
		Aliases: []string{"dests"},
		Short:   "List the destinations THIS tenant may address",
		Long: "destinations lists the provisioned destination NAMES this tenant can route to.\n" +
			"It never returns a credential — a route stores only a name.\n\n" +
			"The list is scoped to your tenant, deliberately: destination names are what a\n" +
			"route addresses, so showing another tenant's names would hand you the ability to\n" +
			"address them.\n\n" +
			"An empty list means no destination is configured yet. Routes may still be\n" +
			"authored — with nothing wired, nothing delivers and the ledger records why.",
		Example: "  olivares notify destinations\n  olivares notify destinations -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: notifyNS, method: http.MethodGet, path: "/destinations"}.do(cmd)
			if err != nil {
				return err
			}
			var d notifyDestinations
			if err := res.decode(&d); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(d.Destinations) == 0 {
					_, err := fmt.Fprintln(w,
						"no destination is provisioned for this tenant: routes can be authored, but nothing will deliver")
					return err
				}
				for _, name := range d.Destinations {
					if _, err := fmt.Fprintln(w, observeCell(name)); err != nil {
						return err
					}
				}
				return nil
			}, observeJSON(res.raw))
		},
	}
}

// ---- evaluate ----------------------------------------------------------------------

type notifyEvaluateInput struct {
	EventType   string `json:"event_type"`
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Source      string `json:"source"`
	SubjectKind string `json:"subject_kind"`
}

type notifyEvaluateVerdict struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Enabled    bool     `json:"enabled"`
	Matched    bool     `json:"matched"`
	Mismatches []string `json:"mismatches"`
}

type notifyEvaluateResponse struct {
	Items        []notifyEvaluateVerdict `json:"items"`
	MatchedCount int                     `json:"matched_count"`
}

func newNotifyEvaluateCmd(flags *authClientFlags) *cobra.Command {
	var in notifyEvaluateInput
	cmd := &cobra.Command{
		Use:     "evaluate",
		Aliases: []string{"eval", "dry-run"},
		Short:   "Ask which routes a signal WOULD select, delivering nothing",
		Long: "evaluate is the dry run. Describe a signal by its predicate dimensions and the\n" +
			"engine answers which routes select it and, for those that do not, which\n" +
			"dimension rejected it. NOTHING IS DELIVERED, RECORDED OR CLAIMED.\n\n" +
			"IT SIMULATES THE PREDICATE ONLY. Dedup, throttling and the claim phase depend on\n" +
			"ledger history rather than on the rule, so they are deliberately not simulated:\n" +
			"a route shown as matching here may still be suppressed by its own dedup window\n" +
			"when a real signal arrives.\n\n" +
			"A route that matches but is DISABLED is reported as such — it would select the\n" +
			"signal and cannot fire.",
		Example: "  olivares notify evaluate --event-type finding.created --severity critical\n" +
			"  olivares notify evaluate --event-type finding.created --kind access_drift --source pg_audit -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(in.EventType) == "" {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--event-type is required: without it there is no signal to evaluate (see `notify match-types`)"))
			}
			if in.Severity != "" && !inList(in.Severity, notifySeverities) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--severity must be empty or one of %v, got %q", notifySeverities, in.Severity))
			}
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodPost, path: "/routes/evaluate", body: in,
			}.do(cmd)
			if err != nil {
				return err
			}
			var out notifyEvaluateResponse
			if err := res.decode(&out); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(out.Items) == 0 {
					_, err := fmt.Fprintln(w, "no route exists to evaluate this signal against")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "ROUTE\tNAME\tMATCHED\tENABLED\tWHY NOT"); err != nil {
					return err
				}
				for _, v := range out.Items {
					why := observeJoinList(v.Mismatches)
					if v.Matched && !v.Enabled {
						why = "matches but the route is DISABLED and cannot fire"
					}
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						observeCell(v.ID), observeCell(v.Name),
						observeBool(v.Matched, "yes", "no"),
						observeBool(v.Enabled, "yes", "NO"),
						observeCell(why)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				_, err := fmt.Fprintf(w,
					"%d of %d route(s) select this signal — predicate only: dedup and throttling are NOT simulated\n",
					out.MatchedCount, len(out.Items))
				return err
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&in.EventType, "event-type", "", "the signal's event type (required)")
	cmd.Flags().StringVar(&in.Kind, "kind", "", "the finding kind")
	cmd.Flags().StringVar(&in.Severity, "severity", "", "the signal's severity")
	cmd.Flags().StringVar(&in.Source, "source", "", "the signal's source")
	cmd.Flags().StringVar(&in.SubjectKind, "subject-kind", "", "the subject's kind")
	return cmd
}

// ---- deliveries -----------------------------------------------------------------------

type notifyDelivery struct {
	ID          string `json:"id"`
	RouteRef    string `json:"route_ref,omitempty"`
	Destination string `json:"destination"`
	EventType   string `json:"event_type"`
	FindingKind string `json:"finding_kind"`
	Severity    string `json:"severity,omitempty"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	Title       string `json:"title,omitempty"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

type notifyDeliveryList struct {
	Items []notifyDelivery `json:"items"`
	observePage
}

func newNotifyDeliveriesCmd(flags *authClientFlags) *cobra.Command {
	var status, findingKind, destination, route string
	var page observePageFlags
	cmd := &cobra.Command{
		Use:     "deliveries",
		Aliases: []string{"ledger"},
		Short:   "List the append-only delivery ledger",
		Long: "deliveries is the append-only record of every delivery ATTEMPT: what was sent,\n" +
			"through which route, to which destination, and how it went.\n\n" +
			"It is the answer to \"did this notification go out\", and because it is\n" +
			"append-only it is also the answer to \"did it go out three weeks ago\". A status\n" +
			"of no_dispatcher means no transport was wired — the signal matched and there was\n" +
			"nowhere to send it.",
		Example: "  olivares notify deliveries\n" +
			"  olivares notify deliveries --status failed\n" +
			"  olivares notify deliveries --route rt-1 --limit 100 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for _, kv := range []struct{ k, v string }{
				{"status", status}, {"finding_kind", findingKind},
				{"destination", destination}, {"route", route},
			} {
				if kv.v != "" {
					q.Set(kv.k, kv.v)
				}
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: notifyNS, method: http.MethodGet, path: "/deliveries", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var list notifyDeliveryList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "no delivery attempt matches this query")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "OCCURRED\tSTATUS\tDESTINATION\tTYPE\tKIND\tSEVERITY\tSUBJECT\tDETAIL"); err != nil {
					return err
				}
				for _, d := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(d.OccurredAt), observeCell(d.Status), observeCell(d.Destination),
						observeCell(d.EventType), observeCell(d.FindingKind), observeCell(d.Severity),
						healthSubject(d.SubjectKind, d.SubjectRef), observeCell(d.Detail)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares notify deliveries")
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by delivery status")
	cmd.Flags().StringVar(&findingKind, "finding-kind", "", "filter by finding kind")
	cmd.Flags().StringVar(&destination, "destination", "", "filter by destination")
	cmd.Flags().StringVar(&route, "route", "", "filter by route id")
	addObservePageFlags(cmd, &page)
	return cmd
}

// ---- outbox --------------------------------------------------------------------------

type notifyOutboxRow struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Attempts    int64  `json:"attempts"`
	Destination string `json:"destination"`
	EventType   string `json:"event_type"`
	FindingKind string `json:"finding_kind"`
	Severity    string `json:"severity,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	Title       string `json:"title,omitempty"`
	LastDetail  string `json:"last_detail,omitempty"`
	NextAttempt string `json:"next_attempt_at,omitempty"`
	LastAttempt string `json:"last_attempt_at,omitempty"`
	OccurredAt  string `json:"occurred_at"`
	RouteRef    string `json:"route_ref,omitempty"`
}

type notifyOutboxList struct {
	Items []notifyOutboxRow `json:"items"`
	observePage
}

func newNotifyOutboxCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "outbox",
		Aliases: []string{"dlq"},
		Short:   "Inspect the durable outbox and requeue terminal rows",
		Long: "outbox is the durable queue behind delivery: what is waiting, what is in flight,\n" +
			"what was delivered and what dead-lettered after exhausting its retries.\n\n" +
			"`--status dead` is the dead-letter view — the notifications that never arrived\n" +
			"and are waiting for someone to decide.",
		Example: "  olivares notify outbox ls --status dead\n" +
			"  olivares notify outbox redeliver ob-1",
	}
	cmd.AddCommand(newNotifyOutboxListCmd(flags), newNotifyOutboxRedeliverCmd(flags))
	return cmd
}

func newNotifyOutboxListCmd(flags *authClientFlags) *cobra.Command {
	var status, destination string
	var page observePageFlags
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List durable outbox rows",
		Long: "ls lists the outbox. Statuses are queued, delivering, delivered and dead.\n\n" +
			"ATTEMPTS AND NEXT ATTEMPT ARE THE TWO COLUMNS THAT EXPLAIN A STUCK QUEUE: a row\n" +
			"with rising attempts and a receding next-attempt time is backing off, not\n" +
			"forgotten.",
		Example: "  olivares notify outbox ls\n" +
			"  olivares notify outbox ls --status dead\n" +
			"  olivares notify outbox ls --destination ops-slack -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			if destination != "" {
				q.Set("destination", destination)
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{flags: flags, ns: notifyNS, method: http.MethodGet, path: "/outbox", query: q}.do(cmd)
			if err != nil {
				return err
			}
			var list notifyOutboxList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(w, "the outbox holds nothing matching this query")
					return err
				}
				tw := newTabWriter(w)
				if _, err := fmt.Fprintln(tw, "ID\tSTATUS\tATTEMPTS\tDESTINATION\tTYPE\tSEVERITY\tNEXT ATTEMPT\tLAST DETAIL"); err != nil {
					return err
				}
				for _, r := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(r.ID), observeCell(r.Status), r.Attempts,
						observeCell(r.Destination), observeCell(r.EventType),
						observeCell(r.Severity), observeCell(r.NextAttempt),
						observeCell(r.LastDetail)); err != nil {
						return err
					}
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				return observeTruncationNote(w, list.observePage, "olivares notify outbox ls")
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status: queued, delivering, delivered or dead")
	cmd.Flags().StringVar(&destination, "destination", "", "filter by destination")
	addObservePageFlags(cmd, &page)
	return cmd
}

func newNotifyOutboxRedeliverCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "redeliver <outbox-id>",
		Aliases: []string{"requeue"},
		Short:   "Requeue a terminal outbox row for another delivery attempt (admin-tier)",
		Long: "redeliver requeues a TERMINAL outbox row: attempts reset to zero and the next\n" +
			"pump sends it again. It is how a dead-letter queue is drained after the\n" +
			"destination is fixed.\n\n" +
			"IT ACCEPTS A `delivered` ROW TOO, AND THAT IS DELIBERATE — it is the\n" +
			"ack-and-retry case for a notification that arrived but was never acted on. The\n" +
			"consequence is worth stating plainly: redelivering a delivered row SENDS A\n" +
			"DUPLICATE to a real destination.\n\n" +
			"A queued or delivering row is in flight and is refused with a conflict, because\n" +
			"requeuing it would race the owner writing its outcome.\n\n" +
			"Admin-tier and audited. Not destructive, so no --yes.",
		Example: "  olivares notify outbox redeliver ob-1\n  olivares notify outbox redeliver ob-1 -o json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: notifyNS, method: http.MethodPost,
				path: "/outbox" + observeIDPath(args[0]) + "/redeliver",
			}.do(cmd)
			if err != nil {
				return err
			}
			var r notifyOutboxRow
			if err := res.decode(&r); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w,
					"requeued %s for delivery to %s — it will be sent again by the next pump\n",
					observeCell(args[0]), observeCell(r.Destination)); err != nil {
					return err
				}
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "status\t%s\n", observeCell(r.Status))
				fmt.Fprintf(tw, "attempts\t%d\n", r.Attempts)
				fmt.Fprintf(tw, "next attempt\t%s\n", observeCell(r.NextAttempt))
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}
