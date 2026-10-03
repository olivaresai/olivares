// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type blockedHookReader struct {
	release <-chan struct{}
	inner   io.Reader
}

func (r blockedHookReader) Read(p []byte) (int, error) { <-r.release; return r.inner.Read(p) }

func TestHookClientWholeDeadlineIncludesStalledInput(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`))
	}))
	defer server.Close()
	release := make(chan struct{})
	var out bytes.Buffer
	returned := make(chan error, 1)
	go func() {
		returned <- RunHookClient(context.Background(), blockedHookReader{release: release, inner: strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/stalled/filesystem"}}`)}, &out, HookClientConfig{Endpoint: server.URL, Timeout: 20 * time.Millisecond})
	}()
	select {
	case err := <-returned:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-returned
		t.Fatal("stdin held the helper beyond its own deadline")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"permissionDecision":"deny"`)) || !bytes.Contains(out.Bytes(), []byte("timed out")) {
		t.Fatalf("deadline did not emit an explicit deny: %s", out.Bytes())
	}
	// The resumed reader must never reach HTTP after its original request expired.
	time.Sleep(30 * time.Millisecond)
	if requests.Load() != 0 {
		t.Fatal("late preprocessing dispatched a request after denial")
	}
}

func TestHookClientWholeDeadlineIncludesBlockingFilesystemCalls(t *testing.T) {
	for _, phase := range []string{"lstat", "symlinks"} {
		for _, event := range []string{"PreToolUse", "PermissionRequest"} {
			t.Run(phase+"/"+event, func(t *testing.T) {
				release, entered, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var requests, evals atomic.Int32
				cfg := HookClientConfig{Endpoint: "http://127.0.0.1/", Timeout: 20 * time.Millisecond, Client: &http.Client{Transport: deadlineRoundTripper(func(r *http.Request) (*http.Response, error) { requests.Add(1); return nil, context.Canceled })}}
				cfg.pathCalls = &hookPathCalls{
					lstat: func(string) (os.FileInfo, error) {
						if phase == "lstat" {
							close(entered)
							<-release
							close(exited)
						}
						return nil, nil
					},
					evalSymlinks: func(path string) (string, error) {
						evals.Add(1)
						if phase == "symlinks" {
							close(entered)
							<-release
							close(exited)
						}
						return path, nil
					},
				}
				body, _ := json.Marshal(map[string]any{"hook_event_name": event, "tool_name": "Write", "tool_input": map[string]any{"file_path": "/blocking/filesystem"}})
				var out bytes.Buffer
				done := make(chan error, 1)
				go func() { done <- RunHookClient(t.Context(), bytes.NewReader(body), &out, cfg) }()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("fake filesystem call was not reached")
				}
				select {
				case err := <-done:
					close(release)
					<-exited
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(500 * time.Millisecond):
					close(release)
					<-exited
					<-done
					t.Fatal("blocked filesystem held the helper beyond its own deadline")
				}
				if !completeHookDecision(event, out.Bytes()) || !bytes.Contains(out.Bytes(), []byte(`"deny"`)) || bytes.Contains(out.Bytes(), []byte(`"allow"`)) {
					t.Fatalf("wrong deadline schema: %s", out.Bytes())
				}
				if requests.Load() != 0 {
					t.Fatal("late filesystem recovery dispatched HTTP")
				}
				if phase == "lstat" && evals.Load() != 0 {
					t.Fatal("expired filesystem work started a second syscall")
				}
			})
		}
	}
}

type deadlineRoundTripper func(*http.Request) (*http.Response, error)

func (f deadlineRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type deadlineReplyBody struct {
	io.Reader
	closed chan struct{}
}

func (b deadlineReplyBody) Close() error { close(b.closed); return nil }

func TestHookClientWholeDeadlineRefusesLateAllowFromUncooperativeTransport(t *testing.T) {
	release, entered, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: deadlineRoundTripper(func(*http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: http.StatusOK, Body: deadlineReplyBody{Reader: strings.NewReader(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`), closed: closed}, Header: make(http.Header)}, nil
	})}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- RunHookClient(t.Context(), strings.NewReader(preToolUsePayload("Bash")), &out, HookClientConfig{Endpoint: "http://127.0.0.1/", Timeout: 20 * time.Millisecond, Client: client})
	}()
	<-entered
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("transport held helper past its deadline")
	}
	first := bytes.Clone(out.Bytes())
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late response body was not closed")
	}
	if !bytes.Contains(first, []byte(`"permissionDecision":"deny"`)) || !bytes.Equal(first, out.Bytes()) {
		t.Fatalf("late transport changed the denial: %s", out.Bytes())
	}
}

func TestHookClientDeadlineStartsAtInvocationAndRefusesBeforeReading(t *testing.T) {
	release := make(chan struct{})
	var out bytes.Buffer
	err := RunHookClient(t.Context(), blockedHookReader{release: release, inner: strings.NewReader(preToolUsePayload("Bash"))}, &out, HookClientConfig{Endpoint: "http://127.0.0.1/", StartedAt: time.Now().Add(-time.Second), Timeout: 20 * time.Millisecond})
	close(release)
	if err != nil || !bytes.Contains(out.Bytes(), []byte(`"permissionDecision":"deny"`)) {
		t.Fatalf("expired invocation did not refuse before input: %s err=%v", out.Bytes(), err)
	}
}

func TestHookClientDeadlineUsesPinnedEventBeforeInputCompletes(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PermissionRequest", "PostToolUse"} {
		for _, partial := range []bool{false, true} {
			t.Run(event+"/payload="+fmt.Sprint(partial), func(t *testing.T) {
				release := make(chan struct{})
				var prefix string
				if partial {
					prefix = `{"hook_event_name":"` + event + `","tool_name":"Bash","tool_input":{"command":"printf ok"}}`
				}
				input := io.MultiReader(strings.NewReader(prefix), blockedHookReader{release: release, inner: strings.NewReader("")})
				var out bytes.Buffer
				err := RunHookClient(t.Context(), input, &out, HookClientConfig{Timeout: 20 * time.Millisecond, ExpectedEvent: event})
				close(release)
				if err != nil {
					t.Fatal(err)
				}
				assertDeadlineDenial(t, event, out.Bytes())
			})
		}
	}
}

func TestHookClientRefusesMismatchedInvocationEventBeforeHTTP(t *testing.T) {
	for _, tc := range []struct{ expected, actual, refusal string }{
		{"PreToolUse", "Stop", "PreToolUse"},
		{"PermissionRequest", "PostToolUseFailure", "PermissionRequest"},
		{"PostToolUse", "Stop", "PostToolUse"},
		{"Stop", "PreToolUse", "PreToolUse"},
		{"PostToolUseFailure", "PermissionRequest", "PermissionRequest"},
		{"unknown event", "PreToolUse", "PreToolUse"},
		{"unknown event", "Stop", "PreToolUse"},
		{"pretooluse", "PermissionRequest", "PermissionRequest"},
	} {
		t.Run(tc.expected+"/"+tc.actual, func(t *testing.T) {
			var requests atomic.Int32
			client := &http.Client{Transport: deadlineRoundTripper(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, context.Canceled
			})}
			body := `{"hook_event_name":"` + tc.actual + `","tool_name":"Bash","tool_input":{"command":"printf ok"}}`
			var out bytes.Buffer
			err := RunHookClient(t.Context(), strings.NewReader(body), &out, HookClientConfig{Endpoint: "http://127.0.0.1/", ExpectedEvent: tc.expected, Client: client})
			// PermissionRequest's existing wire contract carries behavior only.
			if err != nil || requests.Load() != 0 || (tc.refusal != "PermissionRequest" && !bytes.Contains(out.Bytes(), []byte("configured event"))) {
				t.Fatalf("mismatched invocation reached HTTP or lost refusal: %s requests=%d err=%v", out.Bytes(), requests.Load(), err)
			}
			assertDeadlineDenial(t, tc.refusal, out.Bytes())
		})
	}
}

func assertDeadlineDenial(t *testing.T, event string, body []byte) {
	t.Helper()
	var reply struct {
		Decision string `json:"decision"`
		Output   struct {
			Event      string `json:"hookEventName"`
			Permission string `json:"permissionDecision"`
			Decision   struct {
				Behavior string `json:"behavior"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		t.Fatal(err)
	}
	switch event {
	case "PreToolUse":
		if reply.Output.Event != event || reply.Output.Permission != "deny" {
			t.Fatalf("PreToolUse ignored denial schema: %s", body)
		}
	case "PermissionRequest":
		if reply.Output.Event != event || reply.Output.Decision.Behavior != "deny" {
			t.Fatalf("PermissionRequest ignored denial schema: %s", body)
		}
	case "PostToolUse":
		if reply.Decision != "block" {
			t.Fatalf("PostToolUse ignored denial schema: %s", body)
		}
	default:
		t.Fatalf("unexpected test event %s", event)
	}
}
