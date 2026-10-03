// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// managedStdio owns one exact session's MCP process. Its only spawn site uses
// the session Runner, including ARCH's process confinement, group and env policy.
type managedStdio struct {
	process  sessions.Process
	context  context.Context
	cancel   context.CancelFunc
	closed   chan struct{}
	frames   chan []byte
	permit   chan struct{}
	patterns []string
	sequence int64 // protected by permit
	once     sync.Once
	drained  chan struct{}
	failure  error // written before drained closes; contains only a safe sentence
}

func launchManagedStdio(ctx context.Context, runner sessions.Runner, spec sessions.LaunchSpec, patterns []string) (*managedStdio, error) {
	lifetime, cancel := context.WithCancel(ctx)
	process, err := runner.Launch(lifetime, spec)
	if err != nil || process == nil {
		cancel()
		if process != nil {
			stopManagedProcess(process)
		}
		return nil, newManagedStdioFailure("process_start", err, patterns)
	}
	s := &managedStdio{process: process, context: lifetime, cancel: cancel, closed: make(chan struct{}), drained: make(chan struct{}), frames: make(chan []byte, 16), permit: make(chan struct{}, 1), patterns: patterns}
	s.permit <- struct{}{}
	go func() {
		defer close(s.frames)
		defer cancel()
		defer close(s.drained)
		lastStderr := ""
		for frame := range process.Output() {
			if frame.Stream == "stderr" {
				if line := safeStdioDiagnostic(string(frame.Data), patterns); line != "" {
					lastStderr = line
				}
			}
			if frame.Stream != "stdout" {
				continue
			}
			if len(frame.Data) > 1<<20 {
				cancel()
				continue
			}
			select {
			case s.frames <- frame.Data:
			case <-lifetime.Done():
			default:
				cancel() // bounded unsolicited output; keep draining to reap
			}
		}
		code, err := process.Wait()
		if lastStderr != "" {
			err = errors.New(lastStderr)
		} else if err == nil && code != 0 {
			err = fmt.Errorf("MCP server exited with exit status %d", code)
		}
		s.failure = newManagedStdioFailure("process_exit", err, patterns)
	}()
	go func() { <-lifetime.Done(); stopManagedProcess(process); close(s.closed) }()
	return s, nil
}

// Join teardown and stderr drainage before exposing the cause. A broken stdin
// can be observed just before the process's final diagnostic reaches Output.
func (s *managedStdio) unavailable() error {
	s.cancel()
	<-s.closed
	<-s.drained
	return s.failure
}
func stopManagedProcess(process sessions.Process) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = process.Stop(ctx)
}
func (s *managedStdio) Close() {
	s.once.Do(s.cancel)
	<-s.closed
	<-s.drained
}

func (s *managedStdio) confinement() map[string]any {
	if process, ok := s.process.(interface{ Confinement() confine.State }); ok {
		state := process.Confinement()
		return map[string]any{"mode": state.Mode, "abi": state.ABI, "reason": state.Reason, "network_egress": "unrestricted"}
	}
	return map[string]any{"mode": "unknown", "reason": "the session runner does not report process confinement", "network_egress": "unrestricted"}
}

func (s *managedStdio) Forward(ctx context.Context, req mcpc.UpstreamRequest) (mcpc.UpstreamResult, error) {
	select {
	case <-ctx.Done():
		return mcpc.UpstreamResult{State: mcpc.DispatchNotSent}, ctx.Err()
	case <-s.context.Done():
		return mcpc.UpstreamResult{State: mcpc.DispatchNotSent}, s.unavailable()
	case <-s.permit:
	}
	defer func() { s.permit <- struct{}{} }()
	if ctx.Err() != nil || s.context.Err() != nil {
		return mcpc.UpstreamResult{State: mcpc.DispatchNotSent}, s.unavailable()
	}
	s.sequence++
	id := s.sequence
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": req.Method, "params": json.RawMessage(req.Params)})
	if err != nil {
		return mcpc.UpstreamResult{State: mcpc.DispatchNotSent}, errManagedMCPInvalidResponse
	}
	if err := s.process.Send(ctx, raw); err != nil {
		s.cancel()
		return mcpc.UpstreamResult{State: mcpc.DispatchUnknown}, s.unavailable()
	}
	for notifications := 0; notifications <= 128; notifications++ {
		select {
		case <-ctx.Done():
			s.cancel()
			return mcpc.UpstreamResult{State: mcpc.DispatchUnknown}, ctx.Err()
		case <-s.context.Done():
			return mcpc.UpstreamResult{State: mcpc.DispatchUnknown}, s.unavailable()
		case frame, ok := <-s.frames:
			if !ok {
				return mcpc.UpstreamResult{State: mcpc.DispatchUnknown}, s.unavailable()
			}
			if err := s.guard(frame); err != nil {
				s.cancel()
				return mcpc.UpstreamResult{State: mcpc.DispatchUnknown}, err
			}
			// Only recognized notifications can interleave a correlated response.
			var notification struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"method"`
			}
			if json.Unmarshal(frame, &notification) == nil && notification.JSONRPC == "2.0" && len(notification.ID) == 0 && (notification.Method == "notifications/message" || notification.Method == "notifications/progress" || notification.Method == "notifications/tools/list_changed" || notification.Method == "notifications/resources/updated") {
				continue
			}
			result, rpcErr, err := mcpc.ParseStrictJSONRPCResponse(frame, id)
			if err != nil {
				s.cancel()
				return mcpc.UpstreamResult{State: mcpc.DispatchUnknown}, errManagedMCPInvalidResponse
			}
			if rpcErr != nil {
				return mcpc.UpstreamResult{State: mcpc.DispatchCompleted}, errors.New("managed MCP request refused")
			}
			return mcpc.UpstreamResult{Result: result, State: mcpc.DispatchCompleted}, nil
		}
	}
	s.cancel()
	return mcpc.UpstreamResult{State: mcpc.DispatchUnknown}, errManagedMCPInvalidResponse
}

func (s *managedStdio) guard(raw []byte) error {
	if len(s.patterns) == 0 {
		return nil
	}
	reader := &mcpCredentialResponseReader{body: io.NopCloser(bytes.NewReader(raw)), patterns: s.patterns}
	for _, pattern := range s.patterns {
		reader.matchers = append(reader.matchers, newMCPCredentialMatcher(pattern))
	}
	_, err := io.Copy(io.Discard, reader)
	return err
}

func (s *managedStdio) Initialize(ctx context.Context) error {
	result, err := s.Forward(ctx, mcpc.UpstreamRequest{Method: "initialize", Params: []byte(`{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"olivares-managed-mcp","version":"26.10"}}`)})
	if err != nil {
		return err
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(result.Result, &init) != nil || init.ProtocolVersion != "2025-11-25" {
		return errManagedMCPInvalidResponse
	}
	if err := s.process.Send(ctx, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); err != nil {
		s.cancel()
		return s.unavailable()
	}
	return nil
}

func (s *managedStdio) ListTools(ctx context.Context) ([]mcpc.Tool, error) {
	return listManagedTools(ctx, s)
}

func listManagedTools(ctx context.Context, upstream mcpc.Upstream) ([]mcpc.Tool, error) {
	tools := []mcpc.Tool{}
	cursors := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 8; pages++ {
		params, _ := json.Marshal(map[string]string{"cursor": cursor})
		if cursor == "" {
			params = []byte(`{}`)
		}
		response, err := upstream.Forward(ctx, mcpc.UpstreamRequest{Method: "tools/list", Params: params})
		if err != nil {
			return nil, err
		}
		var result struct {
			Tools      []mcpc.Tool `json:"tools"`
			NextCursor string      `json:"nextCursor"`
		}
		if json.Unmarshal(response.Result, &result) != nil || result.Tools == nil {
			return nil, errManagedMCPInvalidResponse
		}
		tools = append(tools, result.Tools...)
		if len(tools) > 128 {
			return nil, errManagedMCPInvalidResponse
		}
		policies := make([]mcpc.ToolPolicy, len(tools))
		for i, tool := range tools {
			policies[i].Name = tool.Name
			if len(tool.InputSchema) == 0 {
				return nil, errManagedMCPInvalidResponse
			}
		}
		if _, err := mcpc.NewToolset(policies); err != nil {
			return nil, errManagedMCPInvalidResponse
		}
		if result.NextCursor == "" {
			return tools, nil
		}
		if len(result.NextCursor) > 1024 || cursors[result.NextCursor] {
			return nil, errManagedMCPInvalidResponse
		}
		cursors[result.NextCursor] = true
		cursor = result.NextCursor
	}
	return nil, errManagedMCPInvalidResponse
}
