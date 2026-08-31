// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// `olivares governance` — THE LARGEST API FAMILY WITH NO OPERATOR SURFACE.
//
// Measured 2026-08-21 against the OpenAPI and the built command tree: /v1/m/governance
// serves 69 routes across 17 sub-families, and the binary exposed a verb for NONE of them.
// That is not the same as "the CLI cannot reach governance" — four sub-families are already
// consumed as INTERNAL PLUMBING by other commands (approvals from approvalbridge.go,
// erasegate.go and hitl.go; breakglass from approvalbridge.go; pdp from cmd_hookpep.go;
// agents from deployidentity.go and cmd_quickstart_governed_rag.go), and those four files
// declare ZERO cobra commands of their own. The code can get there. The operator cannot.
//
// WHY KILL-SWITCH FIRST, and it is not because it is the biggest — it is the smallest of the
// six-route sub-families. Two reasons, both measured:
//
//   - It is what an operator needs WITHOUT A BROWSER during an incident: "what is stopped
//     right now, and why". Today that answer exists only in the console.
//   - It is the only governance sub-family that NO production CLI code touches — the two
//     places that call /killswitch are e2e tests (killswitch_e2e_test.go,
//     e2e_claude_real_test.go), which drive the API directly. So there is no existing client
//     to wrap and it has to set the pattern. `breakglass`, by contrast, already has one in
//     approvalbridge.go: wrapping it is cheaper, and it goes second precisely for that.
//
// This first increment is READ-ONLY on purpose. `engage`, `reenable` and `review` change
// enforcement state and one of them (reenable) can route through an approval, so they want
// their own witnesses and their own confirmation story rather than riding in behind a list.
const governanceNS = "governance"

func newGovernanceCmd() *cobra.Command {
	flags := &authClientFlags{}
	root := &cobra.Command{
		Use:   "governance",
		Short: "Inspect the governance plane: what is stopped, and why",
		Long: "governance is the enforcement plane: kill switches, break-glass grants, approvals and\n" +
			"policy decisions. This command reads it. The namespace is the same word the API uses\n" +
			"(/v1/m/governance), so a route and its verb are always one translation apart.",
		Example: "  olivares governance killswitch state\n" +
			"  olivares governance killswitch ls --status active -o json",
	}
	flags.addPersistent(root)
	root.AddCommand(governanceKillSwitchCmd(flags), governanceBreakGlassCmd(flags), governancePdpCmd(flags), governanceRBACCmd(flags), governanceNHICmd(flags),
		governanceApprovalsCmd(flags), governanceGuardianCmd(flags))
	return root
}

func governanceKillSwitchCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "killswitch",
		Short: "The estate-wide and per-scope stops that deny work while they are active",
		Example: "  olivares governance killswitch state\n" +
			"  olivares governance killswitch ls --status active",
		Long: "A kill switch is the hard stop: while one is active for a scope, the enforcement plane\n" +
			"denies that scope's work regardless of any other policy. Read it here to answer the\n" +
			"first question of an incident — what is stopped, since when, and on whose authority.",
	}
	cmd.AddCommand(governanceKillSwitchStateCmd(flags), governanceKillSwitchListCmd(flags))
	return cmd
}

// cliKillSwitch mirrors modules/governance/killswitch.go killSwitchDTO. Only the fields this
// surface renders or prints as JSON are declared; the raw body is what `-o json` emits, so a
// field added upstream reaches the operator whether or not this struct grows.
type cliKillSwitch struct {
	ID              string `json:"id"`
	ScopeKind       string `json:"scope_kind"`
	ScopeRef        string `json:"scope_ref,omitempty"`
	AgentExternalID string `json:"agent_external_id,omitempty"`
	Status          string `json:"status"`
	Reason          string `json:"reason,omitempty"`
	Source          string `json:"source"`
	EngagedBy       string `json:"engaged_by,omitempty"`
	EngagedAt       string `json:"engaged_at,omitempty"`
	Reviewed        bool   `json:"reviewed"`
}

type cliKillSwitchState struct {
	EstateStopped bool            `json:"estate_stopped"`
	Active        []cliKillSwitch `json:"active"`
}

type cliKillSwitchList struct {
	Items   []cliKillSwitch `json:"items"`
	Cursor  string          `json:"cursor,omitempty"`
	HasMore bool            `json:"has_more,omitempty"`
}

// governanceKillSwitchStateCmd answers the incident question in one call.
//
// It exists SEPARATELY from `ls --status active` even though the two overlap, because the
// state route carries one fact the list cannot: `estate_stopped`. An estate-wide stop is not
// a row in the collection — it is a property of the whole plane — so a caller who only listed
// active switches could see an empty list while the entire estate is halted.
func governanceKillSwitchStateCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "state",
		Short: "Whether the estate is stopped, and every kill switch active right now",
		Long: "Report the enforcement plane's stop state in one call: whether the WHOLE estate is\n" +
			"halted, and the kill switches currently active. The estate flag is not derivable from\n" +
			"the list — an estate-wide stop is a property of the plane, not a row in it.",
		Example: "  olivares governance killswitch state\n  olivares governance killswitch state -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/killswitch/state",
			}.do(cmd)
			if err != nil {
				return err
			}
			var st cliKillSwitchState
			if err := res.decode(&st); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if st.EstateStopped {
					if _, err := fmt.Fprintln(out, "ESTATE STOPPED — every scope is denied"); err != nil {
						return err
					}
				} else if _, err := fmt.Fprintln(out, "estate running"); err != nil {
					return err
				}
				if len(st.Active) == 0 {
					_, err := fmt.Fprintln(out, "no kill switch is active")
					return err
				}
				return writeKillSwitchTable(out, st.Active)
			}, observeJSON(res.raw))
		},
	}
}

func governanceKillSwitchListCmd(flags *authClientFlags) *cobra.Command {
	var status string
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List kill switches, active and historical",
		Long: "List the kill switches this tenant has, newest first, with paging. Without --status the\n" +
			"set includes the ones already re-enabled, which is what an audit of the incident needs;\n" +
			"pass --status active for the ones denying work right now.",
		Example: "  olivares governance killswitch ls\n" +
			"  olivares governance killswitch ls --status active --limit 20",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			// --limit/--cursor are declared because THIS route reads them:
			// modules/governance/killswitch.go:464 calls listQuery(r), and that helper
			// (modules/governance/helpers.go:105) parses `limit` and `cursor`. The
			// transport's own comment warns that bolting them onto a route that ignores
			// them turns the second page into the first page forever.
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/killswitch", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliKillSwitchList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no kill switches match")
					return err
				}
				if err := writeKillSwitchTable(out, list.Items); err != nil {
					return err
				}
				return observeTruncationNote(out, observePage{Cursor: list.Cursor, HasMore: list.HasMore}, cmd.CommandPath())
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "only switches in this status (e.g. active)")
	addObservePageFlags(cmd, page)
	return cmd
}

func writeKillSwitchTable(out io.Writer, rows []cliKillSwitch) error {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tSCOPE\tTARGET\tSTATUS\tSOURCE\tENGAGED BY\tENGAGED AT\tREVIEWED\tREASON"); err != nil {
		return err
	}
	for _, k := range rows {
		target := k.ScopeRef
		if target == "" {
			target = k.AgentExternalID
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			observeCell(k.ID), observeCell(k.ScopeKind), observeCell(target),
			observeCell(k.Status), observeCell(k.Source), observeCell(k.EngagedBy),
			observeCell(k.EngagedAt), observeBool(k.Reviewed, "yes", "no"),
			observeCell(k.Reason)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// `governance breakglass` — the SECOND slice, and it is cheaper than the first for a reason worth
// writing down: unlike kill-switch, this sub-family ALREADY has a client in the tree
// (approvalbridge.go drives /breakglass and /breakglass/consume). Nothing here is new transport;
// it is operator surface over a route the binary was already speaking to.
//
// Read-only, same as kill-switch. `activate`, `consume`, `review` and `revoke` grant or withdraw
// emergency access, which is the one thing in this plane that must never happen as a side effect
// of exploring it.
func governanceBreakGlassCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "breakglass",
		Short:   "Emergency access grants: who has one, until when, and what they did with it",
		Example: "  olivares governance breakglass ls",
		Long: "A break-glass grant is deliberate, time-boxed permission to do something the policy\n" +
			"otherwise denies. Reading it answers the two questions an audit asks: which grants are\n" +
			"live right now, and which actions were actually taken under each one.",
	}
	cmd.AddCommand(governanceBreakGlassListCmd(flags), governanceBreakGlassGetCmd(flags),
		governanceBreakGlassUsesCmd(flags))
	return cmd
}

// cliBreakGlass mirrors modules/governance/breakglass.go breakGlassDTO.
type cliBreakGlass struct {
	ID          string `json:"id"`
	MatchAction string `json:"match_action,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ActivatedBy string `json:"activated_by,omitempty"`
	Status      string `json:"status"`
	ActivatedAt string `json:"activated_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
	UseCount    int64  `json:"use_count"`
	Reviewed    bool   `json:"reviewed"`
}

type cliBreakGlassList struct {
	Items   []cliBreakGlass `json:"items"`
	Cursor  string          `json:"cursor,omitempty"`
	HasMore bool            `json:"has_more,omitempty"`
}

// cliBreakGlassUse mirrors breakGlassUseDTO: one action taken under one grant.
type cliBreakGlassUse struct {
	GrantID     string `json:"grant_id"`
	Action      string `json:"action"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	UsedBy      string `json:"used_by,omitempty"`
	UsedAt      string `json:"used_at,omitempty"`
}

type cliBreakGlassUseList struct {
	Items []cliBreakGlassUse `json:"items"`
}

func governanceBreakGlassListCmd(flags *authClientFlags) *cobra.Command {
	var status string
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List break-glass grants, live and expired",
		Long: "List this tenant's break-glass grants with paging. Without --status the set includes\n" +
			"expired and revoked grants, which is what an audit wants; pass --status active for the\n" +
			"ones that can be used right now.",
		Example: "  olivares governance breakglass ls --status active\n" +
			"  olivares governance breakglass ls --limit 20 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			// Paging is declared HERE because handleListBreakGlass calls listQuery(r), which
			// parses limit and cursor. `uses` below deliberately declares neither — see there.
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/breakglass", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliBreakGlassList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no break-glass grants match")
					return err
				}
				if err := writeBreakGlassTable(out, list.Items); err != nil {
					return err
				}
				return observeTruncationNote(out, observePage{Cursor: list.Cursor, HasMore: list.HasMore}, cmd.CommandPath())
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "only grants in this status (e.g. active)")
	addObservePageFlags(cmd, page)
	return cmd
}

func governanceBreakGlassGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <grant-id>",
		Short:   "Show one break-glass grant",
		Long:    "Show a single grant: what it permits, who activated it and why, and when it expires.",
		Example: "  olivares governance breakglass get 01890000-0000-7000-8000-000000000001",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The id is escaped as ONE path segment. A value carrying `/` or `?` would
			// otherwise re-target the request at a different route of the same plane.
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/breakglass/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var g cliBreakGlass
			if err := res.decode(&g); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				return writeBreakGlassTable(out, []cliBreakGlass{g})
			}, observeJSON(res.raw))
		},
	}
}

// governanceBreakGlassUsesCmd declares NO paging flags, and that is measured rather than an
// oversight: handleListBreakGlassUses calls listAll and reads no query parameter at all, so the
// set comes back complete. A --cursor here would be accepted, dropped by the engine, and the
// second page would be the first page forever.
func governanceBreakGlassUsesCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "uses <grant-id>",
		Short: "Every action actually taken under one grant",
		Long: "List what was done with a grant. A grant that exists and was never used is a very\n" +
			"different fact from one used forty times, and only this route can tell them apart.\n" +
			"The set is returned complete, without paging.",
		Example: "  olivares governance breakglass uses 01890000-0000-7000-8000-000000000001",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/breakglass/" + url.PathEscape(args[0]) + "/uses",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliBreakGlassUseList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "this grant was never used")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ACTION\tSUBJECT\tREF\tUSED BY\tUSED AT"); err != nil {
					return err
				}
				for _, u := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						observeCell(u.Action), observeCell(u.SubjectKind), observeCell(u.SubjectRef),
						observeCell(u.UsedBy), observeCell(u.UsedAt)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

func writeBreakGlassTable(out io.Writer, rows []cliBreakGlass) error {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tACTION\tSTATUS\tACTIVATED BY\tACTIVATED AT\tEXPIRES\tUSES\tREVIEWED\tREASON"); err != nil {
		return err
	}
	for _, g := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			observeCell(g.ID), observeCell(g.MatchAction), observeCell(g.Status),
			observeCell(g.ActivatedBy), observeCell(g.ActivatedAt), observeCell(g.ExpiresAt),
			g.UseCount, observeBool(g.Reviewed, "yes", "no"), observeCell(g.Reason)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// `governance approvals` — the third slice, and the one with the most consumers already inside
// the binary: approvalbridge.go, erasegate.go and hitl.go all drive /approvals, and none of the
// three declares a cobra command. The engine has been asking for approvals for a long time; the
// operator could not read the queue.
//
// Read-only. `decide`, `cancel`, `consume` and `sweep` all move an approval, and `decide` is the
// one action in this plane whose whole point is that a person did it deliberately.
func governanceApprovalsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "approvals",
		Short:   "The approval queue: what is waiting on a human, and who decided what",
		Example: "  olivares governance approvals ls",
		Long: "An approval is a gate that a person has to pass before an action runs. Reading it\n" +
			"answers what is pending and on whom, and — through `decisions` — who voted which way\n" +
			"and why, which is the record an audit actually asks for.",
	}
	cmd.AddCommand(governanceApprovalsListCmd(flags), governanceApprovalsGetCmd(flags),
		governanceApprovalsDecisionsCmd(flags))
	return cmd
}

// cliApproval mirrors modules/governance/approvals.go approvalDTO.
type cliApproval struct {
	ID                string `json:"id"`
	SubjectKind       string `json:"subject_kind,omitempty"`
	SubjectRef        string `json:"subject_ref,omitempty"`
	Action            string `json:"action,omitempty"`
	RequestedBy       string `json:"requested_by,omitempty"`
	Status            string `json:"status"`
	RiskTier          string `json:"risk_tier"`
	RequiredApprovals int64  `json:"required_approvals"`
	ApproveCount      int64  `json:"approve_count"`
	RejectCount       int64  `json:"reject_count"`
	ExpiresAt         string `json:"expires_at,omitempty"`
	Escalated         bool   `json:"escalated"`
	Reason            string `json:"reason,omitempty"`
}

type cliApprovalList struct {
	Items   []cliApproval `json:"items"`
	Cursor  string        `json:"cursor,omitempty"`
	HasMore bool          `json:"has_more,omitempty"`
}

// cliDecision mirrors decisionDTO: one vote on one approval.
type cliDecision struct {
	Decision    string `json:"decision"`
	Decider     string `json:"decider"`
	DeciderUser string `json:"decider_user,omitempty"`
	DecidedAt   string `json:"decided_at,omitempty"`
	Note        string `json:"note,omitempty"`
}

type cliDecisionList struct {
	Items []cliDecision `json:"items"`
}

func governanceApprovalsListCmd(flags *authClientFlags) *cobra.Command {
	var status, action string
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List approvals, pending and decided",
		Long: "List this tenant's approvals with paging. Two filters, both read by the engine:\n" +
			"--status narrows to a state (pending is the one an operator wants first) and --action\n" +
			"narrows to the operation being gated.",
		Example: "  olivares governance approvals ls --status pending\n" +
			"  olivares governance approvals ls --action security.enforcement.enable -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			if action != "" {
				q.Set("action", action)
			}
			// Paging declared because handleListApprovals calls listQuery. `decisions` below
			// does not, for the same measured reason as `breakglass uses`.
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/approvals", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliApprovalList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no approvals match")
					return err
				}
				if err := writeApprovalTable(out, list.Items); err != nil {
					return err
				}
				return observeTruncationNote(out, observePage{Cursor: list.Cursor, HasMore: list.HasMore}, cmd.CommandPath())
			}, observeJSON(res.raw))
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "only approvals in this status (e.g. pending)")
	cmd.Flags().StringVar(&action, "action", "", "only approvals gating this action")
	addObservePageFlags(cmd, page)
	return cmd
}

func governanceApprovalsGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "get <approval-id>",
		Short:   "Show one approval",
		Long:    "Show a single approval: what it gates, who asked, how many votes it needs and has.",
		Example: "  olivares governance approvals get 01890000-0000-7000-8000-000000000001",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/approvals/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var a cliApproval
			if err := res.decode(&a); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				return writeApprovalTable(out, []cliApproval{a})
			}, observeJSON(res.raw))
		},
	}
}

// governanceApprovalsDecisionsCmd declares NO paging: handleListDecisions calls listAll and reads
// no query parameter, so the votes come back complete. Same measurement as `breakglass uses`, and
// the same reason it is checked per HANDLER rather than per family.
func governanceApprovalsDecisionsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "decisions <approval-id>",
		Short: "Who voted which way on one approval, and why",
		Long: "List the votes cast on one approval. The counts on the approval itself say HOW MANY\n" +
			"approved; only this says WHO and on what grounds, which is what an audit asks for.\n" +
			"The set is returned complete, without paging.",
		Example: "  olivares governance approvals decisions 01890000-0000-7000-8000-000000000001",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/approvals/" + url.PathEscape(args[0]) + "/decisions",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliDecisionList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "nobody has voted on this approval yet")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "DECISION\tDECIDER\tUSER\tDECIDED AT\tNOTE"); err != nil {
					return err
				}
				for _, d := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						observeCell(d.Decision), observeCell(d.Decider), observeCell(d.DeciderUser),
						observeCell(d.DecidedAt), observeCell(d.Note)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

func writeApprovalTable(out io.Writer, rows []cliApproval) error {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tACTION\tSUBJECT\tSTATUS\tTIER\tVOTES\tREQUESTED BY\tEXPIRES\tESCALATED"); err != nil {
		return err
	}
	for _, a := range rows {
		subject := a.SubjectRef
		if subject == "" {
			subject = a.SubjectKind
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d+/%d- of %d\t%s\t%s\t%s\n",
			observeCell(a.ID), observeCell(a.Action), observeCell(subject),
			observeCell(a.Status), observeCell(a.RiskTier),
			a.ApproveCount, a.RejectCount, a.RequiredApprovals,
			observeCell(a.RequestedBy), observeCell(a.ExpiresAt),
			observeBool(a.Escalated, "yes", "no")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// `governance guardian` — the automatic arm of the plane: rules that turn a finding into an
// action without a human in the loop. Reading it answers the question the other three cannot:
// what did the system decide BY ITSELF, and under which rule.
//
// Read-only. Creating, editing or deleting a rule changes what the plane will do unattended from
// that moment on — the one class of change that has no business riding in behind a listing.
func governanceGuardianCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "guardian",
		Short:   "The rules that act on findings without a human, and what they have done",
		Example: "  olivares governance guardian rules",
		Long: "Guardian turns a finding into an action — an approval, a kill switch — under a rule and\n" +
			"a mode. `rules` is what it is allowed to do; `actions` is what it actually did.",
	}
	cmd.AddCommand(governanceGuardianRulesCmd(flags), governanceGuardianActionsCmd(flags))
	return cmd
}

// cliGuardianRule mirrors modules/governance/guardian.go guardianRuleDTO.
type cliGuardianRule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	MatchKinds  string `json:"match_kinds,omitempty"`
	MinSeverity string `json:"min_severity"`
	Action      string `json:"action"`
	Mode        string `json:"mode"`
	AgentTier   string `json:"agent_tier,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	Note        string `json:"note,omitempty"`
}

type cliGuardianRuleList struct {
	Items   []cliGuardianRule `json:"items"`
	Cursor  string            `json:"cursor,omitempty"`
	HasMore bool              `json:"has_more,omitempty"`
}

// cliGuardianAction mirrors guardianActionDTO: one thing the plane did on its own.
type cliGuardianAction struct {
	RuleName     string `json:"rule_name"`
	FindingKind  string `json:"finding_kind"`
	Severity     string `json:"finding_severity"`
	TargetKind   string `json:"target_kind"`
	TargetRef    string `json:"target_ref,omitempty"`
	Action       string `json:"action"`
	Mode         string `json:"mode"`
	Status       string `json:"status"`
	ApprovalID   string `json:"approval_id,omitempty"`
	KillswitchID string `json:"killswitch_id,omitempty"`
	ExecutedAt   string `json:"executed_at,omitempty"`
}

type cliGuardianActionList struct {
	Items   []cliGuardianAction `json:"items"`
	Cursor  string              `json:"cursor,omitempty"`
	HasMore bool                `json:"has_more,omitempty"`
}

func governanceGuardianRulesCmd(flags *authClientFlags) *cobra.Command {
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "List the guardian rules and whether each is armed",
		Long: "List what guardian is allowed to do on its own: which finding kinds each rule matches,\n" +
			"from which severity, what it then does, and in which mode. `enabled` is the difference\n" +
			"between a rule that is written down and a rule that will fire.",
		Example: "  olivares governance guardian rules\n  olivares governance guardian rules -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			// handleListGuardianRules calls listQuery, so both flags are honored. It reads no
			// other filter — there is no --enabled, because the engine offers none and a flag
			// the engine drops is worse than a missing one.
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/guardian/rules", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliGuardianRuleList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no guardian rules")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ID\tNAME\tARMED\tMATCHES\tMIN SEVERITY\tACTION\tMODE\tTIER"); err != nil {
					return err
				}
				for _, r := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(r.ID), observeCell(r.Name), observeBool(r.Enabled, "yes", "no"),
						observeCell(r.MatchKinds), observeCell(r.MinSeverity), observeCell(r.Action),
						observeCell(r.Mode), observeCell(r.AgentTier)); err != nil {
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

func governanceGuardianActionsCmd(flags *authClientFlags) *cobra.Command {
	var status string
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:   "actions",
		Short: "What guardian actually did, rule by rule",
		Long: "List the actions guardian took on its own: which finding triggered which rule, what it\n" +
			"did to what, and whether that produced an approval or a kill switch. This is the only\n" +
			"place the unattended decisions of the plane are visible as a list.",
		Example: "  olivares governance guardian actions --status executed\n" +
			"  olivares governance guardian actions --limit 50 -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if status != "" {
				q.Set("status", status)
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/guardian/actions", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliGuardianActionList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "guardian has taken no action")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "RULE\tFINDING\tSEVERITY\tTARGET\tACTION\tMODE\tSTATUS\tAPPROVAL\tKILLSWITCH\tAT"); err != nil {
					return err
				}
				for _, a := range list.Items {
					target := a.TargetRef
					if target == "" {
						target = a.TargetKind
					}
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(a.RuleName), observeCell(a.FindingKind), observeCell(a.Severity),
						observeCell(target), observeCell(a.Action), observeCell(a.Mode),
						observeCell(a.Status), observeCell(a.ApprovalID), observeCell(a.KillswitchID),
						observeCell(a.ExecutedAt)); err != nil {
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
	cmd.Flags().StringVar(&status, "status", "", "only actions in this status (e.g. executed)")
	addObservePageFlags(cmd, page)
	return cmd
}
