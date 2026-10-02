// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Legacy communication CLI envelopes are kept here. They are not advertised or
// callable by /session/mcp and do not adapt the new session principal.
type sessionInboxArgs struct {
	Limit        int    `json:"limit,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}
type sessionSendArgs struct {
	ChannelID      model.ID `json:"channel_id"`
	ToSID          string   `json:"to_sid"`
	Subject        string   `json:"subject"`
	Text           string   `json:"text"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type sessionAckArgs struct {
	ID             model.ID `json:"id"`
	Version        int64    `json:"version"`
	IdempotencyKey string   `json:"idempotency_key"`
}
type sessionHandoffInboxArgs struct {
	State        string `json:"state,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}
type sessionHandoffOfferArgs struct {
	ChannelID          model.ID                `json:"channel_id"`
	WorkItemID         model.ID                `json:"work_item_id"`
	ToSID              string                  `json:"to_sid"`
	Handoff            sessions.HandoffContent `json:"handoff"`
	AckDeadline        string                  `json:"ack_deadline"`
	ExpectedOwnerEpoch int64                   `json:"expected_owner_epoch"`
	Version            int64                   `json:"version"`
	IdempotencyKey     string                  `json:"idempotency_key"`
}
type sessionHandoffRespondArgs struct {
	ID             model.ID                             `json:"id"`
	Transition     sessions.HandoffTransition           `json:"transition"`
	Reason         *sessions.CommunicationReasonContent `json:"reason,omitempty"`
	Version        int64                                `json:"version"`
	IdempotencyKey string                               `json:"idempotency_key"`
}

func messageToolRequest(ctx context.Context, p auth.Principal, name string, raw json.RawMessage) (*http.Request, error) {
	method, path, query := http.MethodGet, "", url.Values{}
	var body any
	headers := http.Header{}
	decode := func(v any) error {
		if len(raw) == 0 {
			raw = []byte(`{}`)
		}
		if strictSessionJSON(bytes.NewReader(raw), v) != nil {
			return errors.New("Invalid arguments; use the schema from tools/list")
		}
		return nil
	}
	switch name {
	case "olivares_session_delivery", "olivares_session_handoff_get":
		var args sessionGetArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ID) || args.Kind != "" {
			return nil, errors.New("delivery requires a UUIDv7 and does not accept kind")
		}
		path = "/deliveries/" + args.ID.String()
		if name == "olivares_session_handoff_get" {
			path += "/handoff"
		}
	case "olivares_session_inbox":
		var args sessionInboxArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		path = "/inbox"
		query.Set("workspace_id", p.SessionWorkspaceID.String())
		if args.Limit < 0 || args.Limit > 200 {
			return nil, errors.New("limit must be 1..200")
		}
		if args.Limit > 0 {
			query.Set("limit", strconv.Itoa(args.Limit))
		}
		if args.Continuation != "" {
			query.Set("continuation", args.Continuation)
		}
	case "olivares_session_send":
		var args sessionSendArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ChannelID) || !validMessageToolSID(args.ToSID) || args.Subject == "" || args.Text == "" || args.IdempotencyKey == "" {
			return nil, errors.New("Provide channel_id UUIDv7, exact to_sid, subject, text and stable idempotency_key")
		}
		method, path = http.MethodPost, "/messages/send"
		headers.Set("Idempotency-Key", args.IdempotencyKey)
		body = sessions.DirectNoticePublishCommand{ChannelID: args.ChannelID, Recipient: sessions.RecipientRef{Kind: sessions.RecipientSession, Ref: args.ToSID}, Content: sessions.MessageContent{Subject: args.Subject, Blocks: []sessions.MessageContentBlock{{Type: sessions.ContentBlockText, Format: sessions.TextPlain, Text: args.Text}}}}
	case "olivares_session_ack":
		var args sessionAckArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ID) || args.Version < 1 || !validSessionToolID(model.ID(args.IdempotencyKey)) {
			return nil, errors.New("Provide delivery id, current version and canonical UUIDv7 idempotency_key")
		}
		method, path, body = http.MethodPost, "/deliveries/"+args.ID.String()+"/ack", map[string]any{}
		headers.Set("If-Match", fmt.Sprintf(`"v%d"`, args.Version))
		headers.Set("Idempotency-Key", args.IdempotencyKey)
	case "olivares_session_handoff_inbox":
		var args sessionHandoffInboxArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if args.Limit < 0 || args.Limit > 200 {
			return nil, errors.New("limit must be 1..200")
		}
		if args.State == "" {
			args.State = "offered"
		}
		switch args.State {
		case "offered", "accepted", "rejected", "withdrawn", "expired":
		default:
			return nil, errors.New("Provide one handoff state from tools/list")
		}
		path = "/inbox/handoffs"
		query.Set("workspace_id", p.SessionWorkspaceID.String())
		query.Set("state", args.State)
		if args.Limit > 0 {
			query.Set("limit", strconv.Itoa(args.Limit))
		}
		if args.Continuation != "" {
			query.Set("continuation", args.Continuation)
		}
	case "olivares_session_handoff_offer":
		var args sessionHandoffOfferArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		deadline, err := time.Parse(time.RFC3339Nano, args.AckDeadline)
		if err != nil || !validSessionToolID(args.ChannelID) || !validSessionToolID(args.WorkItemID) || !validMessageToolSID(args.ToSID) || args.Version < 1 || args.ExpectedOwnerEpoch < 1 || !validSessionToolID(model.ID(args.IdempotencyKey)) {
			return nil, errors.New("Provide exact channel/work/SID, current work version/owner epoch, RFC3339 deadline and UUIDv7 idempotency_key")
		}
		method, path = http.MethodPost, "/handoffs"
		body = sessions.WorkItemHandoffOfferCommand{ChannelID: args.ChannelID, WorkItemID: args.WorkItemID, Recipient: sessions.RecipientRef{Kind: sessions.RecipientSession, Ref: args.ToSID}, Content: args.Handoff, AckDeadline: deadline, ExpectedOwnerEpoch: args.ExpectedOwnerEpoch}
		headers.Set("If-Match", fmt.Sprintf(`"v%d"`, args.Version))
		headers.Set("Idempotency-Key", args.IdempotencyKey)
	case "olivares_session_handoff_respond":
		var args sessionHandoffRespondArgs
		if err := decode(&args); err != nil {
			return nil, err
		}
		if !validSessionToolID(args.ID) || args.Version < 1 || !validSessionToolID(model.ID(args.IdempotencyKey)) || (args.Transition != sessions.HandoffAccept && args.Transition != sessions.HandoffReject) {
			return nil, errors.New("Provide handoff id/current version, accept or reject, and UUIDv7 idempotency_key")
		}
		if args.Transition == sessions.HandoffAccept && args.Reason != nil || args.Transition == sessions.HandoffReject && args.Reason == nil {
			return nil, errors.New("Reject requires a reason; accept must omit it")
		}
		method, path = http.MethodPost, "/handoffs/"+args.ID.String()+"/responses"
		body = sessions.HandoffResponseCommand{Transition: args.Transition, Reason: args.Reason}
		headers.Set("If-Match", fmt.Sprintf(`"v%d"`, args.Version))
		headers.Set("Idempotency-Key", args.IdempotencyKey)
	default:
		return nil, errors.New("Unknown session tool")
	}
	var encoded io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		encoded = bytes.NewReader(data)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, workAPIBase+path, encoded)
	if err != nil {
		return nil, err
	}
	r.Header = headers
	r.Header.Set("Content-Type", "application/json")
	return r, nil
}
func validMessageToolSID(sid string) bool {
	return strings.HasPrefix(sid, "osn_") && validSessionToolID(model.ID(strings.TrimPrefix(sid, "osn_")))
}
