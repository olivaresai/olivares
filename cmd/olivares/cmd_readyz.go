// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/readyzprobe"
)

type readyzOptions struct {
	server, caCert string
	timeout        time.Duration
}

// newReadyzCmd is the in-binary healthcheck for distroless images. It probes
// only numeric loopback, so it cannot accidentally become a generic SSRF client.
func newReadyzCmd() *cobra.Command {
	o := readyzOptions{server: "https://127.0.0.1:8443", timeout: 3 * time.Second}
	cmd := &cobra.Command{
		Use:           "readyz",
		Short:         "Probe this host's local engine readiness without curl",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: "readyz performs one GET of /readyz on a numeric loopback origin. HTTP 200 is\n" +
			"ready (exit 0); any other HTTP status is not ready (exit 1); input, TLS or\n" +
			"transport failures mean the verdict could not be measured (exit 2). Redirects\n" +
			"are not followed. This command is the distroless Compose healthcheck, and the\n" +
			"local first-boot diagnosis the service installer surfaces when a start does not\n" +
			"become ready. A not-ready answer in a first-boot state this build recognizes is\n" +
			"followed by one diagnosis and one remedy held in this binary; no response body or\n" +
			"header is printed there, and no body can change the status-driven verdict or the\n" +
			"exit code. An unmeasurable verdict is different: it reports the input, TLS or\n" +
			"transport failure that stopped the probe, in that error's own unfiltered words.",
		Example: "  olivares readyz --server https://127.0.0.1:8443 \\\n" +
			"    --ca-cert /var/lib/olivares/tls.crt --timeout 3s",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := readyzprobe.Check(cmd.Context(), readyzprobe.Config{
				Origin: o.server, CACert: o.caCert, Timeout: o.timeout,
			})
			switch result.Outcome {
			case readyzprobe.Ready:
				if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "ready: %s returned HTTP %d\n", result.Endpoint, result.StatusCode); werr != nil {
					return exitcode.New(exitcode.Usage, fmt.Errorf("write readiness verdict: %w", werr))
				}
				return nil
			case readyzprobe.NotReady:
				// The verdict line is unchanged and comes first; the optional
				// diagnosis is appended after it, so a caller that reads only the
				// first line still reads exactly what it read before.
				verdict := fmt.Sprintf("not ready: %s returned HTTP %d\n", result.Endpoint, result.StatusCode)
				if result.Diagnosis != "" {
					verdict += fmt.Sprintf("diagnosis: %s\nremedy: %s\n", result.Diagnosis, result.Remedy)
				}
				if _, werr := fmt.Fprint(cmd.ErrOrStderr(), verdict); werr != nil {
					return exitcode.New(exitcode.Usage, fmt.Errorf("write readiness verdict: %w", werr))
				}
				return exitcode.New(exitcode.Err, nil)
			default:
				if err == nil {
					err = errors.New("readiness verdict is unavailable")
				}
				if _, werr := fmt.Fprintf(cmd.ErrOrStderr(), "cannot inspect readiness: %v\n", err); werr != nil {
					return exitcode.New(exitcode.Usage, fmt.Errorf("write readiness verdict: %w", werr))
				}
				return exitcode.New(exitcode.Usage, nil)
			}
		},
	}
	cmd.Flags().StringVar(&o.server, "server", o.server, "local numeric-loopback HTTP(S) origin")
	cmd.Flags().StringVar(&o.caCert, "ca-cert", "", "PEM trust anchor for HTTPS (Compose: <data-dir>/tls.crt)")
	cmd.Flags().DurationVar(&o.timeout, "timeout", o.timeout, "whole-probe deadline")
	return cmd
}
