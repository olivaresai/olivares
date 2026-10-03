// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestManagedStdioFixture(t *testing.T) {
	if os.Getenv("MCP_FIXTURE") != "1" {
		return
	}
	input := bufio.NewScanner(os.Stdin)
	for input.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(input.Bytes(), &req) != nil || req.ID == 0 {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "read_file", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}}}}
		case "tools/call":
			if string(req.Params) == `{"name":"hang"}` {
				time.Sleep(time.Minute)
			}
			text := "fixture result"
			if string(req.Params) == `{"name":"echo_secret"}` {
				text = os.Getenv("MCP_SECRET")
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	os.Exit(0)
}

func TestManagedStdioRealProcessRoundTripAndLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, err := launchManagedStdio(ctx, sessions.NewProcRunner(), sessions.LaunchSpec{Program: os.Args[0], Args: []string{"-test.run=^TestManagedStdioFixture$"}, Dir: t.TempDir(), Isolation: sessions.IsolationNative, WaitDelay: time.Second, Env: []sessions.EnvVar{{Name: "MCP_FIXTURE", Value: "1"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	tools, err := s.ListTools(t.Context())
	if err != nil || len(tools) != 1 || tools[0].Name != "read_file" {
		t.Fatalf("tools=%v err=%v", tools, err)
	}
	result, err := s.Forward(t.Context(), mcpc.UpstreamRequest{Method: "tools/call", Params: []byte(`{"name":"read_file","arguments":{}}`)})
	if err != nil || result.State != mcpc.DispatchCompleted {
		t.Fatalf("roundtrip=%v err=%v", result, err)
	}
	cancel()
	select {
	case <-s.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("child was not reaped on session cancellation")
	}
	if _, err := s.Forward(t.Context(), mcpc.UpstreamRequest{Method: "tools/call", Params: []byte(`{}`)}); err == nil {
		t.Fatal("closed child accepted dispatch")
	}
}

func TestManagedStdioRefusesSecretEchoAndNeverRetriesTimeout(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret bool
	}{{"echo_secret", true}, {"hang", false}} {
		t.Run(tc.name, func(t *testing.T) {
			const fixtureSecret = "fixture-secret-for-response-guard"
			s, err := launchManagedStdio(t.Context(), sessions.NewProcRunner(), sessions.LaunchSpec{Program: os.Args[0], Args: []string{"-test.run=^TestManagedStdioFixture$"}, Dir: t.TempDir(), Isolation: sessions.IsolationNative, WaitDelay: time.Second, Env: []sessions.EnvVar{{Name: "MCP_FIXTURE", Value: "1"}, {Name: "MCP_SECRET", Value: fixtureSecret}}}, []string{fixtureSecret})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			if err := s.Initialize(t.Context()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
			defer cancel()
			params, _ := json.Marshal(map[string]string{"name": tc.name})
			result, err := s.Forward(ctx, mcpc.UpstreamRequest{Method: "tools/call", Params: params})
			if err == nil || result.State != mcpc.DispatchUnknown {
				t.Fatalf("state=%s err=%v", result.State, err)
			}
			if !tc.secret && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout did not expire the request: %v", err)
			}
			if tc.secret && !errors.Is(err, mcpc.ErrUpstreamCredentialDisclosure) {
				t.Fatalf("secret disclosure was not refused: %v", err)
			}
			result, err = s.Forward(t.Context(), mcpc.UpstreamRequest{Method: "tools/call", Params: params})
			if err == nil || result.State != mcpc.DispatchNotSent {
				t.Fatal("ambiguous failed process was reused")
			}
		})
	}
}
