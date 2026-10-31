// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// modelServerStub is an engine that answers the model-server routes: the start it was asked
// for, then a status that is "starting" for the first poll and ends as final.
func modelServerStub(t *testing.T, kind, final string) (*httptest.Server, *atomic.Int32, *map[string]any) {
	t.Helper()
	var polls atomic.Int32
	var started map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST " + agentToolsPath + "/model-servers/" + kind + "/start":
			if err := json.NewDecoder(r.Body).Decode(&started); err != nil {
				t.Errorf("start body: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": kind, "state": "starting", "models": []string{}})
		case "GET " + agentToolsPath + "/model-servers":
			state := "starting"
			if polls.Add(1) > 1 {
				state = final
			}
			row := map[string]any{"kind": kind, "name": "llama.cpp", "installed": true, "state": state, "models": []string{"gemma-3-1b"}}
			switch state {
			case "running":
				row["endpoint"] = "http://127.0.0.1:8080/v1"
			case "failed":
				row["message"] = "llama.cpp stopped on its own. error: the model could not be loaded"
			}
			other := map[string]any{"kind": "vllm", "name": "vLLM", "installed": false, "state": "stopped", "models": []string{}}
			_ = json.NewEncoder(w).Encode(map[string]any{"servers": []any{other, row}})
		case "POST " + agentToolsPath + "/model-servers/" + kind + "/stop":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": kind, "name": "llama.cpp", "installed": true, "state": "stopped", "models": []string{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &polls, &started
}

func TestToolStartRunsALocalModelServerAndNamesTheNextStep(t *testing.T) {
	server, polls, started := modelServerStub(t, "llama-cpp", "running")
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "start", "llama-cpp", "--model", "/models/gemma-3-1b.gguf"}, sessionCreds(server.URL)...)...)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if (*started)["model"] != "/models/gemma-3-1b.gguf" || (*started)["tenant_id"] == "" || (*started)["tenant_id"] == nil {
		t.Fatalf("the engine was asked %v, want the model as typed and the organization", *started)
	}
	for _, want := range []string{"llama.cpp is running at http://127.0.0.1:8080/v1.", "gemma-3-1b", "olivares session start <folder> --tool opencode"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
	if polls.Load() != 2 {
		t.Fatalf("polled %d times, want it to wait until the server answers", polls.Load())
	}
}

func TestToolStartSaysWhyAModelServerDidNotStart(t *testing.T) {
	server, _, _ := modelServerStub(t, "llama-cpp", "failed")
	_, _, err := execSessionCLI(t, nil, append([]string{"tool", "start", "llama-cpp", "--model", "/models/gemma-3-1b.gguf"}, sessionCreds(server.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "llama.cpp did not start") || !strings.Contains(err.Error(), "the model could not be loaded") {
		t.Fatalf("err = %v (code %v), want the server's own last line", err, exitcode.From(err))
	}
}

func TestToolStartRefusesWhatTheCommandCannotDo(t *testing.T) {
	for name, tc := range map[string]struct{ args []string }{
		"no model":                  {[]string{"tool", "start", "llama-cpp"}},
		"a model for Ollama":        {[]string{"tool", "start", "ollama", "--model", "qwen2.5:0.5b"}},
		"a tool that is no service": {[]string{"tool", "start", "claude"}},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("a refused command reached the engine: %s %s", r.Method, r.URL.Path)
			}))
			defer server.Close()
			_, _, err := execSessionCLI(t, nil, append(tc.args, sessionCreds(server.URL)...)...)
			if err == nil || exitcode.From(err) != exitcode.Usage {
				t.Fatalf("err = %v (code %v), want a usage error before any request", err, exitcode.From(err))
			}
		})
	}
}

// A server that answers but could not be registered cannot be used from a session: the command
// fails, with the engine's reason, instead of naming a next step that would not work.
func TestToolStartFailsWhenTheServerCouldNotBeRegistered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"kind":"llama-cpp","state":"starting","models":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"servers":[{"kind":"llama-cpp","name":"llama.cpp","installed":true,"state":"running","endpoint":"http://127.0.0.1:46615/v1","message":"Running, but its endpoint could not be added to Providers: a provider named llama.cpp on this server already exists at another address","models":[]}]}`))
	}))
	defer server.Close()
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "start", "llama-cpp", "--model", "/models/a.gguf"}, sessionCreds(server.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "already exists at another address") || strings.Contains(out, "session start") {
		t.Fatalf("stdout=%q err=%v code=%v, want a failure with the cause and no next step", out, err, exitcode.From(err))
	}
}

func TestToolStopFailsWhenTheServerDidNotStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"llama-cpp","state":"running","models":[]}`))
	}))
	defer server.Close()
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "stop", "llama-cpp"}, sessionCreds(server.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "did not stop") || strings.Contains(out, "stopped.") {
		t.Fatalf("stdout=%q err=%v code=%v, want a failure, not 'stopped.'", out, err, exitcode.From(err))
	}
}

func TestToolStartPassesTheEnginesRefusalOn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"tool_not_installed","message":"Install llama.cpp (llama-server) on this node first: it must be on the engine's PATH."}}`))
	}))
	defer server.Close()
	_, _, err := execSessionCLI(t, nil, append([]string{"tool", "start", "vllm", "--model", "Qwen/Qwen3-0.6B"}, sessionCreds(server.URL)...)...)
	if err == nil || exitcode.From(err) != exitcode.Conflict || !strings.Contains(err.Error(), "llama-server") {
		t.Fatalf("err = %v (code %v), want the engine's sentence and the conflict exit code", err, exitcode.From(err))
	}
}

// The engine can answer "stopped" while warning that a container it started may still
// serve (no runtime can be driven to remove it, or another runtime cannot prove it is
// gone). The person is the one who has to remove it, so `tool stop` says it; JSON-only
// would turn a warning into a silent success.
func TestToolStopShowsTheEnginesWarning(t *testing.T) {
	const warning = "Ollama is stopped, but the engine could not confirm that the container it started is gone, so it may still be serving on http://127.0.0.1:11434; remove it by hand if that address answers."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "stopped", "models": []string{}, "message": warning})
	}))
	defer server.Close()
	out, _, err := execSessionCLI(t, nil, append([]string{"tool", "stop", "ollama"}, sessionCreds(server.URL)...)...)
	if err != nil {
		t.Fatalf("tool stop ollama: %v", err)
	}
	for _, want := range []string{"Ollama stopped.", warning} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool stop ollama lacks %q:\n%s", want, out)
		}
	}
}

func TestToolStopEndsAModelServerAndOllama(t *testing.T) {
	for _, tc := range []struct{ tool, route, said string }{
		{"llama-cpp", "/model-servers/llama-cpp/stop", "llama.cpp stopped."},
		{"ollama", "/ollama/stop", "Ollama stopped."},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			var asked atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != agentToolsPath+tc.route {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				asked.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"state": "stopped", "models": []string{}})
			}))
			defer server.Close()
			out, _, err := execSessionCLI(t, nil, append([]string{"tool", "stop", tc.tool}, sessionCreds(server.URL)...)...)
			if err != nil || !strings.Contains(out, tc.said) || asked.Load() != 1 {
				t.Fatalf("stdout=%q err=%v asked=%d, want %q after one request", out, err, asked.Load(), tc.said)
			}
		})
	}
}
