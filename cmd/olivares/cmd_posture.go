// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The posture namespace: one route, GET /export, which assembles the whole
// governance posture of a tenant — inventory, least-privilege drift, findings —
// inside a single audited scope.
//
// IT IS AN OFF-BOX EXPORT AND THE ENGINE TREATS IT AS ONE: the read is audited
// with the real principal in the SAME transaction as the reads themselves
// (modules/posture-export/postureexport.go:124-131). Running this is a recorded
// act, not a free query.
//
// PAGINATION: none. This route reads no cursor and no limit — it is a snapshot,
// and a half-snapshot would be a different document. What it DOES have is two
// truncation flags, and this command refuses to let them pass quietly (see
// --strict below).

const postureNS = "posture"

func newPostureCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "posture",
		Short: "Export the tenant's governance posture as one document",
		Long: "posture assembles inventory, least-privilege drift and security findings into a\n" +
			"single document, read inside one audited scope so the three halves describe the\n" +
			"same instant.\n\n" +
			"Exporting posture moves it off-box, and the engine audits it as such: this is a\n" +
			"recorded act with the caller's real identity attached, not a free query.",
		Example: "  olivares posture export\n" +
			"  olivares posture export --severity high\n" +
			"  olivares posture export --out posture.json",
	}
	flags.addPersistent(root)
	root.AddCommand(newPostureExportCmd(&flags))
	return root
}

type postureDriftEdge struct {
	OriginKind string `json:"origin_kind"`
	OriginID   string `json:"origin_id"`
	ResourceID string `json:"resource_id"`
	Mode       string `json:"mode"`
}

type postureDrift struct {
	UnexpectedAccesses []postureDriftEdge `json:"unexpected_accesses"`
	UnexpectedCount    int                `json:"unexpected_count"`
	UnusedGrantCount   int                `json:"unused_grant_count"`
	InventoryGrant     int                `json:"inventory_grant_count"`
	Truncated          bool               `json:"truncated"`
}

type postureFinding struct {
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Status      string `json:"status"`
	Source      string `json:"source"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`
	Title       string `json:"title,omitempty"`
	OccurredAt  string `json:"occurred_at,omitempty"`
}

type postureInventoryItem struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Status string `json:"status"`
}

type postureExport struct {
	Tenant             string                 `json:"tenant"`
	Note               string                 `json:"note"`
	Inventory          []postureInventoryItem `json:"inventory"`
	InventoryTruncated bool                   `json:"inventory_truncated"`
	Drift              postureDrift           `json:"posture_drift"`
	Findings           []postureFinding       `json:"findings"`
	FindingsTruncated  bool                   `json:"findings_truncated"`
}

// postureSeverities mirrors the engine's severityRank allow-list
// (modules/posture-export/project.go:85-95). The engine answers 400 for anything
// else; checking here makes a typo an exit 2 with no request sent, and the
// message can name the four values, which a remote 400 does not.
var postureSeverities = []string{"low", "medium", "high", "critical"}

func validPostureSeverity(s string) bool {
	for _, v := range postureSeverities {
		if s == v {
			return true
		}
	}
	return false
}

func newPostureExportCmd(flags *authClientFlags) *cobra.Command {
	var severity, category, kind, out string
	var strict bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export inventory, drift and findings as one posture document",
		Long: "export produces the posture document. --severity sets a FLOOR on findings,\n" +
			"--category matches a finding kind or subject kind, and --kind narrows the\n" +
			"inventory half.\n\n" +
			"TRUNCATION IS NOT A DETAIL HERE. The engine caps both the inventory and the\n" +
			"findings scan and reports each cap as its own flag. A truncated posture export\n" +
			"read as complete understates the estate in exactly the direction that matters —\n" +
			"fewer findings, fewer entities, less drift — so by default a truncated export\n" +
			"exits 7 (degraded) while still writing the whole document. Pass --strict=false\n" +
			"to exit 0 and rely on the flags in the JSON instead.\n\n" +
			"With --out the document is written to a file (or `-` for stdout) verbatim, which\n" +
			"is what an archive or an evidence bundle wants.",
		Example: "  olivares posture export\n" +
			"  olivares posture export --severity high --category access\n" +
			"  olivares posture export --out posture.json\n" +
			"  olivares posture export --strict=false -o json",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if severity != "" && !validPostureSeverity(severity) {
				return exitcode.New(exitcode.Usage, fmt.Errorf(
					"--severity must be one of %v, got %q", postureSeverities, severity))
			}
			q := url.Values{}
			for _, kv := range []struct{ k, v string }{
				{"severity", severity}, {"category", category}, {"kind", kind},
			} {
				if kv.v != "" {
					q.Set(kv.k, kv.v)
				}
			}
			res, err := observeCall{
				flags: flags, ns: postureNS, method: http.MethodGet, path: "/export", query: q,
			}.do(cmd)
			if err != nil {
				return err
			}
			var doc postureExport
			if err := res.decode(&doc); err != nil {
				return err
			}
			truncated := doc.InventoryTruncated || doc.FindingsTruncated || doc.Drift.Truncated

			// --out writes the ENGINE'S bytes, not a re-marshal: an evidence
			// bundle whose contents differ from what the control plane produced is
			// not evidence of anything.
			if out != "" {
				if err := writeObserveArtifact(cmd, out, res, "the posture export"); err != nil {
					return err
				}
				return postureTruncationExit(cmd, strict, truncated)
			}

			if err := renderOut(cmd, func(w io.Writer) error {
				if _, err := fmt.Fprintf(w, "posture for tenant %s\n", observeCell(doc.Tenant)); err != nil {
					return err
				}
				if doc.Note != "" {
					if _, err := fmt.Fprintf(w, "%s\n", observeCell(doc.Note)); err != nil {
						return err
					}
				}
				tw := newTabWriter(w)
				fmt.Fprintf(tw, "inventory entities\t%d%s\n", len(doc.Inventory),
					observeTruncatedMark(doc.InventoryTruncated))
				fmt.Fprintf(tw, "unexpected accesses\t%d%s\n", doc.Drift.UnexpectedCount,
					observeTruncatedMark(doc.Drift.Truncated))
				fmt.Fprintf(tw, "unused grants\t%d\n", doc.Drift.UnusedGrantCount)
				fmt.Fprintf(tw, "inventoried grants\t%d\n", doc.Drift.InventoryGrant)
				fmt.Fprintf(tw, "findings\t%d%s\n", len(doc.Findings),
					observeTruncatedMark(doc.FindingsTruncated))
				if err := tw.Flush(); err != nil {
					return err
				}
				if len(doc.Findings) > 0 {
					ft := newTabWriter(w)
					if _, err := fmt.Fprintln(ft, "SEVERITY\tKIND\tSTATUS\tSUBJECT\tSOURCE\tOCCURRED"); err != nil {
						return err
					}
					for _, f := range doc.Findings {
						subject := f.SubjectID
						if f.SubjectKind != "" {
							subject = f.SubjectKind + ":" + f.SubjectID
						}
						if _, err := fmt.Fprintf(ft, "%s\t%s\t%s\t%s\t%s\t%s\n",
							observeCell(f.Severity), observeCell(f.Kind), observeCell(f.Status),
							observeCell(subject), observeCell(f.Source),
							observeCell(f.OccurredAt)); err != nil {
							return err
						}
					}
					if err := ft.Flush(); err != nil {
						return err
					}
				}
				if truncated {
					_, err := fmt.Fprintln(w,
						"TRUNCATED: at least one half of this export hit the engine's cap, so every "+
							"count above is a FLOOR. This document is NOT a complete posture")
					return err
				}
				return nil
			}, observeJSON(res.raw)); err != nil {
				return err
			}
			return postureTruncationExit(cmd, strict, truncated)
		},
	}
	cmd.Flags().StringVar(&severity, "severity", "", "minimum finding severity: low, medium, high or critical")
	cmd.Flags().StringVar(&category, "category", "", "match a finding kind or subject kind")
	cmd.Flags().StringVar(&kind, "kind", "", "narrow the inventory half to one entity kind")
	observeArtifactFlag(cmd, &out)
	cmd.Flags().Lookup("out").Usage = "write the document verbatim here; `-` means stdout (default: render a summary)"
	cmd.Flags().BoolVar(&strict, "strict", true,
		"exit 7 (degraded) when the engine truncated any half of the export; --strict=false exits 0 instead")
	return cmd
}

func observeTruncatedMark(t bool) string {
	if t {
		return "  (TRUNCATED — a floor, not a total)"
	}
	return ""
}

// postureTruncationExit carries the truncation into the exit code. The document
// is already written, so the wrapper carries the code and no message.
func postureTruncationExit(_ *cobra.Command, strict, truncated bool) error {
	if !strict || !truncated {
		return nil
	}
	return exitcode.New(exitcode.Degraded, nil)
}
