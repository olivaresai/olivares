// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"github.com/olivaresai/olivares/modules/sessions"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentToolOllamaProbeUsesLocalMetadataWithoutInferenceOrCredentials(t *testing.T) {
	tags, show := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tags":
			tags++
			if r.Method != http.MethodGet {
				t.Error("model list method")
			}
			w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":100}]}`))
		case "/api/show":
			show++
			if r.Method != http.MethodPost {
				t.Error("show method")
			}
			w.Write([]byte(`{"capabilities":["completion","tools"]}`))
		default:
			t.Errorf("unexpected inference/endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	probe := newProviderProbe()
	got, err := probe.Probe(context.Background(), sessions.ProviderProbeRequest{Kind: sessions.ProviderKindOllama, BaseURL: server.URL})
	if err != nil || len(got.Models) != 1 || got.Models[0] != "qwen3:8b" || tags != 1 || show != 1 {
		t.Fatalf("probe: %+v %v (%d/%d)", got, err, tags, show)
	}
}

func TestAgentToolOllamaProbeBoundsMetadataBeforeModelFanout(t *testing.T) {
	for _, body := range []string{
		`{"models":[{"name":"` + strings.Repeat("x", (1<<20)+1) + `"}]}`,
		`{"models":[` + strings.TrimSuffix(strings.Repeat(`{"name":"qwen3:8b"},`, 513), ",") + `]}`,
	} {
		t.Run(strconv.Itoa(len(body)), func(t *testing.T) {
			var shows atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/show" {
					shows.Add(1)
					w.Write([]byte(`{}`))
					return
				}
				w.Write([]byte(body))
			}))
			defer server.Close()
			_, err := newProviderProbe().Probe(context.Background(), sessions.ProviderProbeRequest{Kind: sessions.ProviderKindOllama, BaseURL: server.URL})
			if err == nil || shows.Load() != 0 {
				t.Fatalf("oversized metadata accepted or fanned out: %v %d", err, shows.Load())
			}
		})
	}
}
