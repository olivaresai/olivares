// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/olivaresai/olivares/core/envconfig"
	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func resolveMessageClientConfig(cfg *agentClientConfig) error {
	if !cfg.changed("token") && cfg.token == "" {
		cfg.token = envconfig.Get("OLIVARES_COMMUNICATION_TOKEN")
	}
	return cfg.resolve()
}

func newMessageCmd() *cobra.Command {
	root := &cobra.Command{Use: "message", Short: "Exchange exact-session messages through governed channels", Long: "Read, send and acknowledge messages as the authenticated session through operator-authorized channels. Uses the session communication credential from OLIVARES_COMMUNICATION_TOKEN; it does not substitute human or work authority.", Example: "  olivares message inbox --workspace-id <workspace-id>", Args: cobra.NoArgs}
	for _, action := range []string{"inbox", "get", "send", "ack"} {
		root.AddCommand(newMessageActionCmd(action))
	}
	handoff := &cobra.Command{Use: "handoff", Short: "Offer and respond to exact-session work handoffs", Long: "Offer owned work to one exact session and inspect or answer incoming handoffs. Protected context is read through the carrier delivery; accepting a handoff uses the current handoff version and an idempotency key.", Example: "  olivares message handoff inbox --workspace-id <workspace-id>", Args: cobra.NoArgs}
	for _, action := range []string{"inbox", "get", "offer", "respond"} {
		handoff.AddCommand(newMessageActionCmd("handoff-" + action))
	}
	root.AddCommand(handoff, newMessageDecisionCmd())
	return root
}

func readMessageInput(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("an input file is required (use - for stdin)")
	}
	reader := cmd.InOrStdin()
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer func() { _ = file.Close() }()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("message input exceeds 1 MiB")
	}
	return data, nil
}

func newMessageActionCmd(action string) *cobra.Command {
	cfg := &agentClientConfig{}
	var workspace, channel, sid, subject, textFile, key, continuation string
	var item, deadline, contextFile, transition, reasonFile, state string
	var limit int
	var version, ownerEpoch int64
	use := strings.TrimPrefix(action, "handoff-")
	args := cobra.NoArgs
	if action == "get" || action == "ack" || action == "handoff-get" {
		use += " delivery-id"
		args = cobra.ExactArgs(1)
	}
	if action == "handoff-respond" {
		use += " handoff-id"
		args = cobra.ExactArgs(1)
	}
	cmd := &cobra.Command{Use: use, Short: map[string]string{"inbox": "Read this authenticated session's inbox", "get": "Read this exact session's delivery", "send": "Send plain-text content to one canonical session SID", "ack": "Acknowledge this exact session's delivery", "handoff-inbox": "List this exact session's incoming handoffs", "handoff-get": "Read protected handoff context by carrier delivery", "handoff-offer": "Offer owned work to one exact canonical session SID", "handoff-respond": "Accept or reject this exact session's incoming handoff"}[action], Args: args,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveMessageClientConfig(cfg); err != nil {
				return redactCoded(err, cfg.token)
			}
			name := "olivares_session_" + action
			name = strings.ReplaceAll(name, "-", "_")
			var input any
			switch action {
			case "inbox", "handoff-inbox":
				if !validSessionToolID(model.ID(workspace)) {
					return errors.New("--workspace-id must name the session's UUIDv7 workspace")
				}
				if action == "inbox" {
					input = sessionInboxArgs{Limit: limit, Continuation: continuation}
				} else {
					input = sessionHandoffInboxArgs{State: state, Limit: limit, Continuation: continuation}
				}
			case "get", "handoff-get":
				if action == "get" {
					name = "olivares_session_delivery"
				}
				input = sessionGetArgs{ID: model.ID(args[0])}
			case "send":
				text, err := readMessageInput(cmd, textFile)
				if err != nil {
					return err
				}
				input = sessionSendArgs{ChannelID: model.ID(channel), ToSID: sid, Subject: subject, Text: string(text), IdempotencyKey: key}
			case "ack":
				input = sessionAckArgs{ID: model.ID(args[0]), Version: version, IdempotencyKey: key}
			case "handoff-offer":
				data, err := readMessageInput(cmd, contextFile)
				if err != nil {
					return err
				}
				var content sessions.HandoffContent
				if err := strictSessionJSON(bytes.NewReader(data), &content); err != nil {
					return errors.New("--context-file must contain HandoffContent JSON (summary, next_action, optional risk/branch/sha/artifact_refs)")
				}
				input = sessionHandoffOfferArgs{ChannelID: model.ID(channel), WorkItemID: model.ID(item), ToSID: sid, Handoff: content, AckDeadline: deadline, ExpectedOwnerEpoch: ownerEpoch, Version: version, IdempotencyKey: key}
			case "handoff-respond":
				var reason *sessions.CommunicationReasonContent
				if reasonFile != "" {
					data, err := readMessageInput(cmd, reasonFile)
					if err != nil {
						return err
					}
					reason = &sessions.CommunicationReasonContent{}
					if err := strictSessionJSON(bytes.NewReader(data), reason); err != nil {
						return errors.New("--reason-file must contain CommunicationReasonContent JSON (code, optional text/references)")
					}
				}
				input = sessionHandoffRespondArgs{ID: model.ID(args[0]), Transition: sessions.HandoffTransition(transition), Reason: reason, Version: version, IdempotencyKey: key}
			}
			raw, _ := json.Marshal(input)
			request, err := messageToolRequest(cmd.Context(), auth.Principal{SessionWorkspaceID: model.ID(workspace)}, name, raw)
			if err != nil {
				return err
			}
			// The shared builder supplies only closed paths and payloads. This
			// client's own TLS/context resolution owns the network destination.
			request.URL.Scheme, request.URL.Host = "", ""
			path := request.URL.String()
			outbound, err := cfg.newRequest(cmd.Context(), request.Method, path, nil)
			if err != nil {
				return redactCoded(err, cfg.token)
			}
			outbound.Body = request.Body
			outbound.ContentLength = request.ContentLength
			for name, values := range request.Header {
				outbound.Header[name] = values
			}
			client, err := cfg.transport(cfg.timeout)
			if err != nil {
				return redactCoded(err, cfg.token)
			}
			response, err := cliDo(client, outbound)
			if err != nil {
				return redactCodedServer(err, cfg.token)
			}
			defer func() { _ = response.Body.Close() }()
			result, err := readCLIHTTPResponse(response, outbound, (1<<20)+1, response.StatusCode < 400, workHTTPError)
			if err != nil {
				return wrapCLIResponseReadError(err, "read message response")
			}
			if len(result) > 1<<20 {
				return errors.New("message response exceeds 1 MiB")
			}
			result = bytes.ReplaceAll(result, []byte(cfg.token), []byte("[redacted]"))
			if response.StatusCode >= 400 {
				return redactCoded(workHTTPError(response.StatusCode, result), cfg.token)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(result))
			return err
		}}
	cmd.Long = map[string]string{
		"inbox":           "List deliveries for the authenticated session in its exact workspace. Pagination returns an opaque continuation; no other session's inbox is exposed.",
		"get":             "Read one delivery belonging to the authenticated session by delivery ID. Protected content remains subject to the channel and session's current authority.",
		"send":            "Send a plain-text file to one exact canonical session SID through an operator-authorized channel. Retain the idempotency key for retries; the server checks sender and recipient authority.",
		"ack":             "Acknowledge one delivery belonging to the authenticated session. Supply its current version and a canonical UUIDv7 idempotency key.",
		"handoff-inbox":   "List incoming work handoffs for the authenticated session in its exact workspace, filtered by one state. Use the opaque continuation for subsequent pages.",
		"handoff-get":     "Read one protected handoff context through its carrier delivery ID. The authenticated session must remain an authorized recipient.",
		"handoff-offer":   "Offer owned work to one exact canonical session SID through an authorized channel. Supply the work item's current version and owner epoch, a future acknowledgement deadline and a HandoffContent JSON file.",
		"handoff-respond": "Accept or reject an incoming handoff as the authenticated session. Supply its current version and a canonical UUIDv7 idempotency key; rejection also requires a CommunicationReasonContent JSON file.",
	}[action]
	cmd.Example = map[string]string{
		"inbox":           "  olivares message inbox --workspace-id <workspace-id>",
		"get":             "  olivares message get <delivery-id>",
		"send":            "  olivares message send --channel-id <channel-id> --to-sid <osn_UUID> --text-file message.txt --idempotency-key <key>",
		"ack":             "  olivares message ack <delivery-id> --version 1 --idempotency-key <key>",
		"handoff-inbox":   "  olivares message handoff inbox --workspace-id <workspace-id>",
		"handoff-get":     "  olivares message handoff get <delivery-id>",
		"handoff-offer":   "  olivares message handoff offer --channel-id <channel-id> --to-sid <osn_UUID> --work-item-id <item-id> --owner-epoch 1 --version 1 --ack-deadline <RFC3339> --context-file handoff.json --idempotency-key <key>",
		"handoff-respond": "  olivares message handoff respond <handoff-id> --transition accept --version 1 --idempotency-key <key>",
	}[action]
	cfg.addFlags(cmd)
	cmd.Flags().Lookup("token").Usage = "API bearer token (default $OLIVARES_COMMUNICATION_TOKEN, then normal client resolution)"
	switch action {
	case "inbox", "handoff-inbox":
		cmd.Flags().StringVar(&workspace, "workspace-id", "", "exact session workspace UUID")
		cmd.Flags().IntVar(&limit, "limit", 50, "page size (1..200)")
		cmd.Flags().StringVar(&continuation, "continuation", "", "opaque continuation from the previous inbox page")
		if action == "handoff-inbox" {
			cmd.Flags().StringVar(&state, "state", "offered", "one handoff state: offered, accepted, rejected, withdrawn or expired")
		}
	case "send":
		cmd.Flags().StringVar(&channel, "channel-id", "", "operator-authorized channel UUID")
		cmd.Flags().StringVar(&sid, "to-sid", "", "exact recipient canonical SID (osn_UUID)")
		cmd.Flags().StringVar(&subject, "subject", "", "message subject")
		cmd.Flags().StringVar(&textFile, "text-file", "", "plain-text file, or - for stdin")
		cmd.Flags().StringVar(&key, "idempotency-key", "", "stable key retained for exact retries")
	case "ack":
		cmd.Flags().Int64Var(&version, "version", 0, "current delivery version")
		cmd.Flags().StringVar(&key, "idempotency-key", "", "canonical UUIDv7 key retained for exact retries")
	case "handoff-offer":
		cmd.Flags().StringVar(&channel, "channel-id", "", "operator-authorized channel UUID")
		cmd.Flags().StringVar(&sid, "to-sid", "", "exact recipient canonical SID (osn_UUID)")
		cmd.Flags().StringVar(&item, "work-item-id", "", "owned work item UUID")
		cmd.Flags().Int64Var(&ownerEpoch, "owner-epoch", 0, "owner epoch from the current work read")
		cmd.Flags().Int64Var(&version, "version", 0, "current work item version")
		cmd.Flags().StringVar(&deadline, "ack-deadline", "", "future acknowledgment deadline in RFC3339")
		cmd.Flags().StringVar(&contextFile, "context-file", "", "HandoffContent JSON file, or - for stdin")
		cmd.Flags().StringVar(&key, "idempotency-key", "", "canonical UUIDv7 key retained for exact retries")
	case "handoff-respond":
		cmd.Flags().StringVar(&transition, "transition", "", "accept or reject")
		cmd.Flags().StringVar(&reasonFile, "reason-file", "", "CommunicationReasonContent JSON required for reject; file or - for stdin")
		cmd.Flags().Int64Var(&version, "version", 0, "current handoff version from the protected detail")
		cmd.Flags().StringVar(&key, "idempotency-key", "", "canonical UUIDv7 key retained for exact retries")
	}
	return cmd
}
