// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPInspectionCatalogLimitsAndReadOnly(t *testing.T) {
	for _, mode := range []string{"bounded", "repeat", "too_many"} {
		t.Run(mode, func(t *testing.T) {
			lists := 0
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer fixture" {
					t.Error("header alias changed")
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.Method == "notifications/initialized" {
					if len(req.ID) != 0 {
						t.Error("notification id")
					}
					w.WriteHeader(202)
					return
				}
				result := `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
				if req.Method == "tools/list" {
					lists++
					tools := []Tool{{Name: "search"}}
					cursor := ""
					if mode == "repeat" {
						cursor = "unchanged"
					}
					if mode == "too_many" {
						tools = make([]Tool, 129)
						for i := range tools {
							tools[i].Name = fmt.Sprintf("tool%d", i)
						}
					}
					raw, _ := json.Marshal(map[string]any{"tools": tools, "nextCursor": cursor})
					result = string(raw)
				} else if req.Method != "initialize" {
					t.Errorf("inspection executed %s", req.Method)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
			}))
			defer server.Close()
			headers := map[string]string{"Authorization": "Bearer fixture"}
			client, err := NewHTTPInspectionClient(server.URL, headers, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			headers["Authorization"] = "changed"
			if _, err := client.Initialize(t.Context()); err != nil {
				t.Fatal(err)
			}
			tools, err := client.ListTools(t.Context())
			if mode == "bounded" {
				if err != nil || len(tools) != 1 || lists != 1 || calls != 3 {
					t.Fatalf("bounded inspection: %v %d %d", err, lists, calls)
				}
			} else if err == nil || lists > 8 {
				t.Fatal("unbounded or partial catalog accepted")
			}
		})
	}
	if _, err := NewHTTPInspectionClient("https://fixture.test", nil, nil); err == nil {
		t.Fatal("implicit transport accepted")
	}
}

func TestHTTPInspectionRefusesTruncatedInitializedNotification(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error("fixture request decoding")
			return
		}
		if req.Method == "notifications/initialized" {
			w.Header().Set("Content-Length", "12")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "{}") // an incomplete acknowledgment body
			return
		}
		if req.Method != "initialize" {
			t.Error("inspection continued after a failed notification")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`, req.ID)
	}))
	defer server.Close()
	client, err := NewHTTPInspectionClient(server.URL, nil, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Initialize(t.Context()); !errors.Is(err, io.ErrUnexpectedEOF) || calls != 2 {
		t.Fatalf("notification read failure ignored: error=%t calls=%d", err != nil, calls)
	}
}
