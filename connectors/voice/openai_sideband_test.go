// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSidebandEventResponseUsage(t *testing.T) {
	raw := []byte(`{
		"type": "response.done",
		"response": {
			"id": "resp_123",
			"model": "gpt-realtime-2",
			"usage": {
				"total_tokens": 100,
				"input_tokens": 60,
				"output_tokens": 40,
				"input_token_details": {
					"text_tokens": 10,
					"audio_tokens": 20,
					"cached_tokens_details": {
						"text_tokens": 3,
						"audio_tokens": 4
					}
				},
				"output_token_details": {
					"text_tokens": 30,
					"audio_tokens": 10
				}
			}
		}
	}`)

	ev, err := ParseSidebandEvent(raw)
	require.NoError(t, err)
	require.NotNil(t, ev.Usage)
	assert.Equal(t, "response.done", ev.Type)
	assert.Equal(t, "resp_123", ev.Usage.ResponseID)
	assert.Equal(t, "gpt-realtime-2", ev.Usage.Model)
	assert.Equal(t, int64(100), ev.Usage.TotalTokens)
	assert.Equal(t, int64(60), ev.Usage.InputTokens)
	assert.Equal(t, int64(40), ev.Usage.OutputTokens)
	assert.Equal(t, int64(10), ev.Usage.InputTextTokens)
	assert.Equal(t, int64(20), ev.Usage.InputAudioTokens)
	assert.Equal(t, int64(3), ev.Usage.CachedTextTokens)
	assert.Equal(t, int64(4), ev.Usage.CachedAudioTokens)
	assert.Equal(t, int64(30), ev.Usage.OutputTextTokens)
	assert.Equal(t, int64(10), ev.Usage.OutputAudioTokens)
}

func TestParseSidebandEventMissingUsageDetailsZero(t *testing.T) {
	raw := []byte(`{"type":"response.done","response":{"id":"resp_456","usage":{"total_tokens":7}}}`)
	ev, err := ParseSidebandEvent(raw)
	require.NoError(t, err)
	require.NotNil(t, ev.Usage)
	assert.Equal(t, "resp_456", ev.Usage.ResponseID)
	assert.Empty(t, ev.Usage.Model)
	assert.Equal(t, int64(7), ev.Usage.TotalTokens)
	assert.Zero(t, ev.Usage.InputTextTokens)
	assert.Zero(t, ev.Usage.CachedAudioTokens)
	assert.Zero(t, ev.Usage.OutputAudioTokens)
}

func TestParseSidebandEventTranscript(t *testing.T) {
	const transcript = "sensitive transcript text"
	raw := []byte(`{"type":"conversation.item.input_audio_transcription.completed","item_id":"item_1","content_index":2,"transcript":"` + transcript + `"}`)
	ev, err := ParseSidebandEvent(raw)
	require.NoError(t, err)
	require.NotNil(t, ev.Transcript)
	assert.Equal(t, "conversation.item.input_audio_transcription.completed", ev.Type)
	assert.Equal(t, "item_1", ev.Transcript.ItemID)
	assert.Equal(t, 2, ev.Transcript.ContentIndex)
	assert.Equal(t, transcript, ev.Transcript.Transcript)
	assert.NotContains(t, fmt.Sprint(ev), transcript)
	assert.NotContains(t, fmt.Sprint(*ev.Transcript), transcript)
}

func TestParseSidebandEventPassthroughAndInvalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"lifecycle", `{"type":"session.updated","session":{"id":"sess_1"}}`, "session.updated"},
		{"unknown", `{"type":"future.event","payload":{"x":1}}`, "future.event"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParseSidebandEvent([]byte(tc.raw))
			require.NoError(t, err)
			assert.Equal(t, tc.want, ev.Type)
			assert.Nil(t, ev.Usage)
			assert.Nil(t, ev.Transcript)
		})
	}

	_, err := ParseSidebandEvent([]byte(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"secret words",`))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret words")
}

func TestGuardrailSessionUpdate(t *testing.T) {
	got, err := GuardrailSessionUpdate("stay governed")
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"type":"session.update","session":{"type":"realtime","instructions":"stay governed"}}`), got)

	_, err = GuardrailSessionUpdate("")
	require.Error(t, err)
}

func TestRealtimePricing(t *testing.T) {
	p, ok := RealtimePricing("gpt-realtime-2")
	require.True(t, ok)
	assert.Equal(t, int64(4_000_000), p.TextInPerM)
	assert.Equal(t, int64(24_000_000), p.TextOutPerM)
	assert.Equal(t, int64(32_000_000), p.AudioInPerM)
	assert.Equal(t, int64(64_000_000), p.AudioOutPerM)
	assert.Equal(t, int64(400_000), p.CachedInPerM)

	for _, model := range []string{"gpt-realtime-translate", "gpt-realtime-whisper", "unknown"} {
		t.Run(model, func(t *testing.T) {
			_, ok := RealtimePricing(model)
			assert.False(t, ok)
		})
	}
}
