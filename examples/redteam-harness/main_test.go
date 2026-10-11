// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHarnessDeliversPayloadThroughExplicitProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.String() != "http://target.invalid/probe" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		var probe map[string]string
		if err := json.NewDecoder(r.Body).Decode(&probe); err != nil {
			t.Error(err)
		}
		if probe["payload"] != "labelled probe" || probe["surface"] != "input" {
			t.Errorf("probe = %v", probe)
		}
		_, _ = io.WriteString(w, "I cannot help with that.")
	}))
	defer proxy.Close()
	var out bytes.Buffer
	job := `{"target":"http://target.invalid/probe","proxy_url":"` + proxy.URL + `","probe":{"id":"test","surface":"input","payload":"labelled probe"}}`
	if err := execute(strings.NewReader(job), &out); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["reached"] != true || result["response"] != "I cannot help with that." {
		t.Fatalf("result = %v", result)
	}
}

func TestHarnessNeverDialsWithoutProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("direct target connection") }))
	defer target.Close()
	var out bytes.Buffer
	err := execute(strings.NewReader(`{"target":"`+target.URL+`","probe":{"id":"test"}}`), &out)
	if err == nil {
		t.Fatal("unconfigured proxy accepted")
	}
}

func TestHarnessUnixProxyAndRefusal(t *testing.T) {
	ln, err := net.Listen("unix", t.TempDir()+"/proxy.sock")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan struct{}, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { received <- struct{}{}; w.WriteHeader(403) })}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	job := `{"target":"http://target.invalid/","proxy_url":"http://127.0.0.1:9","proxy_socket":"` + ln.Addr().String() + `","probe":{"id":"test"}}`
	var out bytes.Buffer
	if err := execute(strings.NewReader(job), &out); err == nil || err.Error() != "target or proxy returned HTTP 403" {
		t.Fatalf("Unix proxy refusal: %v", err)
	}
	select {
	case <-received:
	default:
		t.Fatal("Unix proxy never received the request")
	}
}

func TestHarnessPreservesLastMockAndMiss(t *testing.T) {
	var out bytes.Buffer
	if err := execute(strings.NewReader(`{"steps":[{"key":"hit","input":"db"},{"key":"miss","input":"absent"}],"mocks":[{"resource":"db","response":"old"},{"resource":"db","response":"new"}]}`), &out); err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 2 || got.Steps[0].Output != "new" || !got.Steps[0].MockHit || got.Steps[1].Output != "[[mock-miss:absent]]" || got.Steps[1].MockHit {
		t.Fatalf("mock resolution changed: %+v", got)
	}
}
