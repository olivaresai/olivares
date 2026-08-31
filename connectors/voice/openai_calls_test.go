// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package voice

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goldenCallWebhook = `{
  "object": "event",
  "id": "evt_123",
  "type": "realtime.call.incoming",
  "created_at": 1750287018,
  "data": {
    "call_id": "some_unique_id",
    "sip_headers": [
      {"name": "From", "value": "sip:+142555512112@sip.example.com"},
      {"name": "To", "value": "sip:+18005551212@sip.example.com"},
      {"name": "Call-ID", "value": "03782086-4ce9-44bf-8b0d-4e303d2cc590"}
    ]
  }
}`

func TestParseCallWebhook(t *testing.T) {
	t.Run("golden", func(t *testing.T) {
		ev, err := ParseCallWebhook([]byte(goldenCallWebhook))
		require.NoError(t, err)
		assert.Equal(t, "evt_123", ev.EventID)
		assert.Equal(t, openaiIncomingCallType, ev.Type)
		assert.Equal(t, time.Unix(1750287018, 0).UTC(), ev.CreatedAt)
		assert.Equal(t, "some_unique_id", ev.CallID)
		assert.Equal(t, "sip:+142555512112@sip.example.com", ev.From())
		assert.Equal(t, "sip:+18005551212@sip.example.com", ev.To())
		assert.Equal(t, "03782086-4ce9-44bf-8b0d-4e303d2cc590", ev.SIPCallID())
	})

	t.Run("unknown fields tolerated", func(t *testing.T) {
		body := []byte(`{"object":"event","id":"evt_1","type":"realtime.call.incoming","created_at":1750287018,"ignored":true,"data":{"call_id":"call_1","sip_headers":[{"name":"from","value":"sip:+15551234567@example.test","ignored":true}],"ignored":true}}`)
		ev, err := ParseCallWebhook(body)
		require.NoError(t, err)
		assert.Equal(t, "call_1", ev.CallID)
		assert.Equal(t, "sip:+15551234567@example.test", ev.From())
	})

	cases := []struct {
		name string
		body string
	}{
		{"wrong type", `{"type":"response.done","data":{"call_id":"call_1"}}`},
		{"missing call id", `{"type":"realtime.call.incoming","data":{}}`},
		{"bad json", `{"type":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCallWebhook([]byte(tc.body))
			require.Error(t, err)
		})
	}
}

func TestVerifyCallWebhook(t *testing.T) {
	now := time.Unix(1750287018, 0).UTC()
	body := []byte(goldenCallWebhook)
	secret := testWebhookSecret("primary signing key")

	t.Run("valid signature", func(t *testing.T) {
		h := signedCallHeaders(body, secret, "evt_123", now)
		require.NoError(t, VerifyCallWebhook(h, body, secret, now))
	})

	t.Run("tampered body fails", func(t *testing.T) {
		h := signedCallHeaders(body, secret, "evt_123", now)
		err := VerifyCallWebhook(h, []byte(`{"tampered":true}`), secret, now)
		require.Error(t, err)
	})

	t.Run("stale timestamp fails", func(t *testing.T) {
		sent := now.Add(-CallWebhookReplayWindow - time.Second)
		h := signedCallHeaders(body, secret, "evt_123", sent)
		err := VerifyCallWebhook(h, body, secret, now)
		require.Error(t, err)
	})

	t.Run("future timestamp fails", func(t *testing.T) {
		sent := now.Add(CallWebhookReplayWindow + time.Second)
		h := signedCallHeaders(body, secret, "evt_123", sent)
		err := VerifyCallWebhook(h, body, secret, now)
		require.Error(t, err)
	})

	t.Run("missing headers fail", func(t *testing.T) {
		err := VerifyCallWebhook(http.Header{}, body, secret, now)
		require.Error(t, err)
	})

	t.Run("malformed secret fails", func(t *testing.T) {
		h := signedCallHeaders(body, secret, "evt_123", now)
		err := VerifyCallWebhook(h, body, "not-whsec", now)
		require.Error(t, err)
	})

	t.Run("wrong key fails", func(t *testing.T) {
		h := signedCallHeaders(body, testWebhookSecret("other signing key"), "evt_123", now)
		err := VerifyCallWebhook(h, body, secret, now)
		require.Error(t, err)
	})

	t.Run("second signature matches", func(t *testing.T) {
		h := signedCallHeaders(body, secret, "evt_123", now)
		h.Set("webhook-signature", "v1,bad "+h.Get("webhook-signature"))
		require.NoError(t, VerifyCallWebhook(h, body, secret, now))
	})
}

func TestCallClientRequests(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		callID     string
		run        func(CallClient) error
		wantURL    string
		wantBody   []byte
		wantNoBody bool
	}{
		{
			name:   "accept default model omits empty instructions",
			callID: "call/one",
			run: func(c CallClient) error {
				return c.Accept(ctx, "call/one", AcceptConfig{})
			},
			wantURL:  "https://api.example.test/v1/realtime/calls/call%2Fone/accept",
			wantBody: []byte(`{"type":"realtime","model":"gpt-realtime-2"}`),
		},
		{
			name:   "reject default no body",
			callID: "call/one",
			run: func(c CallClient) error {
				return c.Reject(ctx, "call/one", 0)
			},
			wantURL:    "https://api.example.test/v1/realtime/calls/call%2Fone/reject",
			wantNoBody: true,
		},
		{
			name:   "reject status",
			callID: "call/one",
			run: func(c CallClient) error {
				return c.Reject(ctx, "call/one", 486)
			},
			wantURL:  "https://api.example.test/v1/realtime/calls/call%2Fone/reject",
			wantBody: []byte(`{"status_code":486}`),
		},
		{
			name:   "refer",
			callID: "call/one",
			run: func(c CallClient) error {
				return c.Refer(ctx, "call/one", "sip:+18005550199@example.test")
			},
			wantURL:  "https://api.example.test/v1/realtime/calls/call%2Fone/refer",
			wantBody: []byte(`{"target_uri":"sip:+18005550199@example.test"}`),
		},
		{
			name:   "hangup",
			callID: "call/one",
			run: func(c CallClient) error {
				return c.Hangup(ctx, "call/one")
			},
			wantURL:    "https://api.example.test/v1/realtime/calls/call%2Fone/hangup",
			wantNoBody: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &stubTransport{status: http.StatusOK, respBody: `{}`}
			client := CallClient{APIKey: masterKey, BaseURL: "https://api.example.test", Transport: tr}
			require.NoError(t, tc.run(client))
			assert.Equal(t, http.MethodPost, tr.gotMethod)
			assert.Equal(t, tc.wantURL, tr.gotURL)
			assert.Equal(t, "Bearer "+masterKey, tr.gotHeaders.Get("Authorization"))
			if tc.wantNoBody {
				assert.Nil(t, tr.gotBody)
			} else {
				assert.Equal(t, tc.wantBody, tr.gotBody)
				assert.Equal(t, "application/json", tr.gotHeaders.Get("Content-Type"))
			}
		})
	}
}

func TestCallClientErrorsAreReduced(t *testing.T) {
	tr := &stubTransport{
		status:   http.StatusInternalServerError,
		respBody: `SIP SECRET BODY sip:+142555512112@sip.example.com`,
	}
	client := CallClient{APIKey: masterKey, BaseURL: "https://api.example.test", Transport: tr}
	err := client.Accept(context.Background(), "call_1", AcceptConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.NotContains(t, err.Error(), "SIP SECRET BODY")
	assert.NotContains(t, err.Error(), "sip:+142555512112")
	assert.NotContains(t, err.Error(), masterKey)
}

func TestCallClientEmptyCallIDDoesNotCallHTTP(t *testing.T) {
	tr := &stubTransport{status: http.StatusOK, respBody: `{}`}
	client := CallClient{APIKey: masterKey, BaseURL: "https://api.example.test", Transport: tr}
	err := client.Hangup(context.Background(), "")
	require.Error(t, err)
	assert.Empty(t, tr.gotMethod)
}

func TestRedactSIPAddress(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"sip uri", "sip:+142555512112@sip.example.com", "sip:***2112@sip.example.com"},
		{"short user digits", "sip:5555@sip.example.com", "sip:***5555@sip.example.com"},
		{"non uri", "not a uri", "***"},
		{"no digits", "sip:alice@sip.example.com", "***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RedactSIPAddress(tc.in))
		})
	}
}

func testWebhookSecret(key string) string {
	return openaiWebhookSecretPrefix + base64.StdEncoding.EncodeToString([]byte(key))
}

func signedCallHeaders(body []byte, secret, id string, at time.Time) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)
	key, err := deriveCallWebhookKey(secret)
	if err != nil {
		panic(err)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id))
	mac.Write([]byte("."))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	sig := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	h := http.Header{}
	h.Set("webhook-id", id)
	h.Set("webhook-timestamp", ts)
	h.Set("webhook-signature", sig)
	return h
}
