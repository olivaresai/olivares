// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	mcpToolPinsPath             = "/v1/m/capabilities/toolpins"
	mcpFingerprintDisplayLength = 16
	maxMCPCLIResponseSize       = 1 << 20
)

type mcpToolPin struct {
	Tool             string `json:"tool"`
	Fingerprint      string `json:"fingerprint"`
	PinnedAt         string `json:"pinned_at"`
	UpdatedAt        string `json:"updated_at"`
	PinCount         int    `json:"pin_count"`
	DriftFingerprint string `json:"drift_fingerprint,omitempty"`
	DriftAt          string `json:"drift_at,omitempty"`
}

type mcpToolPinList struct {
	Items []mcpToolPin `json:"items"`
}

type mcpToolPinAction struct {
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type mcpToolPinActionInput struct {
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint,omitempty"`
	FromDrift   bool   `json:"from_drift,omitempty"`
}

func newMCPCmd() *cobra.Command {
	flags := &authClientFlags{}
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Govern Model Context Protocol resources",
		Long: "Govern Model Context Protocol resources exposed by the control plane. Connection,\n" +
			"credential and TLS values use the same resolution order and trust controls as `auth`.",
		Example: `  olivares mcp pins ls
  olivares mcp --server https://plane.example.com --tenant tenant-a pins ls`,
		Args: cobra.NoArgs,
	}
	flags.addPersistent(cmd)
	cmd.AddCommand(newMCPPinsCmd(flags))
	return cmd
}

func newMCPPinsCmd(flags *authClientFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pins",
		Short: "List and manage approved MCP tool fingerprints",
		Long: "List, approve and remove tenant-scoped MCP tool fingerprints. Tool pins detect\n" +
			"definition drift and are enforced by the enterprise tool-pin verifier.",
		Example: `  olivares mcp pins ls
  olivares mcp pins approve github.search --from-drift
  olivares mcp pins rm github.search`,
		Args: cobra.NoArgs,
	}
	client := mcpPinsClient{flags: flags}
	cmd.AddCommand(newMCPPinsListCmd(client), newMCPPinsApproveCmd(client), newMCPPinsRemoveCmd(client))
	return cmd
}

func newMCPPinsListCmd(client mcpPinsClient) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List approved MCP tool fingerprints and current drift",
		Long: "List this tenant's approved MCP tool fingerprints, approval counts and any\n" +
			"currently observed definition drift. JSON output preserves the raw API response.",
		Example: `  olivares mcp pins ls
  olivares mcp pins ls --tenant tenant-a -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, status, bearer, err := client.do(cmd, http.MethodGet, mcpToolPinsPath, nil)
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				// THE ERROR BODY IS THE PLANE'S, AND IT MAY CONTAIN OUR BEARER: httpErr
				// embeds it verbatim. redactCoded scrubs it and keeps the exit code, so
				// a script can still tell 401 from 500 (bootstrapclient.go:168 does the
				// same for the first-run families).
				return redactCoded(mcpPinsHTTPError(status, raw), bearer)
			}
			var pins mcpToolPinList
			if err := json.Unmarshal(raw, &pins); err != nil {
				return exitcode.New(exitcode.Server, fmt.Errorf("decode tool pins response: %w", err))
			}
			return renderOut(cmd, func(out io.Writer) error {
				tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "TOOL\tFINGERPRINT\tPINNED\tCOUNT\tDRIFT")
				for _, pin := range pins.Items {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
						safeCLIValue(pin.Tool, ""),
						formatMCPFingerprint(pin.Fingerprint),
						defaultMCPTableValue(pin.PinnedAt),
						pin.PinCount,
						formatMCPFingerprint(pin.DriftFingerprint),
					)
				}
				return tw.Flush()
			}, json.RawMessage(raw))
		},
	}
}

func newMCPPinsApproveCmd(client mcpPinsClient) *cobra.Command {
	var (
		fingerprint string
		fromDrift   bool
	)
	cmd := &cobra.Command{
		Use:   "approve <tool>",
		Short: "Approve an explicit or currently drifted tool fingerprint",
		Long: "Approve a fingerprint for one MCP tool. Supply exactly one of --fingerprint for\n" +
			"an explicit value or --from-drift to approve the server's current drift observation.",
		Example: `  olivares mcp pins approve github.search --fingerprint sha256:abc123
  olivares mcp pins approve github.search --from-drift`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMCPToolPins,
		RunE: func(cmd *cobra.Command, args []string) error {
			fingerprint = strings.TrimSpace(fingerprint)
			if (fingerprint == "") == !fromDrift {
				return exitcode.New(exitcode.Usage,
					fmt.Errorf("supply exactly one of --fingerprint or --from-drift"))
			}
			input := mcpToolPinActionInput{
				Tool: args[0], Fingerprint: fingerprint, FromDrift: fromDrift,
			}
			raw, status, bearer, err := client.do(cmd, http.MethodPost, mcpToolPinsPath+"/approve", input)
			if err != nil {
				return err
			}
			if status == http.StatusConflict && fromDrift {
				return exitcode.New(exitcode.Conflict,
					fmt.Errorf("no current drift to approve for tool %q", safeCLIValue(args[0], "")))
			}
			if status != http.StatusOK {
				// THE ERROR BODY IS THE PLANE'S, AND IT MAY CONTAIN OUR BEARER: httpErr
				// embeds it verbatim. redactCoded scrubs it and keeps the exit code, so
				// a script can still tell 401 from 500 (bootstrapclient.go:168 does the
				// same for the first-run families).
				return redactCoded(mcpPinsHTTPError(status, raw), bearer)
			}
			var result mcpToolPinAction
			if err := json.Unmarshal(raw, &result); err != nil {
				return exitcode.New(exitcode.Server, fmt.Errorf("decode tool-pin approval response: %w", err))
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "approved %s at %s\n",
					safeCLIValue(result.Tool, ""), formatMCPFingerprint(result.Fingerprint))
				return err
			}, json.RawMessage(raw))
		},
	}
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "explicit tool-definition fingerprint to approve")
	cmd.Flags().BoolVar(&fromDrift, "from-drift", false, "approve the tool's current drift fingerprint")
	return cmd
}

func newMCPPinsRemoveCmd(client mcpPinsClient) *cobra.Command {
	return &cobra.Command{
		Use:     "rm <tool>",
		Aliases: []string{"remove", "unpin"},
		Short:   "Remove an approved MCP tool fingerprint",
		Long: "Remove this tenant's approved fingerprint for one MCP tool. A tool without an\n" +
			"existing pin is reported as not found.",
		Example:           "  olivares mcp pins rm github.search",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeMCPToolPins,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, status, bearer, err := client.do(cmd, http.MethodPost, mcpToolPinsPath+"/unpin",
				mcpToolPinActionInput{Tool: args[0]})
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				// THE ERROR BODY IS THE PLANE'S, AND IT MAY CONTAIN OUR BEARER: httpErr
				// embeds it verbatim. redactCoded scrubs it and keeps the exit code, so
				// a script can still tell 401 from 500 (bootstrapclient.go:168 does the
				// same for the first-run families).
				return redactCoded(mcpPinsHTTPError(status, raw), bearer)
			}
			var result mcpToolPinAction
			if err := json.Unmarshal(raw, &result); err != nil {
				return exitcode.New(exitcode.Server, fmt.Errorf("decode tool-pin removal response: %w", err))
			}
			return renderOut(cmd, func(out io.Writer) error {
				_, err := fmt.Fprintf(out, "unpinned %s\n", safeCLIValue(result.Tool, ""))
				return err
			}, json.RawMessage(raw))
		},
	}
}

type mcpPinsClient struct {
	flags *authClientFlags
}

// do performs one JSON request and returns the raw body, the status AND THE
// BEARER IT SENT.
//
// The bearer is a return value for the same reason bootstrapClient.do returns one
// (bootstrapclient.go:78): the response body belongs to the plane, httpErr embeds
// it verbatim (cmd_agent.go:589), and a proxy, a WAF or a badly written error
// page that reflects the request headers puts this CLI's own Authorization header
// straight into the operator's terminal. The verb that turns a status into an
// error is the one that must redact it — and until this signature changed it
// COULD NOT: resolved.Token died inside this function, so every caller of
// mcpPinsHTTPError had no secret to hand to redactCoded. That was a structural
// hole, not an oversight at three call sites.
func (c mcpPinsClient) do(cmd *cobra.Command, method, path string, body any) ([]byte, int, string, error) {
	resolved, err := c.flags.resolve(cmd)
	if err != nil {
		return nil, 0, "", redactCoded(err, c.flags.effectiveToken())
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved:       resolved,
		Insecure:       c.flags.insecure,
		Timeout:        c.flags.timeout,
		Stderr:         cmd.ErrOrStderr(),
		AllowCleartext: c.flags.allowCleartext,
	})
	if err != nil {
		return nil, 0, resolved.Token, redactCodedServer(err, resolved.Token)
	}
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, 0, resolved.Token, err
		}
		requestBody = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, resolved.Server+path, requestBody)
	if err != nil {
		return nil, 0, resolved.Token, redactCodedServer(err, resolved.Token)
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cliDo(client, req)
	if err != nil {
		return nil, 0, resolved.Token, redactCodedServer(err, resolved.Token)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPCLIResponseSize+1))
	if err != nil {
		return nil, resp.StatusCode, resolved.Token, exitcode.New(exitcode.Server, fmt.Errorf("read tool-pins response: %w", err))
	}
	if len(raw) > maxMCPCLIResponseSize {
		return nil, resp.StatusCode, resolved.Token, exitcode.New(exitcode.Server,
			fmt.Errorf("tool-pins response exceeds %d bytes", maxMCPCLIResponseSize))
	}
	return raw, resp.StatusCode, resolved.Token, nil
}

func (f *authClientFlags) addPersistent(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.server, "server", "", "control-plane base URL (default $OLIVARES_SERVER_URL, then current context)")
	cmd.PersistentFlags().StringVar(&f.token, "token", "", "API bearer token (prefer --token-file: this form is visible in the process table and in shell history; default $OLIVARES_TOKEN, then current context)")
	cmd.PersistentFlags().StringVar(&f.tokenFile, "token-file", "", "read the API bearer token from a file, or - for stdin")
	cmd.PersistentFlags().BoolVar(&f.allowCleartext, "allow-cleartext", false, "allow sending the credential to a non-loopback host over plain HTTP (DANGEROUS: it travels readable)")
	cmd.PersistentFlags().StringVar(&f.tenant, "tenant", "", "tenant id (default $OLIVARES_TENANT, then current context)")
	cmd.PersistentFlags().StringVar(&f.caCert, "ca-cert", "", "PEM file containing an additional trusted root CA (default: current context)")
	cmd.PersistentFlags().StringArrayVar(&f.pins, "pin-sha256", nil, "trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context)")
	cmd.PersistentFlags().BoolVar(&f.insecure, "insecure", false, "skip TLS certificate verification (DANGEROUS; development only)")
	cmd.PersistentFlags().DurationVar(&f.timeout, "timeout", defaultCLIRequestTimeout, "request timeout")
}

func completeMCPToolPins(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return completeFromAPI(mcpToolPinsPath, "tool")
}

// mcpPinsHTTPError names the ONE refusal on these routes that httpErr's generic
// wording cannot explain: the tool-pin verifier lives in the enterprise add-on
// and the open-core engine answers 501 by design.
//
// THE 501 CODE IS CHOSEN HERE, NOT LEFT TO FALL OUT. A bare fmt.Errorf carries no
// exitcode.coded, so exitcode.From read it as the generic 1 — right by accident,
// and only until someone rewrote the line. Through httpErr the same 501 would
// have been Server(6), i.e. "the plane failed", which an add-on boundary is not.
// complianceHTTPError (cmd_compliance.go:271) states Err(1) for the identical
// case; this now says the same thing out loud instead of inheriting it.
//
// It does not redact: it is never handed a secret. Its callers hold the bearer
// (mcpPinsClient.do returns it) and wrap this in redactCoded, which is the only
// place that can scrub the plane's body without losing the code above.
func mcpPinsHTTPError(status int, body []byte) error {
	if status == http.StatusNotImplemented {
		return exitcode.New(exitcode.Err,
			fmt.Errorf("MCP tool pinning requires the enterprise add-on (HTTP 501)"))
	}
	return httpErr(status, body)
}

func formatMCPFingerprint(fingerprint string) string {
	if fingerprint == "" {
		return "-"
	}
	if len(fingerprint) <= mcpFingerprintDisplayLength {
		return safeCLIValue(fingerprint, "")
	}
	return safeCLIValue(fingerprint[:mcpFingerprintDisplayLength], "") + "…"
}

func defaultMCPTableValue(value string) string {
	if value == "" {
		return "-"
	}
	return safeCLIValue(value, "")
}
