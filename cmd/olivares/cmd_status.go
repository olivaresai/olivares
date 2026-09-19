// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

type statusClientConfig struct {
	server         string
	caCert         string
	pins           []string
	insecure       bool
	timeout        time.Duration
	stderr         io.Writer
	serverExplicit bool
	caCertExplicit bool
	pinsExplicit   bool
}

type statusResponse struct {
	Status                string            `json:"status"`
	Components            []statusComponent `json:"components"`
	Timestamp             string            `json:"timestamp"`
	EmbedderKind          string            `json:"embedder_kind"`
	RetrievalSemantic     bool              `json:"retrieval_semantic"`
	KnowledgeStatusReason string            `json:"knowledge_status_reason"`
	GuardProfile          string            `json:"guard_profile"`
	GuardWarning          string            `json:"guard_warning"`
	GuardDowngradeCount   int               `json:"guard_downgrade_count"`
}

type statusComponent struct {
	Name                string `json:"name"`
	Status              string `json:"status"`
	EmbedderKind        string `json:"embedder_kind,omitempty"`
	RetrievalSemantic   *bool  `json:"retrieval_semantic,omitempty"`
	Reason              string `json:"reason,omitempty"`
	GuardProfile        string `json:"guard_profile,omitempty"`
	GuardWarning        string `json:"guard_warning,omitempty"`
	GuardDowngradeCount int    `json:"guard_downgrade_count,omitempty"`
}

func newStatusCmd() *cobra.Command {
	var cfg statusClientConfig
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the engine public status, including knowledge retrieval posture",
		Long: "Reads the existing unauthenticated GET /status endpoint and prints the engine\n" +
			"status plus the knowledge embedder posture (semantic vs local-hash).",
		Example: "  olivares status --server https://127.0.0.1:8443 --insecure",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg.stderr = cmd.ErrOrStderr()
			cfg.serverExplicit = cmd.Flags().Changed("server")
			cfg.caCertExplicit = cmd.Flags().Changed("ca-cert")
			cfg.pinsExplicit = cmd.Flags().Changed("pin-sha256")
			res, raw, _, err := cfg.fetch(cmd.Context())
			if err != nil {
				return err
			}
			// Exit contract: an engine reporting a FAULT exits Degraded so
			// probes/CI can branch on `olivares status` without parsing output.
			// The report itself still prints below — the coded error is silent.
			//
			// A correct install that was simply never given an optional provider
			// is NOT a fault: it exits 0 and names what is unprovisioned in the
			// report (statusNotConfigured). Exiting 7 there made every pristine
			// install fail its own health check on day one, which is how a real
			// 7 stops meaning anything. Anything this build does not recognize
			// still exits 7: an unreadable verdict is never treated as healthy.
			var degraded error
			if !statusIsHealthy(res.Status) {
				degraded = exitcode.New(exitcode.Degraded, nil)
			}
			if err := renderOut(cmd, func(out io.Writer) error {
				r := renderTo(out)
				head := []termrender.Field{{Key: "status", Value: res.Status, Role: componentRole(res.Status)}}
				// Name the unprovisioned capabilities up front: exiting 0 must
				// never be read as "everything is configured here".
				if pending := notConfiguredComponents(res.Components); len(pending) > 0 {
					head = append(head, termrender.Field{
						Key: "not_configured", Value: strings.Join(pending, " "), Role: termrender.RoleWarn,
					})
				}
				head = append(head,
					termrender.Field{Key: "updated", Value: res.Timestamp},
					termrender.Field{Key: "embedder_kind", Value: res.EmbedderKind},
					termrender.Field{Key: "retrieval_semantic", Value: flagCell(res.RetrievalSemantic)},
					termrender.Field{Key: "guard_profile", Value: res.GuardProfile},
				)
				if res.GuardDowngradeCount > 0 {
					head = append(head, termrender.Field{
						Key: "guard_downgrades", Value: countCell(res.GuardDowngradeCount), Role: termrender.RoleWarn,
					})
				}
				if res.KnowledgeStatusReason != "" {
					head = append(head, termrender.Field{Key: "knowledge_reason", Value: res.KnowledgeStatusReason})
				}
				if res.GuardWarning != "" {
					head = append(head, termrender.Field{
						Key: "knowledge_guard_warning", Value: res.GuardWarning, Role: termrender.RoleWarn,
					})
				}
				r.Fields(head)
				r.Blank()
				tbl := termrender.Table{
					Header: []string{"component", "status", "detail"},
					Empty:  "this engine reports no component",
				}
				for _, c := range res.Components {
					detail := c.Reason
					if c.Name == "knowledge" {
						detail = fmt.Sprintf("embedder=%s semantic=%t guard=%s guard_downgrades=%d reason=%s guard_warning=%s", c.EmbedderKind, boolValue(c.RetrievalSemantic), c.GuardProfile, c.GuardDowngradeCount, c.Reason, c.GuardWarning)
					}
					tbl.Rows = append(tbl.Rows, []string{c.Name, c.Status, detail})
					tbl.Roles = append(tbl.Roles, []termrender.Role{0, componentRole(c.Status)})
				}
				r.Table(tbl)
				return nil
			}, json.RawMessage(raw)); err != nil {
				return err
			}
			return degraded
		},
	}
	cmd.Flags().StringVar(&cfg.server, "server", "", "control-plane base URL (default $OLIVARES_SERVER_URL, then current context)")
	cmd.Flags().StringVar(&cfg.caCert, "ca-cert", "", "PEM file containing an additional trusted root CA (default: current context)")
	cmd.Flags().StringArrayVar(&cfg.pins, "pin-sha256", nil, "trusted leaf SPKI SHA-256 pin, base64 or hex, repeatable — the engine prints it as pin_sha256 on the line reporting its certificate (default: current context)")
	cmd.Flags().BoolVar(&cfg.insecure, "insecure", false, "skip TLS certificate verification (self-signed dev planes only)")
	cmd.Flags().DurationVar(&cfg.timeout, "timeout", 10*time.Second, "request timeout")
	addDeprecatedJSONFlag(cmd)
	return cmd
}

func (c *statusClientConfig) fetch(ctx context.Context) (statusResponse, []byte, int, error) {
	resolved, err := resolveCLIConfig(cliResolutionOptions{
		Server:         c.server,
		CACert:         c.caCert,
		PinSHA256:      append([]string(nil), c.pins...),
		ServerExplicit: c.serverExplicit || c.server != "",
		CACertExplicit: c.caCertExplicit || c.caCert != "",
		PinsExplicit:   c.pinsExplicit || len(c.pins) > 0,
		// GET /status is public and unauthenticated: never let the active
		// context's token ride to an arbitrary --server (review BLOCKER).
		SkipCredentials: true,
	})
	if err != nil {
		return statusResponse{}, nil, 0, err
	}
	if resolved.Server == "" {
		return statusResponse{}, nil, 0, fmt.Errorf("no server: set --server, OLIVARES_SERVER_URL, or an active client context")
	}
	client, headers, err := cliTransport(cliTransportOptions{
		Resolved: resolved,
		Insecure: c.insecure,
		Timeout:  c.timeout,
		Stderr:   c.stderr,
	})
	if err != nil {
		return statusResponse{}, nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved.Server+"/status", nil)
	if err != nil {
		return statusResponse{}, nil, 0, err
	}
	req.Header = headers.Clone()
	resp, err := cliDo(client, req)
	if err != nil {
		return statusResponse{}, nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return statusResponse{}, nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return statusResponse{}, raw, resp.StatusCode, httpErr(resp.StatusCode, raw)
	}
	var out statusResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return statusResponse{}, raw, resp.StatusCode, err
	}
	return out, raw, resp.StatusCode, nil
}

func boolValue(v *bool) bool {
	return v != nil && *v
}

// The engine's public status vocabulary (core/api PublicStatus). Only these two
// values mean "nothing is broken here"; `degraded`, `outage` and any value this
// build does not know are faults.
const (
	statusOperational   = "operational"
	statusNotConfigured = "not_configured"
)

// statusIsHealthy reports whether the aggregate status means the engine is sound.
// It is deny-closed on purpose: an unrecognized word (an older CLI against a newer
// engine) is a fault, so a genuinely broken plane can never exit 0 because the CLI
// failed to understand it.
func statusIsHealthy(status string) bool {
	return status == statusOperational || status == statusNotConfigured
}

// notConfiguredComponents lists, in the order the engine reported them, the
// components that are present but unprovisioned.
// componentRole colours a component's verdict from the closed set. "not
// configured" is WARN and not FAIL: a capability nobody provisioned is a gap in
// the install, and a fresh install that painted half its components red would
// teach the operator that red is the normal colour of a new machine.
func componentRole(status string) termrender.Role {
	switch status {
	case "ok", "healthy", "ready":
		return termrender.RoleOK
	case "degraded", "not_configured", "unknown":
		return termrender.RoleWarn
	case "fail", "failed", "unhealthy", "error":
		return termrender.RoleFail
	default:
		return termrender.RoleNone
	}
}

func notConfiguredComponents(components []statusComponent) []string {
	var out []string
	for _, c := range components {
		if c.Status == statusNotConfigured {
			out = append(out, c.Name)
		}
	}
	return out
}
