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

const selfRunRef = "01a10199-0000-7000-8000-0000000000a1"
const selfSessionID = "osn_01a10199-9a94-7d38-b856-ff57cca8d2b8"

func TestSessionSelfReadsPastTheFirstPage(t *testing.T) {
	t.Setenv("OLIVARES_WORK_SESSION_ID", selfSessionID)
	for _, verb := range []string{"show", "events", "peers"} {
		t.Run(verb, func(t *testing.T) {
			var pages atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != sessionRunsPath {
					if r.URL.Path != sessionRunsPath+"/"+selfRunRef+"/events" {
						t.Errorf("unexpected endpoint: %s", r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"items":[]}`))
					return
				}
				pages.Add(1)
				if r.URL.Query().Get("cursor") == "" {
					_, _ = w.Write([]byte(`{"items":[],"has_more":true,"cursor":"next/page"}`))
					return
				}
				if r.URL.Query().Get("pagination") != "cursor" || r.URL.Query().Get("cursor") != "next/page" {
					t.Errorf("query = %s, want cursor pagination", r.URL.RawQuery)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{{"run_ref": selfRunRef, "canonical_sid": selfSessionID, "name": "lead"}}})
			}))
			defer server.Close()
			out, _, err := execSessionCLI(t, nil, append([]string{"session", verb, "self", "-o", "json"}, sessionCreds(server.URL)...)...)
			if err != nil || out == "" || pages.Load() < 2 {
				t.Fatalf("out=%q err=%v pages=%d, want lookup beyond the first page", out, err, pages.Load())
			}
		})
	}
}

func TestSessionSelfRefusesAnIncompletePage(t *testing.T) {
	t.Setenv("OLIVARES_WORK_SESSION_ID", selfSessionID)
	for _, cursor := range []string{"", "repeated"} {
		t.Run(cursor, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if requests.Add(1) > 2 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}, "has_more": true, "cursor": cursor})
			}))
			defer server.Close()
			_, _, err := execSessionCLI(t, nil, append([]string{"session", "show", "self"}, sessionCreds(server.URL)...)...)
			if err == nil || exitcode.From(err) != exitcode.Server || !strings.Contains(err.Error(), "next session page") || requests.Load() > 2 {
				t.Fatalf("err=%v requests=%d, want explicit incomplete-page refusal", err, requests.Load())
			}
		})
	}
}

func TestSessionSelfResolvesTheChildSession(t *testing.T) {
	for _, verb := range []string{"show", "events", "peers"} {
		for _, alias := range []string{"self", "current"} {
			for _, ref := range []string{selfSessionID, selfRunRef} {
				t.Run(verb+"/"+alias+"/"+ref, func(t *testing.T) {
					t.Setenv("OLIVARES_WORK_SESSION_ID", ref)
					f := newFakeSessionEngine(t)
					f.runs = []map[string]any{
						{"run_ref": "decoy", "name": ref, "state": "stopped"},
						{"run_ref": selfRunRef, "canonical_sid": selfSessionID, "name": "lead", "state": "running", "peers": []any{}},
					}
					run := func(target string) string {
						t.Helper()
						out, stderr, err := execSessionCLI(t, nil, append([]string{"session", verb, target, "-o", "json"}, sessionCreds(f.URL)...)...)
						if err != nil {
							t.Fatalf("%s %s: %v %s", verb, target, err, stderr)
						}
						return out
					}
					want := run(selfRunRef)
					if got := run(alias); got != want {
						t.Fatalf("alias output = %s, explicit id output = %s", got, want)
					}
					f.mu.Lock()
					defer f.mu.Unlock()
					for _, hit := range f.hits {
						if strings.Contains(hit, "/decoy") || !strings.HasPrefix(hit, "GET ") {
							t.Fatalf("alias addressed another session or wrote: %s", hit)
						}
					}
				})
			}
		}
	}
}

func TestSessionSelfDoesNotFallBackToANamedSession(t *testing.T) {
	t.Setenv("OLIVARES_WORK_SESSION_ID", selfSessionID)
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "decoy", "name": selfSessionID}}
	for _, verb := range []string{"show", "events", "peers"} {
		_, _, err := execSessionCLI(t, nil, append([]string{"session", verb, "self"}, sessionCreds(f.URL)...)...)
		if err == nil || exitcode.From(err) != exitcode.NotFound {
			t.Fatalf("%s: err=%v, want not found", verb, err)
		}
	}
}

func TestSessionSelfKeepsTheCallersCredential(t *testing.T) {
	t.Setenv("OLIVARES_WORK_SESSION_ID", selfSessionID)
	t.Setenv("OLIVARES_WORK_TOKEN", "unused-child-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want the caller's token", got)
		}
		if got := r.Header.Get("X-Olivares-Tenant"); got != "tenant-a" {
			t.Errorf("tenant = %q, want tenant-a", got)
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"permission denied"}}`))
	}))
	defer server.Close()
	for _, verb := range []string{"show", "events", "peers"} {
		_, _, err := execSessionCLI(t, nil, append([]string{"session", verb, "self"}, sessionCreds(server.URL)...)...)
		if err == nil || exitcode.From(err) != exitcode.Auth {
			t.Fatalf("%s: err=%v, want auth exit 3", verb, err)
		}
	}
}

func TestSessionSelfSetsPeersOnItsOwnRun(t *testing.T) {
	t.Setenv("OLIVARES_WORK_SESSION_ID", selfSessionID)
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": selfRunRef, "name": "lead", "canonical_sid": selfSessionID}}
	peer := "osn_01a10199-9a94-7d38-b856-ff57cca8d2b9"
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "peers", "self", peer}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	body := f.postsTo(sessionRunsPath + "/" + selfRunRef + "/peers")
	if len(body) != 1 {
		t.Fatalf("PUT bodies = %v, want one write to the current run", body)
	}
	peers, _ := body[0]["peers"].([]any)
	if len(peers) != 1 || peers[0] != peer {
		t.Fatalf("PUT peers = %v, want the specified peer", peers)
	}
}

func TestSessionNamedPeersKeepAliasSpellings(t *testing.T) {
	for _, name := range []string{"self", "current"} {
		for _, env := range []string{"", selfSessionID} {
			t.Run(name+"/"+env, func(t *testing.T) {
				t.Setenv("OLIVARES_WORK_SESSION_ID", env)
				f := newFakeSessionEngine(t)
				f.runs = []map[string]any{
					{"run_ref": selfRunRef, "name": "lead"},
					{"run_ref": "peer-run", "name": name, "canonical_sid": selfSessionID},
				}
				_, _, err := execSessionCLI(t, nil, append([]string{"session", "peers", "lead", name}, sessionCreds(f.URL)...)...)
				if err != nil {
					t.Fatal(err)
				}
				body := f.postsTo(sessionRunsPath + "/" + selfRunRef + "/peers")
				if len(body) != 1 || len(body[0]["peers"].([]any)) != 1 || body[0]["peers"].([]any)[0] != selfSessionID {
					t.Fatalf("named peer write = %v, want %s's canonical id", body, name)
				}
			})
		}
	}
}

func TestSessionSelfRefusesWithoutRequests(t *testing.T) {
	for _, verb := range []string{"show", "events", "peers"} {
		for _, alias := range []string{"self", "current"} {
			for name, env := range map[string]string{"absent": "", "blank": " \t "} {
				t.Run(verb+"/"+alias+"/"+name, func(t *testing.T) {
					t.Setenv("OLIVARES_WORK_SESSION_ID", env)
					f := newFakeSessionEngine(t)
					_, _, err := execSessionCLI(t, nil, append([]string{"session", verb, alias}, sessionCreds(f.URL)...)...)
					if err == nil || exitcode.From(err) != exitcode.Usage {
						t.Fatalf("err = %v, want usage exit 2", err)
					}
					if !strings.Contains(err.Error(), "OLIVARES_WORK_SESSION_ID") || strings.Contains(err.Error(), "\n") {
						t.Fatalf("missing environment error must be one sentence: %v", err)
					}
					f.mu.Lock()
					defer f.mu.Unlock()
					if len(f.hits) != 0 {
						t.Fatalf("refused invocation reached the server: %v", f.hits)
					}
				})
			}
		}
	}
}

// Only peers takes more than one operand, so only peers can pair an explicit
// session with the word self or current.
func TestSessionPeersRefusesAMismatchedExplicitSession(t *testing.T) {
	for _, explicit := range []string{"01a10199-0000-7000-8000-0000000000b1", "osn_01a10199-9a94-7d38-b856-ff57cca8d2ff"} {
		for _, alias := range []string{"self", "current"} {
			t.Run(explicit+"/"+alias, func(t *testing.T) {
				t.Setenv("OLIVARES_WORK_SESSION_ID", selfSessionID)
				f := newFakeSessionEngine(t)
				_, _, err := execSessionCLI(t, nil, append([]string{"session", "peers", explicit, alias}, sessionCreds(f.URL)...)...)
				if err == nil || exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "different session id") {
					t.Fatalf("err = %v, want the mismatch usage error", err)
				}
				f.mu.Lock()
				defer f.mu.Unlock()
				if len(f.hits) != 0 {
					t.Fatalf("refused invocation reached the server: %v", f.hits)
				}
			})
		}
	}
}

func TestSessionPeersAllowsTheMatchingExplicitSession(t *testing.T) {
	t.Setenv("OLIVARES_WORK_SESSION_ID", selfRunRef)
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": selfRunRef, "name": "lead"},
		{"run_ref": "peer-run", "name": "self", "canonical_sid": selfSessionID},
	}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "peers", selfRunRef, "self"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("explicit id equal to the variable was refused: %v", err)
	}
}

func TestSessionSelfTrimsTheVariable(t *testing.T) {
	t.Setenv("OLIVARES_WORK_SESSION_ID", "  "+selfSessionID+" \n")
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": selfRunRef, "canonical_sid": selfSessionID, "name": "lead"}}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "show", "self", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil || !strings.Contains(out, selfRunRef) {
		t.Fatalf("out=%q err=%v, want the current session", out, err)
	}
}

// An explicit target ignores the variable: with it set to another session, every
// output and every request equals the run with it unset.
func TestSessionExplicitTargetIgnoresTheChildSession(t *testing.T) {
	for _, target := range []string{selfRunRef, "lead", "lea"} {
		for _, verb := range []string{"show", "events", "peers"} {
			for _, format := range []string{"text", "json"} {
				t.Run(verb+"/"+format+"/"+target, func(t *testing.T) {
					run := func(env string) (string, string, []string) {
						t.Setenv("OLIVARES_WORK_SESSION_ID", env)
						f := newFakeSessionEngine(t)
						f.runs = []map[string]any{{"run_ref": selfRunRef, "name": "lead", "state": "running", "peers": []any{}}}
						out, stderr, err := execSessionCLI(t, nil, append([]string{"session", verb, target, "-o", format}, sessionCreds(f.URL)...)...)
						if err != nil || out == "" {
							t.Fatalf("env=%q: out=%q stderr=%q err=%v", env, out, stderr, err)
						}
						f.mu.Lock()
						defer f.mu.Unlock()
						return out, stderr, append([]string(nil), f.hits...)
					}
					wantOut, wantErr, wantHits := run("")
					gotOut, gotErr, gotHits := run("osn_01a10199-9a94-7d38-b856-ff57cca8d2ff")
					if gotOut != wantOut || gotErr != wantErr || strings.Join(gotHits, "|") != strings.Join(wantHits, "|") {
						t.Fatalf("variable changed an explicit call:\nout %q vs %q\nstderr %q vs %q\nhits %v vs %v", gotOut, wantOut, gotErr, wantErr, gotHits, wantHits)
					}
				})
			}
		}
	}
}
