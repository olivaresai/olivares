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

// `olivares governance nhi` — the non-human identities: who owns them, when they last rotated,
// and which ones the plane is already refusing.
//
// ⭐ THIS FAMILY IS WHERE THE PER-HANDLER PAGING RULE SHOWS ITS TEETH, and it is why the rule is
// not "one policy per namespace":
//
//	nhi ls      handleListNHI      calls listQuery(r)  ⇒ --limit/--cursor, and they WORK
//	nhi events  handleListNHIEvents calls listAll      ⇒ no flags: the engine reads neither
//
// Two list verbs, one namespace, opposite answers. Copying `ls`'s flags onto `events` would give
// a --cursor the engine ignores, which is the failure this tree already measured on `adoption
// developers`: page two is page one forever.
func governanceNHICmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nhi",
		Short: "Non-human identities: ownership, rotation age and what is already being refused",
		Long: "The non-human identity register: agents and service identities, who sponsors and owns\n" +
			"each one, how stale its credential is, and whether the plane is enforcing against it.\n\n" +
			"`posture` is the estate-wide answer; `ls` is the per-identity one.",
		Example: "  olivares governance nhi posture\n" +
			"  olivares governance nhi ls --enforcement blocked",
	}
	cmd.AddCommand(nhiListCmd(flags), nhiPostureCmd(flags), nhiGetCmd(flags), nhiEventsCmd(flags))
	return cmd
}

type cliNHI struct {
	IdentityRef    string `json:"identity_ref"`
	Source         string `json:"source,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Criticality    string `json:"criticality"`
	OwnerRef       string `json:"owner_ref,omitempty"`
	SponsorRef     string `json:"sponsor_ref,omitempty"`
	RotatedAt      string `json:"rotated_at,omitempty"`
	MaxAgeSeconds  int64  `json:"max_age_seconds,omitempty"`
	RotationTarget string `json:"rotation_target,omitempty"`
	Staleness      string `json:"staleness_status"`
	Enforcement    string `json:"enforcement"`
	EnforceReason  string `json:"enforcement_reason,omitempty"`
	Orphaned       bool   `json:"orphaned"`
	RegistryOrphan bool   `json:"registry_orphaned,omitempty"`
	OffboardState  string `json:"offboard_state"`
	RecoveryUntil  string `json:"recovery_until,omitempty"`
}

type cliNHIList struct {
	Items   []cliNHI `json:"items"`
	Cursor  string   `json:"cursor,omitempty"`
	HasMore bool     `json:"has_more,omitempty"`
}

func nhiListCmd(flags *authClientFlags) *cobra.Command {
	var enforcement, offboardState string
	page := &observePageFlags{}
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the non-human identities",
		Long: "One row per identity. ENFORCE is what the plane is doing about it right now and STALE\n" +
			"is why: an identity can be stale without being blocked, and blocked for a reason that is\n" +
			"not staleness at all — read both columns, not one.",
		Example: "  olivares governance nhi ls --enforcement blocked\n" +
			"  olivares governance nhi ls --offboard-state soft_deleted -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			if enforcement != "" {
				q.Set("enforcement", enforcement)
			}
			if offboardState != "" {
				q.Set("offboard_state", offboardState)
			}
			if err := page.apply(q); err != nil {
				return err
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/nhi", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliNHIList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no non-human identity matches")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw,
					"IDENTITY\tKIND\tCRIT\tOWNER\tROTATED\tSTALE\tENFORCE\tOFFBOARD\tORPHAN"); err != nil {
					return err
				}
				for _, n := range list.Items {
					orphan := "no"
					if n.Orphaned {
						orphan = "YES"
					}
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(n.IdentityRef), observeCell(n.Kind), observeCell(n.Criticality),
						observeCell(n.OwnerRef), observeCell(n.RotatedAt), observeCell(n.Staleness),
						observeCell(n.Enforcement), observeCell(n.OffboardState), orphan); err != nil {
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
	cmd.Flags().StringVar(&enforcement, "enforcement", "", "only identities in this enforcement state")
	cmd.Flags().StringVar(&offboardState, "offboard-state", "", "only identities in this offboarding state")
	addObservePageFlags(cmd, page)
	return cmd
}

type cliNHIPosture struct {
	Total            int     `json:"total"`
	RotationKnown    int     `json:"rotation_known"`
	RotationCoverage float64 `json:"rotation_coverage"`
	Stale            int     `json:"stale"`
	Blocked          int     `json:"blocked"`
	Alerting         int     `json:"alerting"`
	Orphaned         int     `json:"orphaned"`
	Unsponsored      int     `json:"unsponsored"`
	Owned            int     `json:"owned"`
	SoftDeleted      int     `json:"soft_deleted"`
	Finalized        int     `json:"finalized"`
	Critical         int     `json:"critical"`
}

// nhiPostureCmd — the estate-wide counts.
//
// ⚠ ROTATION COVERAGE IS PRINTED WITH ITS NUMERATOR AND DENOMINATOR, never as a bare percentage,
// and an empty register says so instead of showing 0%. "0% of credentials have a known rotation
// date" and "there are no identities" are opposite facts, and a lone `0.0%` renders them
// identically — the register with nothing in it would read as the worst possible posture.
func nhiPostureCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "posture",
		Short: "The estate-wide identity posture in one screen",
		Example: "  olivares governance nhi posture\n" +
			"  olivares governance nhi posture -o json",
		Long: "Counts across the whole register: how many identities exist, how many have a known\n" +
			"rotation date, how many are stale, blocked, orphaned or unsponsored.\n\n" +
			"Rotation coverage is reported as a fraction of the total, not as a bare percentage: an\n" +
			"empty register and a register with no rotation dates are different facts.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/nhi/posture",
			}.do(cmd)
			if err != nil {
				return err
			}
			var p cliNHIPosture
			if err := res.decode(&p); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if p.Total == 0 {
					_, err := fmt.Fprintln(out,
						"the non-human identity register is EMPTY — this is not 0% coverage, it is nothing to cover")
					return err
				}
				if _, err := fmt.Fprintf(out, "identities:       %d (%d critical)\nrotation known:   %d of %d (%.1f%%)\n",
					p.Total, p.Critical, p.RotationKnown, p.Total, p.RotationCoverage*100); err != nil {
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				for _, row := range []struct {
					label string
					n     int
				}{
					{"stale", p.Stale}, {"blocked", p.Blocked}, {"alerting", p.Alerting},
					{"orphaned", p.Orphaned}, {"unsponsored", p.Unsponsored}, {"owned", p.Owned},
					{"soft-deleted", p.SoftDeleted}, {"finalized", p.Finalized},
				} {
					if _, err := fmt.Fprintf(tw, "%s\t%d\n", row.label, row.n); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

func nhiGetCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <identity-ref>",
		Short: "One non-human identity, in full",
		Long: "Everything the register holds for one identity: who owns and sponsors it, when it was\n" +
			"last rotated and against what target, the credential-age bound it is judged by, and the\n" +
			"two orphan facts — the plane's conclusion and the registry's assertion — which are\n" +
			"printed separately because they can disagree, and that disagreement is the interesting\n" +
			"case. `max age` prints `-` when no bound is set: that is a different fact from a bound\n" +
			"of zero.",
		Example: "  olivares governance nhi get svc-billing-sync\n" +
			"  olivares governance nhi get svc-billing-sync -o json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/nhi/" + url.PathEscape(args[0]),
			}.do(cmd)
			if err != nil {
				return err
			}
			var n cliNHI
			if err := res.decode(&n); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if _, err := fmt.Fprintf(out,
					"identity:    %s\nkind:        %s\nsource:      %s\ncriticality: %s\nowner:       %s\nsponsor:     %s\n",
					observeCell(n.IdentityRef), observeCell(n.Kind), observeCell(n.Source),
					observeCell(n.Criticality), observeCell(n.OwnerRef), observeCell(n.SponsorRef)); err != nil {
					return err
				}
				if _, err := fmt.Fprintf(out, "rotated:     %s (target %s)\nstaleness:   %s\nenforcement: %s\n",
					observeCell(n.RotatedAt), observeCell(n.RotationTarget),
					observeCell(n.Staleness), observeCell(n.Enforcement)); err != nil {
					return err
				}
				// The policy bound this identity is judged against. It is decoded and the engine
				// sets it, and this verb advertises the identity "in full"; without the line a
				// configured bound and no bound at all read the same. Zero is NOT rendered as
				// "0s" — it means no bound was set, which is a different fact from a bound of zero.
				maxAge := "-"
				if n.MaxAgeSeconds > 0 {
					maxAge = fmt.Sprintf("%ds", n.MaxAgeSeconds)
				}
				if _, err := fmt.Fprintf(out, "max age:     %s\n", maxAge); err != nil {
					return err
				}
				if n.EnforceReason != "" {
					if _, err := fmt.Fprintf(out, "  reason:    %s\n", n.EnforceReason); err != nil {
						return err
					}
				}
				// The two orphan flags are SEPARATE facts and are printed separately: `orphaned` is
				// the plane's own conclusion, `registry_orphaned` is the registry's assertion. They
				// can disagree, and that disagreement is the interesting case.
				orphan := "no"
				if n.Orphaned {
					orphan = "YES"
				}
				registry := "no"
				if n.RegistryOrphan {
					registry = "YES"
				}
				_, err := fmt.Fprintf(out, "orphaned:    %s (registry says: %s)\noffboard:    %s%s\n",
					orphan, registry, observeCell(n.OffboardState),
					map[bool]string{true: " — recovery until " + n.RecoveryUntil, false: ""}[n.RecoveryUntil != ""])
				return err
			}, observeJSON(res.raw))
		},
	}
}

type cliNHIEvent struct {
	IdentityRef string `json:"identity_ref"`
	Event       string `json:"event"`
	Actor       string `json:"actor"`
	Detail      string `json:"detail,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

type cliNHIEventList struct {
	Items []cliNHIEvent `json:"items"`
}

// nhiEventsCmd — NO paging flags, and that is measured, not assumed: handleListNHIEvents calls
// listAll, so the engine reads neither `limit` nor `cursor`. Its sibling `nhi ls` DOES page. Two
// list verbs in one namespace with opposite answers is exactly why this is decided per handler.
func nhiEventsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "events <identity-ref>",
		Short:   "The lifecycle events recorded for one identity",
		Example: "  olivares governance nhi events svc-billing-sync",
		Long: "Every recorded event for the identity, oldest first as the engine returns them. This\n" +
			"route reads the whole set — it takes no cursor, unlike `nhi ls`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/nhi/" + url.PathEscape(args[0]) + "/events",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliNHIEventList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no lifecycle event recorded for this identity")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "WHEN\tEVENT\tACTOR\tDETAIL"); err != nil {
					return err
				}
				for _, e := range list.Items {
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
						observeCell(e.OccurredAt), observeCell(e.Event),
						observeCell(e.Actor), observeCell(e.Detail)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}
