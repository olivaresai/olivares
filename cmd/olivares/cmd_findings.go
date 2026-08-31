// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	findingsExportPath       = "/v1/m/security/findings/export"
	maxFindingsErrorBodySize = 1 << 20
)

// newFindingsCmd is a thin authenticated client for findings-shaped exports;
// export policy, filtering, fingerprinting, and result caps remain server-side.
func newFindingsCmd() *cobra.Command {
	var flags authClientFlags
	root := &cobra.Command{
		Use:   "findings",
		Short: "Export governed security findings",
		Long: "findings hands the governed security findings to the tools that consume them.\n" +
			"export writes SARIF 2.1.0, the format code-scanning dashboards and pull-request\n" +
			"annotations already read, so findings raised here surface where engineers\n" +
			"already look instead of in a console only a reviewer opens.",
		Example: "  olivares findings export --format sarif --out findings.sarif",
	}
	flags.addPersistent(root)
	root.AddCommand(newFindingsExportCmd(&flags))
	return root
}

func newFindingsExportCmd(flags *authClientFlags) *cobra.Command {
	var format, outPath string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export all matching findings as SARIF 2.1.0",
		Long: "export requests the tenant-scoped findings export from the running control plane and\n" +
			"writes the exact SARIF response to stdout or --out. The server caps one run at 25,000\n" +
			"results and the CLI warns when that interoperability cap truncates the response.\n" +
			"Findings whose metadata carries artifact_uri anchor to that committed file; the rest\n" +
			"fall back to a synthetic governance/<subject> URI — valid SARIF, but GitHub code\n" +
			"scanning only renders alerts whose URI matches a file in the analyzed repository.",
		Example: "  olivares findings export --format sarif --out findings.sarif",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format = strings.TrimSpace(format)
			if format != "sarif" {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("invalid --format %q (valid values: sarif)", format))
			}
			resolved, err := flags.resolve(cmd)
			if err != nil {
				return redactCoded(err, flags.effectiveToken())
			}
			client, headers, err := cliTransport(cliTransportOptions{
				Resolved:       resolved,
				Insecure:       flags.insecure,
				Timeout:        flags.timeout,
				Stderr:         cmd.ErrOrStderr(),
				AllowCleartext: flags.allowCleartext,
			})
			if err != nil {
				return redactCodedServer(err, resolved.Token)
			}
			req, err := http.NewRequestWithContext(
				cmd.Context(), http.MethodGet, resolved.Server+findingsExportPath+"?format=sarif", nil,
			)
			if err != nil {
				return redactCodedServer(err, resolved.Token)
			}
			req.Header = headers.Clone()
			req.Header.Set("Accept", "application/sarif+json")

			resp, err := cliDo(client, req)
			if err != nil {
				return redactCodedServer(err, resolved.Token)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxFindingsErrorBodySize))
				if readErr != nil {
					return exitcode.New(exitcode.Server,
						fmt.Errorf("read findings export error response: %w", readErr))
				}
				// This line used to say "no redaction here", and the reason it gave
				// was the defect: redactCLIError rebuilt a plain error and would have
				// stripped httpErr's exit-code wrapper, which scripts branch on. The
				// choice between "keep the code" and "scrub the bearer" no longer
				// exists — redactCoded does both — so the body of a route that DOES
				// carry an Authorization header is no longer embedded verbatim.
				return redactCoded(httpErr(resp.StatusCode, raw), resolved.Token)
			}
			if err := writeFindingsExport(cmd.OutOrStdout(), outPath, resp.Body); err != nil {
				return err
			}
			if strings.EqualFold(strings.TrimSpace(resp.Header.Get("X-Olivares-Truncated")), "true") {
				_, err = fmt.Fprintln(cmd.ErrOrStderr(),
					"WARNING: SARIF export truncated at 25000 findings; narrow the server-side filters and export again")
			}
			return err
		},
	}
	cmd.Flags().StringVar(&format, "format", "sarif",
		"export format: sarif (this selects the EXPORT format and is fully supported — "+
			"it is not the deprecated -o/--output alias other commands spell the same way)")
	cmd.Flags().StringVar(&outPath, "out", "", "output file (default: stdout)")
	_ = cmd.RegisterFlagCompletionFunc("format", func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
		return []string{"sarif"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

// writeFindingsExport keeps a failed or interrupted network copy from replacing
// an existing export with a partial SARIF document.
func writeFindingsExport(stdout io.Writer, outPath string, src io.Reader) error {
	outPath = strings.TrimSpace(outPath)
	if outPath == "" || outPath == "-" {
		_, err := io.Copy(stdout, src)
		return err
	}

	target := filepath.Clean(outPath)
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary findings export: %w", err)
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("secure temporary findings export: %w", err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		return fmt.Errorf("write findings export: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync findings export: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close findings export: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return fmt.Errorf("replace findings export %s: %w", target, err)
	}
	keep = true
	return nil
}
