// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func newMessageDecisionCmd() *cobra.Command {
	root := &cobra.Command{
		Use: "decision", Short: "Answer decision requests attached to work",
		Long:    "Answer a decision request as its currently authorized owner. The engine checks the deadline and records the response with the work decision atomically.",
		Example: "  olivares message decision respond <request-id> yes --version 1", Args: cobra.NoArgs,
	}
	cfg := &agentClientConfig{}
	var version int64
	var key, reason string
	respond := &cobra.Command{
		Use: "respond <request-id> <choice>", Short: "Resolve a decision request with one offered choice",
		Long:    "Submit one of the request's offered choice keys using its current version. Retain the idempotency key for an ambiguous retry; an ended credential or an elapsed deadline never grants permission.",
		Example: "  olivares message decision respond <request-id> yes --version 1 --reason 'Ready to proceed'",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !validSessionToolID(model.ID(args[0])) {
				return workUsagef("request-id must be a canonical UUIDv7")
			}
			if version < 1 {
				return workUsagef("--version must be the current request version, greater than zero")
			}
			if strings.TrimSpace(args[1]) == "" {
				return workUsagef("choice must be one of the request's offered keys")
			}
			if key != "" && !validSessionToolID(model.ID(key)) {
				return workUsagef("--idempotency-key must be a canonical UUIDv7")
			}
			if err := resolveMessageClientConfig(cfg); err != nil {
				return redactCoded(err, cfg.token)
			}
			if key == "" {
				key = model.NewID().String()
				fmt.Fprintf(cmd.ErrOrStderr(), "idempotency key: %s (reuse this key for an ambiguous retry)\n", key)
			}
			body := sessions.DecisionRequestResponseCommand{
				Transition: sessions.DecisionResolve,
				Response:   sessions.DecisionResponseContent{ChoiceKey: args[1], Reason: sessions.CommunicationReasonContent{Code: "resolved", Text: reason}},
			}
			headers := make(http.Header)
			headers.Set("If-Match", fmt.Sprintf("\"v%d\"", version))
			headers.Set("Idempotency-Key", key)
			result, err := workDo(cmd.Context(), cfg, http.MethodPost, workAPIBase+"/decision-requests/"+args[0]+"/responses", body, headers, false)
			if err != nil {
				return redactCoded(err, cfg.token)
			}
			return renderWorkResponse(cmd, bytes.ReplaceAll(result.body, []byte(cfg.token), []byte("[redacted]")))
		},
	}
	respond.Flags().Int64Var(&version, "version", 0, "current decision request version")
	respond.Flags().StringVar(&key, "idempotency-key", "", "stable UUIDv7 key for exact retries (generated if omitted)")
	respond.Flags().StringVar(&reason, "reason", "", "reason for the choice")
	cfg.addFlags(respond)
	root.AddCommand(respond)
	return root
}
