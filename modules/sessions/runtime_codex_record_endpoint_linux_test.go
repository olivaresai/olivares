// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// This opt-in native regression uses only task-owned homes, synthetic credentials
// and loopback servers. The tracer observes attempts (including refused connects)
// over the entire process tree, from spawn through stop and the resumed process.
func TestCodexRegisteredKeyNativeStartAndResumeReachOnlyTheBoundEndpoint(t *testing.T) {
	codexNativeBoundEndpointFixture(t, ProviderKindOpenAICompatible)
}

func TestCodexLocalRecordRejectsSavedCommandAuthBeforeNativeStartAndResume(t *testing.T) {
	codexNativeBoundEndpointFixture(t, ProviderKindOllama)
}

func codexNativeBoundEndpointFixture(t *testing.T, kind string) {
	t.Helper()
	if os.Getenv("OLIVARES_TEST_CODEX_ENDPOINT_PRIVACY") != "1" {
		t.Skip("set OLIVARES_TEST_CODEX_ENDPOINT_PRIVACY=1 with the native Codex and strace programs")
	}
	native, tracer := os.Getenv("OLIVARES_TEST_CODEX_PROGRAM"), os.Getenv("OLIVARES_TEST_STRACE_PROGRAM")
	for _, program := range []string{native, tracer} {
		if !filepath.IsAbs(program) {
			t.Fatal("native privacy proof requires explicit absolute executable paths")
		}
	}
	version, err := exec.Command(native, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native: %s", strings.TrimSpace(string(version)))

	for _, endpointUp := range []bool{true, false} {
		t.Run(map[bool]string{true: "up", false: "down"}[endpointUp], func(t *testing.T) {
			var boundRequests, otherRequests, foreignKeyExposures atomic.Int64
			bound := codexPrivacyEndpoint(t, &boundRequests, nil)
			other := codexPrivacyEndpoint(t, &otherRequests, &foreignKeyExposures)
			if !endpointUp {
				bound.Close()
			}
			configHome, userHome := t.TempDir(), t.TempDir()
			if captures := os.Getenv("OLIVARES_TEST_CODEX_TRAFFIC_DIR"); captures != "" {
				if !filepath.IsAbs(captures) {
					t.Fatal("traffic captures require an absolute task-owned directory")
				}
				t.Cleanup(func() {
					dir := filepath.Join(captures, map[bool]string{true: "up", false: "down"}[endpointUp])
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Error(err)
						return
					}
					paths, _ := filepath.Glob(filepath.Join(configHome, "*traffic.*"))
					for _, path := range paths {
						data, err := os.ReadFile(path)
						if err == nil {
							err = os.WriteFile(filepath.Join(dir, filepath.Base(path)), data, 0o600)
						}
						if err != nil {
							t.Error(err)
						}
					}
				})
			}
			wrapper := filepath.Join(configHome, "codex-traced")
			// Zero string bytes keeps payloads out of the capture; decoded socket
			// descriptors distinguish native Unix IPC from network sends.
			script := "#!/bin/sh\nexec " + codexPrivacyShellWord(tracer) +
				` -ff -s 0 -yy -o "$CODEX_HOME/traffic" -e trace=connect,sendto,sendmsg,sendmmsg ` +
				codexPrivacyShellWord(native) + ` "$@"` + "\n"
			if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			authCommand, authMarker := filepath.Join(configHome, "auth-command"), filepath.Join(configHome, "auth-ran")
			authConfig := "\n[model_providers.olivares_ollama.auth]\ncommand = " + strconv.Quote(authCommand) + "\n"
			if kind == ProviderKindOllama {
				script := "#!/bin/sh\n: > " + codexPrivacyShellWord(authMarker) + "\nprintf '%s' " + codexPrivacyShellWord(testProviderKey) + "\n"
				if err := os.WriteFile(authCommand, []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			nativeConfig := `model = "fixture-model"
model_provider = "native_canary"
check_for_update_on_startup = false
web_search = "disabled"
[analytics]
enabled = true
[features]
plugins = true
api_key_model_discovery = true
unbounded_connection_retries = false
[feedback]
enabled = true
[otel]
exporter = { otlp-http = { endpoint = ` + strconv.Quote(other.URL+"/telemetry") + `, protocol = "json" } }
trace_exporter = { otlp-http = { endpoint = ` + strconv.Quote(other.URL+"/traces") + `, protocol = "json" } }
[model_providers.native_canary]
name = "Native canary"
base_url = ` + strconv.Quote(other.URL+"/v1") + `
wire_api = "responses"
env_key = "OPENAI_API_KEY"
requires_openai_auth = false
request_max_retries = 0
stream_max_retries = 0
[model_providers.olivares_record]
name = "Old native record route"
base_url = ` + strconv.Quote(other.URL+"/v1") + `
model_catalog_url = ` + strconv.Quote(other.URL+"/catalog") + `
wire_api = "responses"
env_key = "OPENAI_API_KEY"
requires_openai_auth = false
request_max_retries = 0
			stream_max_retries = 0
`
			baseURL, key := bound.URL+"/v1", testProviderKey
			if kind == ProviderKindOllama {
				baseURL, key = bound.URL, ""
				nativeConfig = strings.ReplaceAll(nativeConfig, "olivares_record", "olivares_ollama")
				nativeConfig = strings.ReplaceAll(nativeConfig, "env_key = \"OPENAI_API_KEY\"\n", "")
			}
			cleanConfig := nativeConfig
			if kind == ProviderKindOllama {
				nativeConfig += authConfig
			}
			if err := os.WriteFile(filepath.Join(configHome, "config.toml"), []byte(nativeConfig), 0o600); err != nil {
				t.Fatal(err)
			}
			m, _, tenant, _ := newRuntimeHarness(t,
				WithRunner(NewProcRunner()), WithProviderDriver(NewCodexDriver()),
				WithDriverProgram(providerDriverCodex, wrapper), WithProductVersion("test"),
				WithProviderSecretVault(newFakeVault()), WithProviderProbe(&fakeProbe{}),
				WithStopWaitDelay(2*time.Second), WithDriverTimeouts(20*time.Second, 2*time.Second))
			m.UseExecutionEnvironmentRef(testEnvRef)
			record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
				Kind: kind, DisplayName: "Bound loopback provider", BaseURL: baseURL, APIKey: key,
			})
			profile := mustCreateProfile(t, m, tenant, CreateProfileInput{
				Driver: providerDriverCodex, ConfigHome: configHome, UserHome: userHome,
				DisplayName: "Native privacy proof", AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			params := CreateRunParams{
				ProviderProfileRef: profile.Ref, Transport: TransportStreamJSON, Isolation: IsolationNative,
				Actor: "user:u1", ActorKind: model.ActorUser, Model: "fixture-model",
				PermissionMode: permModeBypass, MayRunUnrestricted: true,
			}
			run, err := m.createRun(ctx, tenant, params)
			if kind == ProviderKindOllama {
				if err == nil {
					t.Error("saved command auth was accepted at native start")
				} else {
					assertCodexAuthRefusedBeforeSpawn(t, err, configHome, authMarker, otherRequests.Load())
					if err := os.WriteFile(filepath.Join(configHome, "config.toml"), []byte(cleanConfig), 0o600); err != nil {
						t.Fatal(err)
					}
					run, err = m.createRun(ctx, tenant, params)
				}
			}
			if err != nil {
				t.Fatalf("native create: %v", err)
			}
			conversation, runRef := run.ProviderConversationID, run.RunRef
			totalAttempts := 0
			for generation := 0; generation < 2; generation++ {
				requestsBefore := boundRequests.Load()
				if generation > 0 {
					if kind == ProviderKindOllama {
						if err := os.WriteFile(filepath.Join(configHome, "config.toml"), []byte(cleanConfig+authConfig), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					run, err = m.resumeRunAsCaller(ctx, tenant, runRef, "user:u1", model.ActorUser, "", true, true)
					if kind == ProviderKindOllama {
						if err == nil {
							t.Error("saved command auth was accepted at native resume")
						} else {
							assertCodexAuthRefusedBeforeSpawn(t, err, configHome, authMarker, otherRequests.Load())
							if err := os.WriteFile(filepath.Join(configHome, "config.toml"), []byte(cleanConfig), 0o600); err != nil {
								t.Fatal(err)
							}
							run, err = m.resumeRunAsCaller(ctx, tenant, runRef, "user:u1", model.ActorUser, "", true, true)
						}
					}
					if err != nil {
						t.Fatalf("native resume: %v", err)
					}
					if run.ProviderConversationID != conversation {
						t.Fatal("resume changed the provider conversation")
					}
				}
				t.Logf("generation %d before input: foreign HTTP=%d, foreign key exposures=%d", generation, otherRequests.Load(), foreignKeyExposures.Load())
				if err := m.sendTextInput(ctx, tenant, run.RunRef, "Reply only with fixture answer."); err != nil {
					t.Fatalf("native input: %v", err)
				}
				deadline := time.Now().Add(35 * time.Second)
				for {
					live, ok := m.rt.getLive(tenant, run.RunRef)
					if ok && live.session.ActiveTurn() == "" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("native turn did not settle within its bound")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if endpointUp {
					live, _ := m.rt.getLive(tenant, run.RunRef)
					completed := false
					for _, frame := range live.ring.readFrom(0).frames {
						var notification struct {
							Method string                         `json:"method"`
							Params codexTurnCompletedNotification `json:"params"`
						}
						if json.Unmarshal(frame.Data, &notification) == nil && notification.Method == codexNotifyTurnCompleted && notification.Params.Turn.Status == "completed" {
							completed = true
						}
					}
					if !completed {
						t.Fatalf("generation %d did not complete its native turn", generation)
					}
				}
				if _, err := m.stopRun(ctx, tenant, run.RunRef, "user:u1", model.ActorUser); err != nil {
					t.Fatalf("native stop: %v", err)
				}
				// Retain both generations rather than overwriting the first trace.
				traces, _ := filepath.Glob(filepath.Join(configHome, "traffic.*"))
				for _, path := range traces {
					if err := os.Rename(path, filepath.Join(configHome, fmt.Sprintf("generation-%d-%s", generation, filepath.Base(path)))); err != nil {
						t.Fatal(err)
					}
				}
				attempts := codexPrivacyAssertDestinations(t, configHome, bound.URL, generation)
				if attempts == 0 {
					t.Fatalf("generation %d did not attempt the bound destination", generation)
				}
				if endpointUp && boundRequests.Load() == requestsBefore {
					t.Fatalf("generation %d did not reach the bound endpoint", generation)
				}
				totalAttempts += attempts
			}
			if foreignKeyExposures.Load() != 0 {
				t.Errorf("foreign endpoint received the bound key %d times", foreignKeyExposures.Load())
			}
			if otherRequests.Load() != 0 {
				t.Fatalf("native profile endpoint received %d requests", otherRequests.Load())
			}
			if endpointUp && boundRequests.Load() < 2 {
				t.Fatalf("bound endpoint received %d requests; both generations must use it", boundRequests.Load())
			}
			if !endpointUp && boundRequests.Load() != 0 {
				t.Fatal("the stopped endpoint served a request")
			}
			if !t.Failed() {
				t.Logf("start+resume: bound attempts=%d, bound HTTP=%d, other HTTP=0, forbidden attempts=0, foreign key exposures=0", totalAttempts, boundRequests.Load())
			}
		})
	}
}

func assertCodexAuthRefusedBeforeSpawn(t *testing.T, err error, home, authMarker string, foreignRequests int64) {
	t.Helper()
	var refused *runErr
	if !errors.As(err, &refused) || refused.status != http.StatusConflict || !strings.Contains(refused.msg, "auth.command") {
		t.Fatalf("saved command auth did not receive its safe conflict: %v", err)
	}
	traces, _ := filepath.Glob(filepath.Join(home, "traffic.*"))
	if len(traces) != 0 || foreignRequests != 0 {
		t.Fatal("refused saved auth spawned a native process or reached the foreign endpoint")
	}
	if _, err := os.Stat(authMarker); !os.IsNotExist(err) {
		t.Fatal("refused saved auth executed its command")
	}
	t.Log("saved auth.command refused before spawn; foreign destinations=0, auth command executions=0")
}

func codexPrivacyShellWord(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func codexPrivacyEndpoint(t *testing.T, requests, exposedKeys *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if exposedKeys != nil && r.Header.Get("Authorization") == "Bearer "+testProviderKey {
			exposedKeys.Add(1)
		}
		if r.URL.Path != "/v1/responses" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		message := map[string]any{"id": "msg_fixture", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "fixture answer", "annotations": []any{}}}}
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp_fixture", "object": "response", "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.done", "output_index": 0, "item": message},
			{"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "object": "response", "status": "completed", "output": []any{message}, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		} {
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func codexPrivacyAssertDestinations(t *testing.T, home, endpoint string, generation int) int {
	t.Helper()
	u, _ := url.Parse(endpoint)
	port := u.Port()
	traces, _ := filepath.Glob(filepath.Join(home, fmt.Sprintf("generation-%d-traffic.*", generation)))
	if len(traces) == 0 {
		t.Fatal("native traffic capture is missing")
	}
	connect := regexp.MustCompile(`connect\(.*\{sa_family=AF_INET, sin_port=htons\((\d+)\), sin_addr=inet_addr\("([^"]+)"\)\}`)
	boundSocket := "->" + u.Hostname() + ":" + port + "]>"
	attempts := 0
	for _, path := range traces {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "connect(") && !strings.Contains(line, "sendto(") && !strings.Contains(line, "sendmsg(") && !strings.Contains(line, "sendmmsg(") {
				continue
			}
			implicitSend := !strings.Contains(line, "connect(") && !strings.Contains(line, "sa_family=")
			if strings.Contains(line, "sa_family=AF_UNIX") || (implicitSend && strings.Contains(line, "<UNIX-")) {
				continue
			}
			match := connect.FindStringSubmatch(line)
			if len(match) == 3 && match[1] == port && match[2] == u.Hostname() {
				attempts++
				continue
			}
			if implicitSend && strings.Contains(line, "<TCP:[") && strings.Contains(line, boundSocket) {
				continue
			}
			// An unclassified networking syscall is a failure, never an omitted
			// destination. Payload strings are restricted to zero bytes.
			t.Errorf("forbidden or unclassified native network attempt: %s", line)
		}
	}
	return attempts
}
