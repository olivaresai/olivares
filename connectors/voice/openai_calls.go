// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	openaiIncomingCallType     = "realtime.call.incoming"
	openaiWebhookSecretPrefix  = "whsec_"
	openaiCallsControlPathBase = "/v1/realtime/calls/"
)

// CallWebhookReplayWindow is the Standard-Webhooks replay tolerance for OpenAI
// Realtime SIP call notifications.
const CallWebhookReplayWindow = 5 * time.Minute

// SIPHeader is one OpenAI Realtime SIP header from realtime.call.incoming,
// verified against the OpenAI wire shape on 2026-07-05. Unknown surrounding JSON
// fields are ignored by ParseCallWebhook.
type SIPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// CallWebhookEvent is the minimal OpenAI Realtime SIP incoming-call event shape,
// verified on 2026-07-05. It intentionally carries parsed SIP headers to the
// caller but never embeds them in errors.
type CallWebhookEvent struct {
	EventID    string
	Type       string
	CreatedAt  time.Time
	CallID     string
	SIPHeaders []SIPHeader
}

// From returns the first SIP From header value, using a case-insensitive header
// name match.
func (e CallWebhookEvent) From() string { return e.sipHeader("From") }

// To returns the first SIP To header value, using a case-insensitive header name
// match.
func (e CallWebhookEvent) To() string { return e.sipHeader("To") }

// SIPCallID returns the first SIP Call-ID header value, using a case-insensitive
// header name match.
func (e CallWebhookEvent) SIPCallID() string { return e.sipHeader("Call-ID") }

func (e CallWebhookEvent) sipHeader(name string) string {
	for _, h := range e.SIPHeaders {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

// callWebhookPayload is the OpenAI incoming-call webhook envelope verified on
// 2026-07-05. It is decoded with json.Unmarshal so unknown fields remain
// tolerated for this external API.
type callWebhookPayload struct {
	Object    string          `json:"object"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	CreatedAt int64           `json:"created_at"`
	Data      callWebhookData `json:"data"`
}

// callWebhookData is the OpenAI incoming-call webhook data object verified on
// 2026-07-05.
type callWebhookData struct {
	CallID     string      `json:"call_id"`
	SIPHeaders []SIPHeader `json:"sip_headers"`
}

// ParseCallWebhook parses an OpenAI Realtime SIP incoming-call webhook body. It
// ignores unknown fields and fails closed for invalid JSON, unexpected event
// types, and missing call IDs.
func ParseCallWebhook(body []byte) (CallWebhookEvent, error) {
	var in callWebhookPayload
	if err := json.Unmarshal(body, &in); err != nil {
		return CallWebhookEvent{}, fmt.Errorf("openai call webhook: invalid json: %w", err)
	}
	if in.Type != openaiIncomingCallType {
		return CallWebhookEvent{}, fmt.Errorf("openai call webhook: unsupported event type")
	}
	if strings.TrimSpace(in.Data.CallID) == "" {
		return CallWebhookEvent{}, fmt.Errorf("openai call webhook: missing call id")
	}
	return CallWebhookEvent{
		EventID:    in.ID,
		Type:       in.Type,
		CreatedAt:  time.Unix(in.CreatedAt, 0).UTC(),
		CallID:     in.Data.CallID,
		SIPHeaders: append([]SIPHeader(nil), in.Data.SIPHeaders...),
	}, nil
}

// VerifyCallWebhook verifies an OpenAI Realtime SIP Standard-Webhooks signature.
// The secret must be whsec_<base64-key>, the signed content is
// "{webhook-id}.{webhook-timestamp}.{body}", and any one matching v1 signature
// accepts. The scheme and header names were verified on 2026-07-05.
func VerifyCallWebhook(h http.Header, body []byte, secret string, now time.Time) error {
	id := strings.TrimSpace(headerValue(h, "webhook-id"))
	tsRaw := strings.TrimSpace(headerValue(h, "webhook-timestamp"))
	sigRaw := strings.TrimSpace(headerValue(h, "webhook-signature"))
	if id == "" || tsRaw == "" || sigRaw == "" {
		return fmt.Errorf("openai call webhook: missing signature headers")
	}

	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("openai call webhook: invalid timestamp")
	}
	sent := time.Unix(ts, 0)
	if sent.Before(now.Add(-CallWebhookReplayWindow)) || sent.After(now.Add(CallWebhookReplayWindow)) {
		return fmt.Errorf("openai call webhook: timestamp outside replay window")
	}

	key, err := deriveCallWebhookKey(secret)
	if err != nil {
		return err
	}
	want := computeCallWebhookMAC(key, id, tsRaw, body)
	for _, tok := range strings.Fields(sigRaw) {
		version, encoded, ok := strings.Cut(tok, ",")
		if !ok || version != "v1" || encoded == "" {
			continue
		}
		got, err := decodeBase64(encoded)
		if err != nil {
			continue
		}
		if hmac.Equal(got, want) {
			return nil
		}
	}
	return fmt.Errorf("openai call webhook: signature mismatch")
}

func headerValue(h http.Header, name string) string {
	if v := h.Get(name); v != "" {
		return v
	}
	for k, vals := range h {
		if strings.EqualFold(k, name) && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

func deriveCallWebhookKey(secret string) ([]byte, error) {
	trimmed := strings.TrimSpace(secret)
	if !strings.HasPrefix(trimmed, openaiWebhookSecretPrefix) {
		return nil, fmt.Errorf("openai call webhook: malformed secret")
	}
	key, err := decodeBase64(strings.TrimPrefix(trimmed, openaiWebhookSecretPrefix))
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("openai call webhook: malformed secret")
	}
	return key, nil
}

func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

func computeCallWebhookMAC(key []byte, id, timestamp string, body []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return mac.Sum(nil)
}

// CallClient controls one OpenAI Realtime SIP call through the REST call-control
// endpoints verified on 2026-07-05. It holds only the API key, base URL, and
// injectable transport.
type CallClient struct {
	APIKey    string
	BaseURL   string
	Transport Transport
}

// AcceptConfig is the GA realtime session object subset accepted by the OpenAI
// SIP accept endpoint, verified on 2026-07-05.
type AcceptConfig struct {
	Model        string
	Instructions string
}

// openaiAcceptRequest is the OpenAI SIP accept request body verified on
// 2026-07-05. Empty fields are omitted except Model, which defaults before
// marshaling.
type openaiAcceptRequest struct {
	Type         string `json:"type"`
	Model        string `json:"model,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

// openaiRejectRequest is the OpenAI SIP reject request body verified on
// 2026-07-05.
type openaiRejectRequest struct {
	StatusCode int `json:"status_code"`
}

// openaiReferRequest is the OpenAI SIP refer request body verified on
// 2026-07-05.
type openaiReferRequest struct {
	TargetURI string `json:"target_uri"`
}

// Accept accepts an incoming SIP call with a governed realtime session object.
func (c CallClient) Accept(ctx context.Context, callID string, cfg AcceptConfig) error {
	model := cfg.Model
	if model == "" {
		model = openaiDefaultModel
	}
	body, err := json.Marshal(openaiAcceptRequest{
		Type:         "realtime",
		Model:        model,
		Instructions: cfg.Instructions,
	})
	if err != nil {
		return fmt.Errorf("openai calls: marshal accept: %w", err)
	}
	return c.post(ctx, callID, "accept", body)
}

// Reject rejects an incoming SIP call. A zero statusCode omits the body so
// OpenAI applies its default rejection status.
func (c CallClient) Reject(ctx context.Context, callID string, statusCode int) error {
	var body []byte
	if statusCode > 0 {
		var err error
		body, err = json.Marshal(openaiRejectRequest{StatusCode: statusCode})
		if err != nil {
			return fmt.Errorf("openai calls: marshal reject: %w", err)
		}
	}
	return c.post(ctx, callID, "reject", body)
}

// Refer transfers a SIP call to targetURI.
func (c CallClient) Refer(ctx context.Context, callID, targetURI string) error {
	body, err := json.Marshal(openaiReferRequest{TargetURI: targetURI})
	if err != nil {
		return fmt.Errorf("openai calls: marshal refer: %w", err)
	}
	return c.post(ctx, callID, "refer", body)
}

// Hangup terminates a live SIP call and is the authoritative call kill switch.
func (c CallClient) Hangup(ctx context.Context, callID string) error {
	return c.post(ctx, callID, "hangup", nil)
}

func (c CallClient) post(ctx context.Context, callID, action string, body []byte) error {
	if strings.TrimSpace(callID) == "" {
		return fmt.Errorf("openai calls: missing call id")
	}

	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = openaiDefaultBase
	}
	endpoint := base + openaiCallsControlPathBase + url.PathEscape(callID) + "/" + action
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, rdr)
	if err != nil {
		return fmt.Errorf("openai calls: build %s request: %w", action, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := defaultTransport(c.Transport).Do(req)
	if err != nil {
		if ue, ok := err.(*url.Error); ok && ue.Err != nil {
			err = ue.Err
		}
		return fmt.Errorf("openai calls: %s request: %w", action, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("openai calls: %s http %d", action, resp.StatusCode)
	}
	return nil
}

// RedactSIPAddress reduces a SIP URI to scheme, host, and the last four digits
// of the user part. Invalid or non-URI input is fully masked.
func RedactSIPAddress(s string) string {
	raw := strings.TrimSpace(s)
	i := strings.IndexByte(raw, ':')
	if i <= 0 || i == len(raw)-1 {
		return "***"
	}
	scheme := raw[:i]
	rest := raw[i+1:]
	if strings.ContainsAny(scheme, " \t\r\n/?#") {
		return "***"
	}
	at := strings.IndexByte(rest, '@')
	if at <= 0 || at == len(rest)-1 {
		return "***"
	}
	user, host := rest[:at], rest[at+1:]
	if host == "" {
		return "***"
	}
	digits := make([]byte, 0, len(user))
	for _, r := range user {
		if r >= '0' && r <= '9' {
			digits = append(digits, byte(r))
		}
	}
	if len(digits) == 0 {
		return "***"
	}
	if len(digits) > 4 {
		digits = digits[len(digits)-4:]
	}
	return scheme + ":***" + string(digits) + "@" + host
}
