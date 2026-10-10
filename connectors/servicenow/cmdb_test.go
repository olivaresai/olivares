// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package servicenow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const cmdbID = "0123456789abcdef0123456789abcdef"

func cmdbRecord(id string) map[string]string {
	return map[string]string{"sys_id": id, "name": "fixture server", "sys_class_name": "cmdb_ci_server", "sys_updated_on": "2026-10-02 12:00:00", "private_payload": "CMDB-PAYLOAD-CANARY"}
}

func cmdbConfig(base string) sdk.Config {
	return sdk.Config{Settings: map[string]string{"instance_url": base, "auth_mode": "bearer", "token": "CMDB-CREDENTIAL-CANARY", "source_ref": "fixture-cmdb", "page_size": "1", "max_pages": "4", "sys_id": cmdbID}}
}

type cmdbSink struct{ items []model.Observation }

func (s *cmdbSink) Emit(_ context.Context, item model.Observation) error {
	s.items = append(s.items, item)
	return nil
}

func TestCMDBDefaultsWithoutSourceLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer CMDB-CREDENTIAL-CANARY" {
			t.Error("default configuration lost the credential")
		}
		if r.URL.Path == "/api/now/table/cmdb_ci/"+cmdbID {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": cmdbRecord(cmdbID)})
			return
		}
		w.Header().Set("X-Total-Count", "1")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{cmdbRecord(cmdbID)}})
	}))
	defer server.Close()
	cfg := cmdbConfig(server.URL)
	delete(cfg.Settings, "source_ref")
	var provenance string
	for _, instance := range []string{server.URL, server.URL + "/"} {
		cfg.Settings["instance_url"] = instance
		source := NewCMDBSource()
		if err := source.Open(t.Context(), cfg); err != nil {
			t.Fatalf("discovery requires an unnecessary source label: %v", err)
		}
		sink := &cmdbSink{}
		if err := source.Gather(t.Context(), sink); err != nil || len(sink.items) != 1 {
			t.Fatalf("default discovery unavailable: observations=%d error=%v", len(sink.items), err)
		}
		_ = source.Close(t.Context())
		edge := sink.items[0].(model.EdgeObservation)
		if !cmdbSourceRef.MatchString(edge.OriginRef) || provenance != "" && edge.OriginRef != provenance {
			t.Fatalf("default source provenance is unsafe or unstable: %q", edge.OriginRef)
		}
		provenance = edge.OriginRef
	}
	reader, err := NewCMDBReader(cfg)
	if err != nil {
		t.Fatalf("single-resource reader requires an unused label: %v", err)
	}
	value, err := reader.Read(t.Context())
	if err != nil || value.SysID != cmdbID {
		t.Fatalf("default resource read unavailable: id=%q error=%v", value.SysID, err)
	}
}

func TestCMDBSourceCollectsACLFilteredPagesWithProvenance(t *testing.T) {
	var offsets []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/now/table/cmdb_ci" || r.Header.Get("Authorization") != "Bearer CMDB-CREDENTIAL-CANARY" {
			t.Errorf("unexpected CMDB request method/path/auth")
		}
		q := r.URL.Query()
		if q.Get("sysparm_fields") != "sys_id,name,sys_class_name,sys_updated_on" || q.Get("sysparm_display_value") != "false" || q.Get("sysparm_query") != "ORDERBYsys_id" || q.Get("sysparm_limit") != "1" {
			t.Errorf("CMDB request did not use the bounded metadata contract")
		}
		offset, _ := strconv.Atoi(q.Get("sysparm_offset"))
		offsets = append(offsets, offset)
		w.Header().Set("X-Total-Count", "3")
		rows := []map[string]string{}
		if offset != 1 { // Table API applies ACL filtering after sysparm_limit.
			rows = append(rows, cmdbRecord(fmt.Sprintf("%032x", offset+1)))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": rows})
	}))
	defer server.Close()
	source := NewCMDBSource()
	if source.Descriptor().Name != "olivares.servicenow.cmdb" || source.Descriptor().Type != sdk.TypeSource {
		t.Fatal("CMDB source must have its own source descriptor")
	}
	if err := source.Open(t.Context(), cmdbConfig(server.URL)); err != nil {
		t.Fatal(err)
	}
	defer source.Close(t.Context())
	sink := &cmdbSink{}
	if err := source.Gather(t.Context(), sink); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(offsets) != "[0 1 2]" || len(sink.items) != 2 {
		t.Fatalf("ACL-filtered page was mistaken for completion: offsets=%v observations=%d", offsets, len(sink.items))
	}
	for _, item := range sink.items {
		edge, ok := item.(model.EdgeObservation)
		if !ok || edge.Mode != model.ModeUnknown || edge.Source != "servicenow_cmdb" || edge.OriginRef != "fixture-cmdb" || edge.ResourceKind != "servicenow.cmdb" || !strings.HasPrefix(edge.ResourceRef, server.URL+"/api/now/table/cmdb_ci/") || edge.ObservedAt.IsZero() {
			t.Fatalf("resource lost discovery provenance: %+v", item)
		}
		encoded, _ := json.Marshal(item)
		if strings.Contains(string(encoded), "CANARY") || strings.Contains(string(encoded), "fixture server") {
			t.Fatal("CMDB record or credential escaped onto the observation bus")
		}
	}
}

func TestCMDBSourceFailsBeforeEmittingIncompleteRoster(t *testing.T) {
	for _, failure := range []string{"outage", "denied", "redirect", "missing result", "bad id", "page budget", "missing count", "changing count", "duplicate id", "oversized", "truncated"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				second := r.URL.Query().Get("sysparm_offset") != "0"
				w.Header().Set("X-Total-Count", "2")
				if second || failure == "page budget" {
					switch failure {
					case "outage":
						w.WriteHeader(503)
						_, _ = w.Write([]byte("CMDB-RESPONSE-CANARY"))
						return
					case "denied":
						w.WriteHeader(403)
						return
					case "redirect":
						w.Header().Set("Location", "/secret?token=CMDB-RESPONSE-CANARY")
						w.WriteHeader(302)
						return
					case "missing result":
						_, _ = w.Write([]byte(`{}`))
						return
					case "bad id":
						_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{cmdbRecord("CMDB-RESPONSE-CANARY")}})
						return
					case "page budget":
						w.Header().Set("X-Total-Count", "5")
					case "missing count":
						w.Header().Del("X-Total-Count")
					case "changing count":
						w.Header().Set("X-Total-Count", "3")
					case "oversized":
						_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
						return
					case "truncated":
						_, _ = w.Write([]byte(`{"result":[`))
						return
					}
				}
				id := cmdbID
				if second && failure != "duplicate id" {
					id = "1123456789abcdef0123456789abcdef"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"result": []any{cmdbRecord(id)}})
			}))
			defer server.Close()
			source := NewCMDBSource()
			if err := source.Open(t.Context(), cmdbConfig(server.URL)); err != nil {
				t.Fatal(err)
			}
			sink := &cmdbSink{}
			err := source.Gather(t.Context(), sink)
			if err == nil || len(sink.items) != 0 || strings.Contains(err.Error(), "CANARY") {
				t.Fatalf("incomplete roster emitted observations or reflected remote text: count=%d err=%v", len(sink.items), err)
			}
		})
	}
}

func TestCMDBReaderFreshAllowlistedMetadataAndOutage(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || r.URL.Path != "/api/now/table/cmdb_ci/"+cmdbID {
			t.Error("reader escaped configured resource")
		}
		if requests > 1 {
			w.WriteHeader(503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": cmdbRecord(cmdbID)})
	}))
	defer server.Close()
	reader, err := NewCMDBReader(cmdbConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	value, err := reader.Read(t.Context())
	if err != nil || value.SysID != cmdbID || value.Name != "fixture server" || value.Class != "cmdb_ci_server" || value.UpdatedAt.IsZero() || value.CollectedAt.IsZero() {
		t.Fatalf("read metadata: %+v %v", value, err)
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "CANARY") {
		t.Fatal("private CMDB fields escaped the projection")
	}
	if _, err := reader.Read(t.Context()); err == nil {
		t.Fatal("outage returned a cached CMDB record")
	}
}

func TestCMDBMCPRejectsCallerSelectedResource(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(map[string]any{"result": cmdbRecord(cmdbID)})
	}))
	defer server.Close()
	reader, err := NewCMDBReader(cmdbConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_cmdb_resource","arguments":{"sys_id":"another-resource"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_cmdb_resource","arguments":{}}}
`)
	var out bytes.Buffer
	if err := reader.ServeCMDBMCP(t.Context(), input, &out); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	var response map[string]json.RawMessage
	for n := 0; n < 4; n++ {
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if n == 2 && !bytes.Contains(response["error"], []byte("-32602")) {
			t.Fatal("caller-selected resource accepted")
		}
		if n == 3 && !bytes.Contains(response["result"], []byte("structuredContent")) {
			t.Fatal("permitted metadata read unavailable")
		}
	}
	if requests != 1 {
		t.Fatal("refused call fetched remote metadata")
	}
}

func TestCMDBConfigurationDoesNotReflectCredentials(t *testing.T) {
	for key, value := range map[string]string{"instance_url": "https://user:CMDB-CANARY@example.invalid", "auth_mode": "CMDB-CANARY", "source_ref": "CMDB-CANARY/secret", "page_size": "CMDB-CANARY", "max_pages": "0", "sys_id": "CMDB-CANARY"} {
		cfg := cmdbConfig("https://example.invalid")
		cfg.Settings[key] = value
		_, err := NewCMDBReader(cfg)
		if err == nil || strings.Contains(err.Error(), "CANARY") {
			t.Fatalf("configuration accepted or reflected %s: %v", key, err)
		}
	}
	fields := NewCMDBSource().Descriptor().ConfigFields
	for _, key := range []string{"username", "password", "token"} {
		found := false
		for _, field := range fields {
			if field.Key == key {
				found = field.Secret
			}
		}
		if !found {
			t.Fatalf("credential %s is not declared secret", key)
		}
	}
}
