// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// `olivares governance pdp` — WHICH POLICY IS ACTUALLY DECIDING REQUESTS RIGHT NOW.
//
// The engine already answers this and nothing could ask it. The value is concentrated in one
// route, and the module's own source says why (pdp_authoring.go, on pdpActivePolicy):
//
//	«Without it a console can only remember the last publish in browser memory: a reload, a
//	 second operator or a different replica all lose the one fact that says whether the
//	 revision on screen is the one deciding requests.»
//
// A CLI has no browser memory at all, so before this the fact was simply unreachable from a
// terminal. That is the reason this slice is four read verbs and not a listing.
//
// TWO AXES THAT MUST NOT BE FOLDED, and the module is explicit that folding them makes one
// unreadable — so the text view prints both, always:
//
//   - live_activation — applied | deferred | not_applicable | no_policy. Whether the selected
//     revision is the one this process is deciding with.
//   - grants_expired — this process is past the offline-staleness bound, so POSITIVE grants have
//     degraded to abstain while forbid rules stay enforced (ADR-0024 Q1).
//
// «applied» with grants_expired=true is a real and dangerous state: the engine holds exactly the
// policy you published, and half of what it says is not in force.
func governancePdpCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pdp",
		Short: "The policy decision point: which revision is actually deciding, and is it in force",
		Long: "pdp reads the authoring and activation state of the policy engines. `active` is the\n" +
			"one to reach for: it says whether the revision you published is the revision this\n" +
			"process is deciding with, and separately whether its positive grants have expired.",
		Example: "  olivares governance pdp active --engine cedar\n" +
			"  olivares governance pdp versions\n" +
			"  olivares governance pdp tests --engine opa",
	}
	cmd.AddCommand(pdpVersionsCmd(flags), pdpGetVersionCmd(flags), pdpActiveCmd(flags), pdpTestsCmd(flags))
	return cmd
}

// addEngineFlag declares --engine and requires it.
//
// It is REQUIRED and deliberately NOT validated here. Revision numbers are per-surface, so cedar
// r1 and opa r1 are different documents and defaulting to either would silently answer about the
// wrong one — hence required. But the set of legal values belongs to the engine
// (`engine must be cedar or opa`), and copying it into the client would be a second place to
// update: the day a third surface lands, a client-side allowlist rejects it while the server
// accepts it, and the CLI is the thing that looks broken.
func addEngineFlag(cmd *cobra.Command, dst *string) {
	cmd.Flags().StringVar(dst, "engine", "", "policy surface to read (required; the engine states which are legal)")
	_ = cmd.MarkFlagRequired("engine")
}

type cliPdpRevision struct {
	Revision  int64  `json:"revision"`
	Surface   string `json:"surface"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Content   string `json:"content,omitempty"`
	Validated bool   `json:"validated"`
	Active    bool   `json:"active,omitempty"`
	Note      string `json:"note,omitempty"`
}

type cliPdpRevisionList struct {
	Items []cliPdpRevision `json:"items"`
}

// pdpVersionsCmd — /pdp/versions, which is BOTH surfaces at once and no paging.
//
// handlePdpVersions loops over cedar and opa and concatenates; it takes no filter and no cursor.
// So there is no --engine here (unlike every other verb in this family) and no --limit: the
// SURFACE column is how you tell the two apart, and it is why the column is first.
func pdpVersionsCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "versions",
		Short: "Every stored policy revision, both surfaces, metadata only",
		Example: "  olivares governance pdp versions\n" +
			"  olivares governance pdp versions -o json",
		Long: "List the stored revisions of both policy surfaces. The route returns cedar AND opa in\n" +
			"one response and takes no filter, so read the SURFACE column: revision numbers are\n" +
			"per-surface and cedar r1 is not opa r1.\n\n" +
			"Metadata only — use `get-version` for the document itself.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/pdp/versions",
			}.do(cmd)
			if err != nil {
				return err
			}
			var list cliPdpRevisionList
			if err := res.decode(&list); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if len(list.Items) == 0 {
					_, err := fmt.Fprintln(out, "no policy revision has been stored on either surface")
					return err
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "SURFACE\tREV\tACTIVE\tVALIDATED\tAUTHOR\tCREATED"); err != nil {
					return err
				}
				for _, rv := range list.Items {
					active := "no"
					if rv.Active {
						active = "YES"
					}
					validated := "no"
					if rv.Validated {
						validated = "yes"
					}
					if _, err := fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
						observeCell(rv.Surface), rv.Revision, active, validated,
						observeCell(rv.Author), observeCell(rv.CreatedAt)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
}

// pdpGetVersionCmd — one stored revision WITH its content.
func pdpGetVersionCmd(flags *authClientFlags) *cobra.Command {
	var engine string
	cmd := &cobra.Command{
		Use:   "get-version <revision>",
		Short: "One stored revision, with the policy document itself",
		Long: "Fetch a stored revision and its content. Revision numbers are per-surface, so --engine\n" +
			"is required: cedar r1 and opa r1 are different documents.\n\n" +
			"This is what lets you diff against what is STORED rather than against whatever text a\n" +
			"console happens to be holding — including after a rollback, or from another machine.",
		Example: "  olivares governance pdp get-version 7 --engine cedar",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// EL ID NO SE VALIDA AQUI, y quitar esa comprobacion fue deliberado.
			//
			// Duplicaba una regla del motor —que ya contesta 400 «revision must be a positive
			// integer»— y ademas hacia INMEDIBLE el escapado: la tabla de recorrido de
			// cmd_observeplane_test.go comprueba que un id hostil viaja ESCAPADO (%2F) y sale
			// como un solo segmento, y para eso la peticion tiene que llegar a salir. Un
			// rechazo local antes de construir la ruta deja esa propiedad sin testigo y
			// cambia un control medido por uno que nadie mira.
			q := url.Values{}
			q.Set("engine", engine)
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet,
				path: "/pdp/versions/" + url.PathEscape(args[0]), query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var rv cliPdpRevision
			if err := res.decode(&rv); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				active := "no"
				if rv.Active {
					active = "YES — this is the selected revision"
				}
				validated := "no"
				if rv.Validated {
					validated = "yes"
				}
				if _, err := fmt.Fprintf(out,
					"surface:    %s\nrevision:   %d\nactive:     %s\nvalidated:  %s\nauthor:     %s\ncreated:    %s\n",
					observeCell(rv.Surface), rv.Revision, active, validated,
					observeCell(rv.Author), observeCell(rv.CreatedAt)); err != nil {
					return err
				}
				if rv.Note != "" {
					if _, err := fmt.Fprintf(out, "note:       %s\n", rv.Note); err != nil {
						return err
					}
				}
				if rv.Content == "" {
					_, err := fmt.Fprintln(out, "\n(no content stored for this revision)")
					return err
				}
				_, err := fmt.Fprintf(out, "\n%s\n", rv.Content)
				return err
			}, observeJSON(res.raw))
		},
	}
	addEngineFlag(cmd, &engine)
	return cmd
}

type cliPdpSurface struct {
	Present   bool   `json:"present"`
	Revision  int64  `json:"revision,omitempty"`
	Content   string `json:"content,omitempty"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type cliPdpActive struct {
	Engine         string        `json:"engine"`
	Authored       cliPdpSurface `json:"authored"`
	Managed        cliPdpSurface `json:"managed"`
	Adopted        cliPdpSurface `json:"adopted"`
	UnionSHA256    string        `json:"union_sha256,omitempty"`
	LiveActivation string        `json:"live_activation"`
	GrantsExpired  bool          `json:"grants_expired"`
	Note           string        `json:"note,omitempty"`
}

// pdpActivationApplied is the ONE activation state in which `grants_expired` is a measurement
// rather than a zero value. Named rather than inlined because the negative test below asserts on
// the same constant, so the two cannot drift apart.
const pdpActivationApplied = "applied"

// pdpActiveCmd — the verb this whole slice exists for.
func pdpActiveCmd(flags *authClientFlags) *cobra.Command {
	var engine string
	cmd := &cobra.Command{
		Use:   "active",
		Short: "Which policy this process is deciding with, and whether it is fully in force",
		Long: "Read the active policy for a surface. Two facts are reported on SEPARATE lines because\n" +
			"they are separate axes and either one alone is misleading:\n\n" +
			"  activation  whether the selected revision is the one this process decides with\n" +
			"  grants      whether positive grants have expired past the offline-staleness bound,\n" +
			"              which leaves forbid rules enforced and permits abstaining\n\n" +
			"«applied» with expired grants is a real state: the engine holds exactly the policy you\n" +
			"published, and half of what it says is not in force. `governance rbac grants ls` is what\n" +
			"says WHICH grants those are.",
		Example: "  olivares governance pdp active --engine cedar",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			q.Set("engine", engine)
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/pdp/active", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var a cliPdpActive
			if err := res.decode(&a); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if _, err := fmt.Fprintf(out, "engine:      %s\n", observeCell(a.Engine)); err != nil {
					return err
				}
				// NEVER collapsed into one line. The module carries a comment saying that folding
				// these two makes one of them unreadable, and it is right: "applied" alone reads
				// as healthy while positive grants may be abstaining.
				if _, err := fmt.Fprintf(out, "activation:  %s\n", observeCell(a.LiveActivation)); err != nil {
					return err
				}
				// `grants_expired` is only a MEASUREMENT when a snapshot is actually applied. The
				// engine reports `deferred` when there is no loaded set, `no_policy` when nothing is
				// authored, and OPA reports `not_applicable`; in all of those the field is a zero
				// value nobody computed. Printing "in force" for it tells the operator that positive
				// grants are live when the truth is that expiry was never evaluated — the opposite of
				// what this command exists to answer, and worse than saying nothing.
				grants := "in force"
				switch {
				case a.GrantsExpired:
					// Checked FIRST and on its own: if the engine says expired we report expired,
					// whatever the activation says. A dangerous state is never downgraded to n/a.
					grants = "EXPIRED — positive grants abstain, forbid rules still enforced"
				case a.LiveActivation != pdpActivationApplied:
					grants = "n/a — no applied snapshot, so expiry was never evaluated"
				}
				if _, err := fmt.Fprintf(out, "grants:      %s\n", grants); err != nil {
					return err
				}
				if a.UnionSHA256 != "" {
					if _, err := fmt.Fprintf(out, "union:       %s\n", a.UnionSHA256); err != nil {
						return err
					}
				}
				if a.Note != "" {
					if _, err := fmt.Fprintf(out, "note:        %s\n", a.Note); err != nil {
						return err
					}
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "\nSURFACE\tPRESENT\tREV\tAUTHOR\tCREATED\tSHA256"); err != nil {
					return err
				}
				// All three are printed even when absent — the module keeps them in the struct on
				// purpose so a client can render "none" honestly instead of silently omitting a
				// surface the reader would then assume was not asked about.
				for _, s := range []struct {
					name string
					v    cliPdpSurface
				}{{"authored", a.Authored}, {"managed", a.Managed}, {"adopted", a.Adopted}} {
					present := "no"
					rev := "—"
					if s.v.Present {
						present = "yes"
						rev = strconv.FormatInt(s.v.Revision, 10)
					}
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						s.name, present, rev, observeCell(s.v.Author),
						observeCell(s.v.CreatedAt), observeCell(s.v.SHA256)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
	addEngineFlag(cmd, &engine)
	return cmd
}

type cliPdpTestResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type cliPdpTestStatus struct {
	Engine    string             `json:"engine"`
	Revision  int64              `json:"revision,omitempty"`
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Passed    int                `json:"passed"`
	Failed    int                `json:"failed"`
	Total     int                `json:"total"`
	Results   []cliPdpTestResult `json:"results,omitempty"`
}

// pdpTestsCmd — the stored test artifact for a revision.
//
// `available:false` is NOT a failure and is not rendered as one. It means no test artifact was
// stored for that revision, which is a different fact from "the tests failed" — and conflating
// them would turn "nobody wrote tests" into "the policy is broken".
func pdpTestsCmd(flags *authClientFlags) *cobra.Command {
	var engine string
	var revision int64
	cmd := &cobra.Command{
		Use:   "tests",
		Short: "The stored test results for a policy revision",
		Long: "Read the test artifact stored with a policy revision. Without --revision the engine\n" +
			"picks the newest revision that has one.\n\n" +
			"«no artifact available» is not a failure: it means nobody stored tests for that\n" +
			"revision, which is a different fact from the tests not passing.",
		Example: "  olivares governance pdp tests --engine cedar\n" +
			"  olivares governance pdp tests --engine opa --revision 3",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			q.Set("engine", engine)
			// `Changed` and not `> 0`: those are different questions. Omitting --revision means
			// «newest with an artifact», which is the documented default. Passing --revision 0 or a
			// negative names a revision that cannot exist, and the old `> 0` test dropped it and
			// asked for the newest anyway — so the operator got test results for a DIFFERENT
			// revision than the one they typed, with nothing on screen saying so. Refusing is right
			// here and would be wrong elsewhere: there is no manual fallback being denied, only a
			// question that has no answer.
			if cmd.Flags().Changed("revision") {
				if revision <= 0 {
					return fmt.Errorf("--revision %d is not a revision: revisions start at 1, and omitting the flag reads the newest one that has stored tests", revision)
				}
				q.Set("revision", strconv.FormatInt(revision, 10))
			}
			res, err := observeCall{
				flags: flags, ns: governanceNS, method: http.MethodGet, path: "/pdp/tests", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var st cliPdpTestStatus
			if err := res.decode(&st); err != nil {
				return err
			}
			return renderOut(cmd, func(out io.Writer) error {
				if !st.Available {
					reason := st.Reason
					if reason == "" {
						reason = "no stored test artifact"
					}
					_, err := fmt.Fprintf(out, "%s: %s\n(this is not a test failure — nothing was stored)\n",
						observeCell(st.Engine), reason)
					return err
				}
				if _, err := fmt.Fprintf(out, "engine:   %s\nrevision: %d\nresult:   %d passed, %d failed, %d total\n",
					observeCell(st.Engine), st.Revision, st.Passed, st.Failed, st.Total); err != nil {
					return err
				}
				if len(st.Results) == 0 {
					return nil
				}
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "\nTEST\tRESULT\tDETAIL"); err != nil {
					return err
				}
				for _, r := range st.Results {
					verdict := "FAIL"
					if r.Passed {
						verdict = "pass"
					}
					if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n",
						observeCell(r.Name), verdict, observeCell(r.Detail)); err != nil {
						return err
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw))
		},
	}
	addEngineFlag(cmd, &engine)
	cmd.Flags().Int64Var(&revision, "revision", 0, "a specific revision (default: the newest with a stored artifact)")
	return cmd
}
