// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

type launchFlow struct{ id int }

func (*launchFlow) RestoreArguments(_ context.Context, _ mcpc.ToolFlowCall, a json.RawMessage) (json.RawMessage, map[string]int, error) {
	return a, nil, nil
}

func (*launchFlow) TokenizeResult(_ context.Context, _ mcpc.ToolFlowCall, r json.RawMessage) (json.RawMessage, map[string]int, error) {
	return r, nil, nil
}

func TestSessionDataFlowIsAbsentInCommunity(t *testing.T) {
	m := &mcpManagement{}
	if flow := m.sessionDataFlow("t/run/launch", context.Background()); flow != nil {
		t.Fatalf("no edition flow means none: %v", flow)
	}
}

// One flow per exact launch, shared by its servers, dropped when the launch ends.
func TestSessionDataFlowLivesExactlyAsLongAsTheLaunch(t *testing.T) {
	made := 0
	m := &mcpManagement{newToolDataFlow: func() mcpc.ToolDataFlow { made++; return &launchFlow{id: made} }}
	launch, end := context.WithCancel(context.Background())

	m.cacheMu.Lock()
	first := m.sessionDataFlow("t/run/launch-1", launch)
	again := m.sessionDataFlow("t/run/launch-1", launch)
	other := m.sessionDataFlow("t/run/launch-2", context.Background())
	m.cacheMu.Unlock()

	if first != again {
		t.Fatal("every server of one launch must share its flow")
	}
	if first == other {
		t.Fatal("another launch must not see this launch's tokens")
	}

	end()
	deadline := time.Now().Add(5 * time.Second)
	for {
		m.cacheMu.Lock()
		_, live := m.sessionFlows["t/run/launch-1"]
		m.cacheMu.Unlock()
		if !live {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the flow must be dropped when its launch ends")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.cacheMu.Lock()
	next := m.sessionDataFlow("t/run/launch-1", context.Background())
	m.cacheMu.Unlock()
	if next == first {
		t.Fatal("a dropped flow must never come back")
	}
}
