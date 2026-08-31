// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/wsclient"
)

const openaiDefaultSidebandBase = "wss://api.openai.com"

// SidebandConn is the minimal live-call sideband socket consumed by the module
// layer. *wsclient.Conn satisfies it structurally.
type SidebandConn interface {
	ReadMessage(ctx context.Context) ([]byte, error)
	WriteText(ctx context.Context, p []byte) error
	Close() error
}

// SidebandDialer attaches to an OpenAI Realtime SIP call sideband channel.
type SidebandDialer func(ctx context.Context, callID string) (SidebandConn, error)

// NewOpenAISidebandDialer returns a dialer for
// wss://api.openai.com/v1/realtime?call_id={call_id}. The model query parameter
// is intentionally not used when attaching by call_id.
func NewOpenAISidebandDialer(apiKey, baseURL string) SidebandDialer {
	base := normalizeSidebandBase(baseURL)
	return func(ctx context.Context, callID string) (SidebandConn, error) {
		if strings.TrimSpace(callID) == "" {
			return nil, fmt.Errorf("openai sideband: missing call id")
		}
		header := http.Header{}
		header.Set("Authorization", "Bearer "+apiKey)
		rawURL := base + "/v1/realtime?call_id=" + url.QueryEscape(callID)
		return wsclient.Dial(ctx, rawURL, header, wsclient.Options{})
	}
}

func normalizeSidebandBase(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return openaiDefaultSidebandBase
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	return strings.TrimRight(u.String(), "/")
}

// SidebandEvent is the OpenAI Realtime SIP sideband event union. The recognized
// wire event names and payload paths were verified on 2026-07-05; parsing is
// tolerant because the external schema may add fields.
type SidebandEvent struct {
	Type       string
	Usage      *ResponseUsage
	Transcript *TranscriptDone
}

// String returns only non-content labels, never transcript text.
func (e SidebandEvent) String() string {
	if e.Usage != nil {
		return fmt.Sprintf("SidebandEvent{Type:%q Usage:%s}", e.Type, e.Usage)
	}
	if e.Transcript != nil {
		return fmt.Sprintf("SidebandEvent{Type:%q Transcript:%s}", e.Type, e.Transcript)
	}
	return fmt.Sprintf("SidebandEvent{Type:%q}", e.Type)
}

// ResponseUsage is the OpenAI Realtime GA response.usage subset observed on
// 2026-07-05. The public docs did not render the full schema, so every path is
// parsed tolerantly and missing numeric fields remain zero.
type ResponseUsage struct {
	ResponseID        string
	Model             string
	TotalTokens       int64
	InputTokens       int64
	OutputTokens      int64
	InputTextTokens   int64
	InputAudioTokens  int64
	CachedTextTokens  int64
	CachedAudioTokens int64
	OutputTextTokens  int64
	OutputAudioTokens int64
}

// String returns only identifiers and aggregate counts, never content.
func (u ResponseUsage) String() string {
	return fmt.Sprintf("ResponseUsage{ResponseID:%q Model:%q TotalTokens:%d}", u.ResponseID, u.Model, u.TotalTokens)
}

// TranscriptDone is the OpenAI input-audio transcription completion payload
// verified on 2026-07-05. Transcript is returned to the caller but deliberately
// omitted from String output.
type TranscriptDone struct {
	ItemID       string
	ContentIndex int
	Transcript   string
}

// String returns only structural transcript metadata, never the transcript text.
func (t TranscriptDone) String() string {
	return fmt.Sprintf("TranscriptDone{ItemID:%q ContentIndex:%d}", t.ItemID, t.ContentIndex)
}

// sidebandTypeEnvelope is the OpenAI sideband event discriminator verified on
// 2026-07-05.
type sidebandTypeEnvelope struct {
	Type string `json:"type"`
}

// responseDoneWire is the OpenAI response.done sideband event shape verified on
// 2026-07-05.
type responseDoneWire struct {
	Type     string `json:"type"`
	Response struct {
		ID    string            `json:"id"`
		Model string            `json:"model"`
		Usage responseUsageWire `json:"usage"`
	} `json:"response"`
}

// responseUsageWire is the OpenAI response.usage shape observed on 2026-07-05;
// missing numeric fields decode to zero.
type responseUsageWire struct {
	TotalTokens        int64                  `json:"total_tokens"`
	InputTokens        int64                  `json:"input_tokens"`
	OutputTokens       int64                  `json:"output_tokens"`
	InputTokenDetails  inputTokenDetailsWire  `json:"input_token_details"`
	OutputTokenDetails outputTokenDetailsWire `json:"output_token_details"`
}

// inputTokenDetailsWire is the OpenAI usage.input_token_details shape observed
// on 2026-07-05.
type inputTokenDetailsWire struct {
	TextTokens          int64                  `json:"text_tokens"`
	AudioTokens         int64                  `json:"audio_tokens"`
	CachedTokensDetails cachedTokenDetailsWire `json:"cached_tokens_details"`
}

// cachedTokenDetailsWire is the OpenAI cached_tokens_details shape observed on
// 2026-07-05.
type cachedTokenDetailsWire struct {
	TextTokens  int64 `json:"text_tokens"`
	AudioTokens int64 `json:"audio_tokens"`
}

// outputTokenDetailsWire is the OpenAI usage.output_token_details shape observed
// on 2026-07-05.
type outputTokenDetailsWire struct {
	TextTokens  int64 `json:"text_tokens"`
	AudioTokens int64 `json:"audio_tokens"`
}

// transcriptDoneWire is the OpenAI transcription completion event shape verified
// on 2026-07-05.
type transcriptDoneWire struct {
	Type         string `json:"type"`
	ItemID       string `json:"item_id"`
	ContentIndex int    `json:"content_index"`
	Transcript   string `json:"transcript"`
}

// ParseSidebandEvent parses one OpenAI Realtime SIP sideband server event. Known
// events populate typed payload pointers; unknown events return Type only.
func ParseSidebandEvent(raw []byte) (SidebandEvent, error) {
	var env sidebandTypeEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return SidebandEvent{}, fmt.Errorf("openai sideband event: invalid json: %w", err)
	}
	switch env.Type {
	case "response.done":
		var wire responseDoneWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			return SidebandEvent{}, fmt.Errorf("openai sideband event: invalid response.done: %w", err)
		}
		u := wire.Response.Usage
		return SidebandEvent{
			Type: env.Type,
			Usage: &ResponseUsage{
				ResponseID:        wire.Response.ID,
				Model:             wire.Response.Model,
				TotalTokens:       u.TotalTokens,
				InputTokens:       u.InputTokens,
				OutputTokens:      u.OutputTokens,
				InputTextTokens:   u.InputTokenDetails.TextTokens,
				InputAudioTokens:  u.InputTokenDetails.AudioTokens,
				CachedTextTokens:  u.InputTokenDetails.CachedTokensDetails.TextTokens,
				CachedAudioTokens: u.InputTokenDetails.CachedTokensDetails.AudioTokens,
				OutputTextTokens:  u.OutputTokenDetails.TextTokens,
				OutputAudioTokens: u.OutputTokenDetails.AudioTokens,
			},
		}, nil
	case "conversation.item.input_audio_transcription.completed":
		var wire transcriptDoneWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			return SidebandEvent{}, fmt.Errorf("openai sideband event: invalid transcription: %w", err)
		}
		return SidebandEvent{
			Type: env.Type,
			Transcript: &TranscriptDone{
				ItemID:       wire.ItemID,
				ContentIndex: wire.ContentIndex,
				Transcript:   wire.Transcript,
			},
		}, nil
	case "session.created", "session.updated", "error", "response.created":
		return SidebandEvent{Type: env.Type}, nil
	default:
		return SidebandEvent{Type: env.Type}, nil
	}
}

// guardrailSessionUpdateWire is the OpenAI session.update client event shape
// verified on 2026-07-05.
type guardrailSessionUpdateWire struct {
	Type    string               `json:"type"`
	Session guardrailSessionWire `json:"session"`
}

// guardrailSessionWire is the session object nested in session.update, verified
// on 2026-07-05.
type guardrailSessionWire struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

// GuardrailSessionUpdate builds the PEP mutate-half session.update payload used
// to inject governed instructions onto a live sideband session.
func GuardrailSessionUpdate(instructions string) ([]byte, error) {
	if strings.TrimSpace(instructions) == "" {
		return nil, fmt.Errorf("openai sideband: empty guardrail instructions")
	}
	body := guardrailSessionUpdateWire{
		Type: "session.update",
		Session: guardrailSessionWire{
			Type:         "realtime",
			Instructions: instructions,
		},
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, fmt.Errorf("openai sideband: marshal guardrail update: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// RealtimeModalityPricing is OpenAI Realtime list pricing as micro-USD per 1M
// tokens, verified on 2026-07-05. Example: $4.00 per 1M tokens is stored as
// 4_000_000 micro-USD per 1M tokens. Per-minute realtime models intentionally
// return ok=false from RealtimePricing because token usage cannot derive cost.
type RealtimeModalityPricing struct {
	TextInPerM   int64
	TextOutPerM  int64
	AudioInPerM  int64
	AudioOutPerM int64
	CachedInPerM int64
}

// RealtimePricing returns exact-id OpenAI Realtime modality pricing. It does not
// prefix-match model IDs.
func RealtimePricing(model string) (RealtimeModalityPricing, bool) {
	switch model {
	case openaiDefaultModel:
		return RealtimeModalityPricing{
			TextInPerM:   4_000_000,
			TextOutPerM:  24_000_000,
			AudioInPerM:  32_000_000,
			AudioOutPerM: 64_000_000,
			CachedInPerM: 400_000,
		}, true
	case "gpt-realtime-translate", "gpt-realtime-whisper":
		return RealtimeModalityPricing{}, false
	default:
		return RealtimeModalityPricing{}, false
	}
}
