// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// memoryDurableTaskStore is the connector test authority. Tests that exercise
// MCP Tasks wire it explicitly; nil-store tests remain available to prove the
// standalone fail-closed posture.
type memoryDurableTaskStore struct {
	mu          sync.Mutex
	next        int64
	views       map[string]DurableTaskView
	listErr     error
	getErr      error
	registerErr error
	updateErr   error
}

func newMemoryDurableTaskStore() *memoryDurableTaskStore {
	return &memoryDurableTaskStore{views: map[string]DurableTaskView{}}
}

func durableTestOwnerKey(owner TaskOwner) string {
	return strings.Join([]string{owner.Tenant, owner.Issuer, owner.Subject, owner.ActAs, owner.ClientID}, "\x00")
}

func durableTestKey(owner TaskOwner, taskID string) string {
	return durableTestOwnerKey(owner) + "\x00" + taskID
}

func (s *memoryDurableTaskStore) Register(_ context.Context, intent DurableTaskIntent) (DurableTaskRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registerErr != nil {
		return DurableTaskRef{}, s.registerErr
	}
	if s.views == nil {
		s.views = map[string]DurableTaskView{}
	}
	key := durableTestKey(intent.Owner, intent.TaskID)
	for currentKey, current := range s.views {
		if current.Intent.Owner.Tenant != intent.Owner.Tenant || current.Ref.TaskID != intent.TaskID {
			continue
		}
		if durableTestOwnerKey(current.Intent.Owner) == durableTestOwnerKey(intent.Owner) &&
			intent.OriginOperationID != "" && current.Intent.OriginOperationID == intent.OriginOperationID &&
			current.Intent.OriginEffectDigest == intent.OriginEffectDigest {
			return current.Ref, nil
		}
		if current.Observation.Terminal {
			// The connector only reaches a replacement registration after its
			// process cache released the confirmed terminal generation. Keep the
			// fake's List projection to the current generation, like the adapter.
			delete(s.views, currentKey)
			continue
		}
		return DurableTaskRef{}, ErrDurableTaskConflict
	}
	s.next++
	ref := DurableTaskRef{
		TaskID: intent.TaskID, Generation: s.next,
		BindingID:  "binding-" + strconv.FormatInt(s.next, 10),
		WorkItemID: "work-" + strconv.FormatInt(s.next, 10),
		SID:        "sid:mcp:test:" + strconv.FormatInt(s.next, 10),
	}
	s.views[key] = DurableTaskView{
		Ref: ref, Intent: intent,
		Observation: DurableTaskObservation{
			TaskID: intent.TaskID, Generation: ref.Generation,
			Kind: DurableTaskObservationRegister, Status: intent.InitialStatus,
			StatusReason: intent.InitialStatusReason, Verdict: DurableTaskVerdictClean,
			ObservedAt: intent.CreatedAt,
		},
	}
	return ref, nil
}

func (s *memoryDurableTaskStore) Get(_ context.Context, owner TaskOwner, taskID string, generation int64) (DurableTaskView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return DurableTaskView{}, s.getErr
	}
	view, ok := s.views[durableTestKey(owner, taskID)]
	if !ok || (generation != 0 && view.Ref.Generation != generation) {
		return DurableTaskView{}, ErrDurableTaskNotFound
	}
	return view, nil
}

func (s *memoryDurableTaskStore) UpdateObservation(_ context.Context, owner TaskOwner, update DurableTaskObservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.updateErr != nil {
		return s.updateErr
	}
	key := durableTestKey(owner, update.TaskID)
	view, ok := s.views[key]
	if !ok || view.Ref.Generation != update.Generation {
		return ErrDurableTaskNotFound
	}
	view.Observation = update
	s.views[key] = view
	return nil
}

func (s *memoryDurableTaskStore) List(_ context.Context, owner TaskOwner, cursor string, limit int) (DurableTaskPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return DurableTaskPage{}, s.listErr
	}
	if limit <= 0 {
		return DurableTaskPage{}, errors.New("limit must be positive")
	}
	start := 0
	if cursor != "" {
		var err error
		start, err = strconv.Atoi(cursor)
		if err != nil || start < 0 {
			return DurableTaskPage{}, errors.New("invalid cursor")
		}
	}
	keys := make([]string, 0, len(s.views))
	for key, view := range s.views {
		if view.Intent.Owner.Tenant != owner.Tenant {
			continue
		}
		if owner.Issuer != "" && internalTaskOwner(view.Intent.Owner) != internalTaskOwner(owner) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if start > len(keys) {
		return DurableTaskPage{}, errors.New("cursor outside inventory")
	}
	end := start + limit
	if end > len(keys) {
		end = len(keys)
	}
	page := DurableTaskPage{Tasks: make([]DurableTaskView, 0, end-start)}
	for _, key := range keys[start:end] {
		page.Tasks = append(page.Tasks, s.views[key])
	}
	if end < len(keys) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

// fixedTime is a deterministic timestamp for finding/edge assertions.
func fixedTime() time.Time { return time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC) }

// --- fakeSink ----------------------------------------------------------------

type fakeSink struct {
	mu  sync.Mutex
	obs []model.Observation
}

func (f *fakeSink) Emit(_ context.Context, o model.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return nil
}

func (f *fakeSink) edges() []model.EdgeObservation {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.EdgeObservation
	for _, o := range f.obs {
		if e, ok := o.(model.EdgeObservation); ok {
			out = append(out, e)
		}
	}
	return out
}

func (f *fakeSink) findings() []model.FindingReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.FindingReport
	for _, o := range f.obs {
		if r, ok := o.(model.FindingReport); ok {
			out = append(out, r)
		}
	}
	return out
}

// --- scripted mock transport (for client unit tests) -------------------------

// mockTransport answers roundTrip from a method→[]result script, advancing a
// per-method page index so pagination can be exercised. A nil entry for a method
// returns an error.
type mockTransport struct {
	pages   map[string][]json.RawMessage
	idx     map[string]int
	calls   []string
	version string
}

func newMockTransport() *mockTransport {
	return &mockTransport{pages: map[string][]json.RawMessage{}, idx: map[string]int{}}
}

func (m *mockTransport) reply(method string, results ...string) {
	for _, r := range results {
		m.pages[method] = append(m.pages[method], json.RawMessage(r))
	}
}

func (m *mockTransport) roundTrip(_ context.Context, req rpcRequest) (json.RawMessage, error) {
	m.calls = append(m.calls, req.Method)
	pages, ok := m.pages[req.Method]
	if !ok {
		return nil, fmt.Errorf("mock: no reply scripted for %q", req.Method)
	}
	i := m.idx[req.Method]
	if i >= len(pages) {
		i = len(pages) - 1
	}
	m.idx[req.Method] = i + 1
	return pages[i], nil
}

func (m *mockTransport) notify(context.Context, string, any) error { return nil }
func (m *mockTransport) setProtocolVersion(v string)               { m.version = v }
func (m *mockTransport) Close() error                              { return nil }

// --- stdio helper process ----------------------------------------------------

// helperEnv, when set, turns the re-executed test binary into a minimal MCP stdio
// server (see TestHelperMCPServer), so the real stdio transport can be tested
// against a real subprocess without shipping a separate binary.
const helperEnv = "GO_MCP_HELPER"

// TestHelperMCPServer is not a real test: when GO_MCP_HELPER is set it runs a
// canned MCP server over stdin/stdout and exits, so it never prints the test
// framework's trailing output onto the MCP stream.
func TestHelperMCPServer(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process; only runs when re-executed by a stdio test")
	}
	runHelperServer()
	os.Exit(0)
}

// runHelperServer reads newline-delimited JSON-RPC requests and writes canned
// responses, exercising pagination on tools/list.
func runHelperServer() {
	in := bufio.NewReader(os.Stdin)
	out := os.Stdout
	for {
		line, err := in.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return
		}
		var msg struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Cursor string `json:"cursor"`
			} `json:"params"`
		}
		if jerr := json.Unmarshal(trimLine(line), &msg); jerr != nil {
			if err != nil {
				return
			}
			continue
		}
		if msg.ID == nil { // a notification (e.g. notifications/initialized)
			if err != nil {
				return
			}
			continue
		}
		result := helperResult(msg.Method, msg.Params.Cursor)
		fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", *msg.ID, result)
		if err != nil {
			return
		}
	}
}

// helperResult returns the canned result JSON for a method (and tools/list page).
func helperResult(method, cursor string) string {
	switch method {
	case "initialize":
		return `{"protocolVersion":"2025-06-18","serverInfo":{"name":"helper","version":"1.0"},"capabilities":{"tools":{},"resources":{},"prompts":{}}}`
	case "tools/list":
		if cursor == "" {
			return `{"tools":[{"name":"read_file","annotations":{"readOnlyHint":true}}],"nextCursor":"p2"}`
		}
		return `{"tools":[{"name":"delete_file","annotations":{"destructiveHint":true}}]}`
	case "resources/list":
		return `{"resources":[{"uri":"file:///etc/hosts","name":"hosts"}]}`
	case "resources/templates/list":
		return `{"resourceTemplates":[{"uriTemplate":"file:///{path}","name":"file"}]}`
	case "prompts/list":
		return `{"prompts":[{"name":"review"}]}`
	default:
		return `{}`
	}
}

// Adversarial code points shared by the posture tests (explicit escapes, never
// literal invisible runes in source). The full fixture set lives with the
// primitives in connectors/internal/textscan.
const (
	zwsp      = "​" // zero-width space
	cyrillicA = "а" // Cyrillic a (homoglyph of Latin a)
)
