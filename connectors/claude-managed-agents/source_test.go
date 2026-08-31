// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor identity = %+v", d)
	}
	// The version crosses the plugin wire as the operator-facing connector identity:
	// a build WITHOUT the Dreams admission/PERMITTED surface must not report the same
	// version as one that enforces it.
	if d.Version != "0.3.0" {
		t.Errorf("version = %q, want 0.3.0 (thread-event read surface)", d.Version)
	}
	secret := map[string]bool{}
	for _, f := range d.ConfigFields {
		secret[f.Key] = f.Secret
	}
	for _, k := range []string{cfgAPIKey, cfgWebhookSecret} {
		if !secret[k] {
			t.Errorf("config field %q must be marked Secret", k)
		}
	}
}

func TestOpenBindsWebhookListener(t *testing.T) {
	s := openTestSource(t, map[string]string{
		cfgWebhookSecret: testSecret,
		cfgWebhookAddr:   "127.0.0.1:0",
	})
	defer func() { _ = s.Close(context.Background()) }()
	if s.lis == nil {
		t.Fatal("Open should bind the webhook listener when a secret is configured")
	}
}

// TestGatherWebhookEndToEnd drives a signed webhook through the full streaming Gather: the
// receiver verifies it and emits to the sink, and Gather returns cleanly on ctx cancel.
func TestGatherWebhookEndToEnd(t *testing.T) {
	s := openTestSource(t, map[string]string{
		cfgWebhookSecret: testSecret,
		cfgWebhookAddr:   "127.0.0.1:0",
		cfgWebhookPath:   "/cma/webhooks",
	})
	addr := s.lis.Addr().String()

	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	body := envelopeBody("event_e2e", "vault.created", "vlt_e2e", testTime)
	url := "http://" + addr + "/cma/webhooks"

	// The listener is already bound in Open, so the POST connects even before Serve runs;
	// retry briefly to absorb the goroutine scheduling of srv.Serve.
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set(webhookSigHeader, signWebhookBody(testSecret, body))
		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("webhook status = %d, want 200", resp.StatusCode)
	}
	if len(sink.all()) != 1 {
		t.Errorf("expected the webhook to emit 1 observation, got %d", len(sink.all()))
	}

	cancel()
	select {
	case gerr := <-done:
		if gerr != nil && !errors.Is(gerr, context.Canceled) {
			t.Errorf("Gather returned %v, want context.Canceled/nil", gerr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not return after ctx cancel")
	}
	_ = s.Close(context.Background())
}

// TestGatherPollOnlyReturnsOnCancel proves a poll-only Gather runs at least one pass and
// returns when ctx is canceled.
func TestGatherPollOnlyReturnsOnCancel(t *testing.T) {
	srv := cmaFixtureServer(t)
	defer srv.Close()
	s := openTestSource(t, map[string]string{
		cfgAPIKey:  "sk-ant-test",
		cfgBaseURL: srv.URL,
		cfgRefresh: "1h",
	})
	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Gather(ctx, sink) }()

	// Wait for the immediate first refresh pass to land at least one observation.
	deadline := time.Now().Add(3 * time.Second)
	for len(sink.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(sink.all()) == 0 {
		t.Fatal("poll-only Gather produced no observations on the first pass")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Gather did not return after ctx cancel")
	}
}
