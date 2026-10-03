// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type commandBlockedHookInput struct {
	release <-chan struct{}
	source  io.Reader
}

func TestClaudeHookCommandPinnedEventDeniesStalledInput(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PermissionRequest", "PostToolUse"} {
		t.Run(event, func(t *testing.T) {
			release := make(chan struct{})
			var out bytes.Buffer
			cmd := newClaudeHookCmd()
			cmd.SetIn(commandBlockedHookInput{release: release, source: strings.NewReader("")})
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--timeout", "20ms", "--hook-event", event})
			err := cmd.Execute()
			close(release)
			if err != nil {
				t.Fatal(err)
			}
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
			if err := json.Unmarshal(out.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if event == "PostToolUse" {
				if reply.Decision != "block" {
					t.Fatalf("wrong PostToolUse deadline refusal: %s", out.Bytes())
				}
			} else if reply.Output.Event != event || (event == "PreToolUse" && reply.Output.Permission != "deny") || (event == "PermissionRequest" && reply.Output.Decision.Behavior != "deny") {
				t.Fatalf("wrong %s deadline refusal: %s", event, out.Bytes())
			}
		})
	}
}

func (r commandBlockedHookInput) Read(p []byte) (int, error) { <-r.release; return r.source.Read(p) }

func TestClaudeHookCommandWholeDeadlineDeniesStalledInput(t *testing.T) {
	cmd := newClaudeHookCmd()
	cmd.SetArgs([]string{"--endpoint", "http://127.0.0.1:1/", "--timeout", "20ms"})
	release := make(chan struct{})
	cmd.SetIn(commandBlockedHookInput{release: release, source: strings.NewReader(`{"hook_event_name":"PreToolUse"}`)})
	var out, diagnostic bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&diagnostic)
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	select {
	case err := <-done:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("command waited past its whole-helper deadline")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"permissionDecision":"deny"`)) || !bytes.Contains(out.Bytes(), []byte("timed out")) {
		t.Fatalf("command did not deny: %s", out.Bytes())
	}
}

func TestClaudeHookCommandDeadlineIncludesInvocationSetup(t *testing.T) {
	cmd := newClaudeHookCmd()
	cmd.SetArgs([]string{"--endpoint", "http://127.0.0.1:1/", "--timeout", "20ms"})
	time.Sleep(30 * time.Millisecond)
	release := make(chan struct{})
	cmd.SetIn(commandBlockedHookInput{release: release, source: strings.NewReader(`{"hook_event_name":"PreToolUse"}`)})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	select {
	case err := <-done:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("expired invocation tried to read input")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"permissionDecision":"deny"`)) {
		t.Fatalf("expired command did not deny: %s", out.Bytes())
	}
}

func TestClaudeHookPublishedDefaultDeniesBeforeManagedOuterDeadline(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"claude-hook"})
	release := make(chan struct{})
	root.SetIn(commandBlockedHookInput{release: release, source: strings.NewReader(`{"hook_event_name":"PreToolUse"}`)})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	done := make(chan error, 1)
	go func() { done <- root.Execute() }()
	select {
	case err := <-done:
		close(release)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		close(release)
		<-done
		t.Fatal("published hook cannot deliver its denial before the 5s managed timeout")
	}
	if !bytes.Contains(out.Bytes(), []byte(`"permissionDecision":"deny"`)) || !bytes.Contains(out.Bytes(), []byte("timed out")) {
		t.Fatalf("published default hook did not deny stalled input: %s", out.Bytes())
	}
}
