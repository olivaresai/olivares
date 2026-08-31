// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The identity namespace from a terminal: four read-only posture views —
// federation graph, SSO connection state, customer-managed keys, and workspace
// data residency.
//
// ═══ THE ONE DESIGN DECISION IN THIS FILE, AND WHY IT IS NOT DECORATION ═══
//
// Three of these four routes answer with THREE states, not two, and the engine
// spent real effort making that so. From modules/governance/identitysso.go:28-33,
// verbatim:
//
//	configured=false + no reason  — SSO is genuinely OFF (a real, known state)
//	configured=false + a reason   — we could NOT determine it. "Reporting this as
//	                                'off' would tell an operator SSO is disabled
//	                                when the truth is that we did not look."
//
// modules/governance/identityposture.go:31-35 says the same for the CMEK and
// residency inventories: "No customer-managed keys" and "we could not look" are
// different facts and must not render alike.
//
// A CLI that exited 0 for both would destroy that distinction at the exact place
// it matters most — a fleet sweep. So the third state exits INDETERMINATE (8),
// which the contract already defines for precisely this: "the answer could not be
// established … A fleet sweep must treat this as 'not yet answered', never as
// 'clean'" (exitcode.go:31-37).
//
// This ADDS a use of an existing code; it redefines nothing. A caller that today
// treats non-zero as failure keeps working. A caller that branches learns the
// difference between "no CMEK is configured" and "I could not see whether one is".
// The report is on stdout in both cases, so the exit code is the only thing that
// changes — and `--strict=false` turns it off for a caller that does not want it.

const identityNS = "identity"

func newIdentityCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "identity",
		Short: "Read federation, SSO, customer-managed key and residency posture",
		Long: "identity reports the estate's identity posture: the workload-identity federation\n" +
			"graph, whether and how SSO is connected, the customer-managed encryption key\n" +
			"inventory, and each workspace's data-residency object.\n\n" +
			"EVERY VERB HERE IS A READ AND NONE OF THEM CONFIGURES ANYTHING. SSO is authored\n" +
			"through the superadmin console; keys and residency are governed at the provider.\n" +
			"These commands mirror the resulting posture.\n\n" +
			"THREE ANSWERS, NEVER TWO. `sso`, `external-keys` and `residency` distinguish\n" +
			"\"this is off / empty\" from \"the engine could not look\". The second exits 8\n" +
			"(indeterminate), because a sweep that counted it as clean would be reporting a\n" +
			"posture nobody measured. Pass --strict=false to exit 0 either way.",
		Example: "  olivares identity sso\n" +
			"  olivares identity external-keys -o json\n" +
			"  olivares identity residency\n" +
			"  olivares identity wif -o json",
	}
	flags.addPersistent(root)
	root.AddCommand(
		newIdentitySsoCmd(&flags),
		newIdentityExternalKeysCmd(&flags),
		newIdentityResidencyCmd(&flags),
		newIdentityWifCmd(&flags),
	)
	return root
}

// addIdentityStrictFlag declares the opt-out from the indeterminate exit.
//
// It defaults TRUE. The safe default for a posture read is to make "I could not
// look" loud; an operator who has decided they do not care can say so, but they
// have to say it.
func addIdentityStrictFlag(cmd *cobra.Command, strict *bool) {
	cmd.Flags().BoolVar(strict, "strict", true,
		"exit 8 (indeterminate) when the engine reports it could not establish this posture; --strict=false exits 0 instead")
}

// identityIndeterminate is the shared decision. The report is ALREADY on stdout
// when this is called, so the returned error carries the code and no message —
// exitcode.Silent's contract (exitcode.go:99).
func identityIndeterminate(strict bool, reason string) error {
	if !strict || reason == "" {
		return nil
	}
	return exitcode.New(exitcode.Indeterminate, nil)
}

// ---- sso ---------------------------------------------------------------------------

type identitySso struct {
	Protocol    string `json:"protocol"`
	Configured  bool   `json:"configured"`
	RedirectURI string `json:"redirect_uri,omitempty"`
	PKCEMethod  string `json:"pkce_method,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func newIdentitySsoCmd(flags *authClientFlags) *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "sso",
		Short: "Report the SSO connection state",
		Long: "sso reports whether an identity provider currently backs sign-in, which protocol\n" +
			"it speaks, and the redirect URI to register with that provider. The redirect URI\n" +
			"is derived through the engine's own rule, so what this prints is what the login\n" +
			"leg will actually send — including behind a trusted reverse proxy.\n\n" +
			"It REPORTS and never configures. And it distinguishes \"SSO is off\" from \"the\n" +
			"engine could not determine the state\": the second exits 8, because telling a\n" +
			"sweep that SSO is disabled when nobody looked is the failure this split exists\n" +
			"to prevent.",
		Example: "  olivares identity sso\n" +
			"  olivares identity sso -o json\n" +
			"  olivares identity sso --strict=false",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: identityNS, method: http.MethodGet, path: "/sso"}.do(cmd)
			if err != nil {
				return err
			}
			var s identitySso
			if err := res.decode(&s); err != nil {
				return err
			}
			if err := renderOut(cmd, func(w io.Writer) error {
				tw := newTabWriter(w)
				switch {
				case s.Reason != "":
					fmt.Fprintf(tw, "sso\tUNDETERMINED\n")
					fmt.Fprintf(tw, "reason\t%s\n", observeCell(s.Reason))
				case s.Configured:
					fmt.Fprintf(tw, "sso\tconnected\n")
					fmt.Fprintf(tw, "protocol\t%s\n", observeCell(s.Protocol))
				default:
					fmt.Fprintf(tw, "sso\tnot configured\n")
				}
				fmt.Fprintf(tw, "redirect uri\t%s\n", observeCell(s.RedirectURI))
				fmt.Fprintf(tw, "pkce method\t%s\n", observeCell(s.PKCEMethod))
				if err := tw.Flush(); err != nil {
					return err
				}
				if s.Reason != "" {
					_, werr := fmt.Fprintln(w,
						"this is NOT a report that SSO is disabled: the engine could not establish the state")
					return werr
				}
				return nil
			}, observeJSON(res.raw)); err != nil {
				return err
			}
			return identityIndeterminate(strict, s.Reason)
		},
	}
	addIdentityStrictFlag(cmd, &strict)
	return cmd
}

// ---- external keys ---------------------------------------------------------------

type identityExternalKey struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	Name            string `json:"name,omitempty"`
	State           string `json:"state,omitempty"`
	LastValidatedAt string `json:"last_validated_at,omitempty"`
	InUse           bool   `json:"in_use"`
	CreatedAt       string `json:"created_at,omitempty"`
}

// identityPostureList is the engine's postureListDTO: a list envelope PLUS the
// availability pair that carries the third answer.
type identityPostureList[T any] struct {
	Items     []T    `json:"items"`
	HasMore   bool   `json:"has_more"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// unavailableLine is the one place the "could not look" wording is produced, so
// the three posture verbs cannot word it differently from each other.
func unavailableLine(w io.Writer, what, reason string) error {
	if _, err := fmt.Fprintf(w, "%s: UNAVAILABLE — this is not an empty inventory, it is an unmeasured one\n", what); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "reason: %s\n", observeCell(reason))
	return err
}

func newIdentityExternalKeysCmd(flags *authClientFlags) *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:     "external-keys",
		Aliases: []string{"cmek"},
		Short:   "List the customer-managed encryption key inventory",
		Long: "external-keys lists the organization's customer-managed (CMEK) key inventory as\n" +
			"metadata only: a key REFERENCE, the cloud-KMS provider, a validation state and\n" +
			"timestamps. There is no field on this route that can carry key material.\n\n" +
			"An empty list from a wired provider is a real zero — no customer-managed keys.\n" +
			"An UNAVAILABLE answer means no admin credential is wired, or the fetch failed,\n" +
			"and exits 8: an inventory nobody could read must not be counted as an inventory\n" +
			"that came back empty.",
		Example: "  olivares identity external-keys\n" +
			"  olivares identity external-keys -o json\n" +
			"  olivares identity external-keys --strict=false",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: identityNS, method: http.MethodGet, path: "/external-keys"}.do(cmd)
			if err != nil {
				return err
			}
			var out identityPostureList[identityExternalKey]
			if err := res.decode(&out); err != nil {
				return err
			}
			if err := renderOut(cmd, func(w io.Writer) error {
				if !out.Available {
					return unavailableLine(w, "customer-managed key inventory", out.Reason)
				}
				if len(out.Items) == 0 {
					_, werr := fmt.Fprintln(w,
						"no customer-managed keys: the inventory was read cleanly and is empty")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "KEY\tPROVIDER\tNAME\tSTATE\tIN USE\tLAST VALIDATED"); werr != nil {
					return werr
				}
				for _, k := range out.Items {
					if _, werr := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(k.ID), observeCell(k.Provider), observeCell(k.Name),
						observeCell(k.State), observeBool(k.InUse, "yes", "no"),
						observeCell(k.LastValidatedAt)); werr != nil {
						return werr
					}
				}
				return tw.Flush()
			}, observeJSON(res.raw)); err != nil {
				return err
			}
			return identityIndeterminate(strict, out.Reason)
		},
	}
	addIdentityStrictFlag(cmd, &strict)
	return cmd
}

// ---- residency ---------------------------------------------------------------------

type identityDataResidency struct {
	AllowedInferenceGeos []string `json:"allowed_inference_geos,omitempty"`
	DefaultInferenceGeo  string   `json:"default_inference_geo,omitempty"`
}

type identityWorkspaceResidency struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name,omitempty"`
	Geo           string                 `json:"geo,omitempty"`
	ExternalKeyID string                 `json:"external_key_id,omitempty"`
	CompartmentID string                 `json:"compartment_id,omitempty"`
	DataResidency *identityDataResidency `json:"data_residency,omitempty"`
}

func newIdentityResidencyCmd(flags *authClientFlags) *cobra.Command {
	var strict bool
	cmd := &cobra.Command{
		Use:   "residency",
		Short: "List each workspace's data-residency and CMEK posture",
		Long: "residency lists the governance object of each active workspace: its home geo,\n" +
			"the customer-managed key covering it (if any), its compartment and its allowed\n" +
			"inference geos.\n\n" +
			"ARCHIVED WORKSPACES ARE ABSENT BY DESIGN, engine-side: an archived workspace\n" +
			"takes no inference, so listing its missing CMEK would report a residency gap\n" +
			"nobody can act on.\n\n" +
			"An UNAVAILABLE answer exits 8. A workspace with no key is shown as a gap, which\n" +
			"is a finding; a list that could not be read is not a finding at all.",
		Example: "  olivares identity residency\n" +
			"  olivares identity residency -o json\n" +
			"  olivares identity residency --strict=false",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: identityNS, method: http.MethodGet, path: "/residency"}.do(cmd)
			if err != nil {
				return err
			}
			var out identityPostureList[identityWorkspaceResidency]
			if err := res.decode(&out); err != nil {
				return err
			}
			if err := renderOut(cmd, func(w io.Writer) error {
				if !out.Available {
					return unavailableLine(w, "workspace residency inventory", out.Reason)
				}
				if len(out.Items) == 0 {
					_, werr := fmt.Fprintln(w, "no active workspaces: the inventory was read cleanly and is empty")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "WORKSPACE\tNAME\tGEO\tCMEK\tCOMPARTMENT\tDEFAULT GEO\tALLOWED GEOS"); werr != nil {
					return werr
				}
				gaps := 0
				for _, ws := range out.Items {
					key := ws.ExternalKeyID
					if key == "" {
						key = "NONE"
						gaps++
					}
					defGeo, allowed := "-", "-"
					if ws.DataResidency != nil {
						defGeo = observeCell(ws.DataResidency.DefaultInferenceGeo)
						if len(ws.DataResidency.AllowedInferenceGeos) > 0 {
							allowed = fmt.Sprintf("%v", ws.DataResidency.AllowedInferenceGeos)
						}
					}
					if _, werr := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						observeCell(ws.ID), observeCell(ws.Name), observeCell(ws.Geo),
						observeCell(key), observeCell(ws.CompartmentID), defGeo, allowed); werr != nil {
						return werr
					}
				}
				if werr := tw.Flush(); werr != nil {
					return werr
				}
				if gaps > 0 {
					_, werr := fmt.Fprintf(w,
						"%d of %d workspace(s) are provider-encrypted with no customer-managed key\n",
						gaps, len(out.Items))
					return werr
				}
				return nil
			}, observeJSON(res.raw)); err != nil {
				return err
			}
			return identityIndeterminate(strict, out.Reason)
		},
	}
	addIdentityStrictFlag(cmd, &strict)
	return cmd
}

// ---- wif ------------------------------------------------------------------------

// identityWifGraph mirrors claude-wif's WIFGraph (connectors/claude-wif/graph.go:41).
//
// Written from that type, not from memory: the first draft of this file invented
// `url`, `trust_domain` and `allowed_audiences`, none of which exist on the wire —
// the issuer's URL is `issuer_url` and a rule is keyed by `rule_id`. It would have
// printed a table of empty columns with no error anywhere.
//
// Two fields matter more than the object lists and are surfaced ABOVE them:
//
//   - key_shadow is the static-key footgun. A present ANTHROPIC_API_KEY /
//     ANTHROPIC_AUTH_TOKEN takes PRECEDENCE over federation (graph.go:45-48), so a
//     perfectly configured WIF graph can be inert. Printing the graph without this
//     would show federation that is not actually in effect.
//   - reconciliation says whether the declared graph was checked against the live
//     WIF Admin API. When it could not be, the engine refuses to render a
//     "fabricated all clear" (graph.go:55-57) and neither does this command.
type identityWifGraph struct {
	Issuers []struct {
		ID               string `json:"id"`
		IssuerURL        string `json:"issuer_url,omitempty"`
		JWKSMode         string `json:"jwks_mode,omitempty"`
		CACertConfigured bool   `json:"ca_cert_configured"`
		Source           string `json:"source,omitempty"`
	} `json:"issuers"`
	Rules []struct {
		RuleID             string `json:"rule_id"`
		IssuerID           string `json:"issuer_id,omitempty"`
		ServiceAccountID   string `json:"service_account_id"`
		ServiceAccountName string `json:"service_account_name,omitempty"`
		SubjectPrefix      string `json:"subject_prefix,omitempty"`
		Audience           string `json:"audience,omitempty"`
	} `json:"rules"`
	ServiceAccounts []struct {
		ID               string `json:"id"`
		Name             string `json:"name,omitempty"`
		OrganizationRole string `json:"organization_role,omitempty"`
	} `json:"service_accounts"`
	KeyShadow *struct {
		Present bool   `json:"present"`
		Var     string `json:"var,omitempty"`
	} `json:"key_shadow,omitempty"`
	Reconciliation *struct {
		Reconciled  bool   `json:"reconciled"`
		ObservedAt  string `json:"observed_at,omitempty"`
		Unavailable string `json:"unavailable,omitempty"`
	} `json:"reconciliation,omitempty"`
}

func newIdentityWifCmd(flags *authClientFlags) *cobra.Command {
	return &cobra.Command{
		Use:     "wif",
		Aliases: []string{"federation"},
		Short:   "Show the workload-identity federation graph",
		Long: "wif shows the declared workload-identity federation graph: trusted issuers, the\n" +
			"rules mapping external workload identities onto principals, and the service\n" +
			"accounts they can become.\n\n" +
			"It carries whether a CA certificate is configured as a BOOLEAN only — no PEM, no\n" +
			"key, no SVID ever crosses this route.\n\n" +
			"UNLIKE ITS THREE SIBLINGS THIS ROUTE HAS NO `reason` FIELD, so it cannot\n" +
			"distinguish \"no federation is declared\" from \"the federation seam is not wired\n" +
			"in this build\": both arrive as an empty graph. This command therefore says\n" +
			"\"empty\" and does NOT claim which, and it has no --strict flag because there is\n" +
			"nothing to be strict about.",
		Example: "  olivares identity wif\n  olivares identity wif -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := observeCall{flags: flags, ns: identityNS, method: http.MethodGet, path: "/wif"}.do(cmd)
			if err != nil {
				return err
			}
			var g identityWifGraph
			if err := res.decode(&g); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				// The two posture signals come FIRST. A reader who stops after the
				// object lists must not miss that federation is shadowed or unverified.
				if g.KeyShadow != nil && g.KeyShadow.Present {
					if _, werr := fmt.Fprintf(w,
						"STATIC KEY SHADOWS FEDERATION: %s is set and takes precedence — "+
							"the graph below may be declared but inert\n",
						observeCell(g.KeyShadow.Var)); werr != nil {
						return werr
					}
				}
				switch {
				case g.Reconciliation == nil:
					if _, werr := fmt.Fprintln(w,
						"declared graph only: it was not reconciled against the live federation API"); werr != nil {
						return werr
					}
				case g.Reconciliation.Reconciled:
					if _, werr := fmt.Fprintf(w, "reconciled against the live federation API at %s\n",
						observeCell(g.Reconciliation.ObservedAt)); werr != nil {
						return werr
					}
				default:
					if _, werr := fmt.Fprintf(w,
						"NOT RECONCILED: the live federation config could not be listed (%s) — "+
							"what follows is the declared baseline, not a verified state\n",
						observeCell(g.Reconciliation.Unavailable)); werr != nil {
						return werr
					}
				}
				if len(g.Issuers) == 0 && len(g.Rules) == 0 && len(g.ServiceAccounts) == 0 {
					_, werr := fmt.Fprintln(w,
						"the federation graph is empty — either nothing is declared, or the federation "+
							"seam is not wired in this build. This route carries no field that tells the two apart")
					return werr
				}
				tw := newTabWriter(w)
				if _, werr := fmt.Fprintln(tw, "ISSUER\tISSUER URL\tJWKS\tCA CERT\tSOURCE"); werr != nil {
					return werr
				}
				for _, iss := range g.Issuers {
					if _, werr := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
						observeCell(iss.ID), observeCell(iss.IssuerURL), observeCell(iss.JWKSMode),
						observeBool(iss.CACertConfigured, "configured", "no"),
						observeCell(iss.Source)); werr != nil {
						return werr
					}
				}
				if werr := tw.Flush(); werr != nil {
					return werr
				}
				rt := newTabWriter(w)
				if _, werr := fmt.Fprintln(rt, "RULE\tISSUER\tSUBJECT PREFIX\tAUDIENCE\tBECOMES"); werr != nil {
					return werr
				}
				for _, rule := range g.Rules {
					if _, werr := fmt.Fprintf(rt, "%s\t%s\t%s\t%s\t%s\n",
						observeCell(rule.RuleID), observeCell(rule.IssuerID),
						observeCell(rule.SubjectPrefix), observeCell(rule.Audience),
						observeCell(firstNonEmptyCLI(rule.ServiceAccountName, rule.ServiceAccountID))); werr != nil {
						return werr
					}
				}
				if werr := rt.Flush(); werr != nil {
					return werr
				}
				// An admin-role service account is the only target a rule can use to
				// mint org:admin tokens (graph.go:106-108) — a posture signal, so it
				// is named rather than left in the JSON.
				admins := 0
				for _, sa := range g.ServiceAccounts {
					if sa.OrganizationRole == "admin" {
						admins++
					}
				}
				if _, werr := fmt.Fprintf(w, "%d issuer(s), %d rule(s), %d service account(s)",
					len(g.Issuers), len(g.Rules), len(g.ServiceAccounts)); werr != nil {
					return werr
				}
				if admins > 0 {
					if _, werr := fmt.Fprintf(w, " — %d with the ADMIN org role, which can mint org:admin tokens", admins); werr != nil {
						return werr
					}
				}
				_, werr := fmt.Fprintln(w)
				return werr
			}, observeJSON(res.raw))
		},
	}
}
