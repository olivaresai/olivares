// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// loadCall is one observed load_skill emission: the skill member pinned and the
// conversation it was pinned TO (empty for a workspace/template target, whose
// assignment serves future conversations).
type loadCall struct {
	conversation string
	skill        string
}

type recordingLoadTracer struct {
	mu    sync.Mutex
	calls []loadCall
}

func (r *recordingLoadTracer) LoadSkill(conversationID, skillName string) *obstrace.AgentSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, loadCall{conversation: conversationID, skill: skillName})
	return nil // the nil handle's End is a no-op; the real provider's span is asserted in core
}

func (r *recordingLoadTracer) snapshot() []loadCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]loadCall(nil), r.calls...)
}

// sessionOrWorkspaceAuthority resolves a session target statically (a session is
// named by its public run_ref; no row is read) and a workspace target through
// its stored row, like the production adapters.
type sessionOrWorkspaceAuthority struct{}

func (sessionOrWorkspaceAuthority) ResolveSkillsTarget(ctx context.Context, sc store.Scope, p auth.Principal, target skills.Target, write bool) (skills.StoredTarget, error) {
	if target.Kind == "session" {
		return skills.StoredTarget{Target: target, Version: 1}, nil
	}
	return workspaceAuthority{}.ResolveSkillsTarget(ctx, sc, p, target, write)
}

// An assignment pin emits one load_skill span per selected member; a session
// target names the conversation, a workspace target (future conversations)
// carries none. A failed assignment loads nothing.
func TestSkillsAssignmentEmitsLoadSkillPerMember(t *testing.T) {
	rec := &recordingLoadTracer{}
	h := catalogOptions(t, skills.Options{Targets: sessionOrWorkspaceAuthority{}, Tracer: rec})
	pack := h.install(t, "load-skill-traced", "fixture")

	// A session target is named by its public run_ref, a canonical id.
	session := skills.Target{Kind: "session", ID: model.NewID().String()}
	response := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{
		Target: session, RevisionID: pack.Revision.ID, Members: []string{"research"},
	}, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("session assignment: %d %s", response.Code, response.Body.String())
	}
	calls := rec.snapshot()
	if len(calls) != 1 || calls[0].skill != "research" || calls[0].conversation != session.ID {
		t.Fatalf("session assignment load_skill calls = %+v, want one research on %s", calls, session.ID)
	}

	workspace := h.workspace(t, false)
	response = h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{
		Target: workspace, RevisionID: pack.Revision.ID, Members: []string{"research"},
	}, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("workspace assignment: %d %s", response.Code, response.Body.String())
	}
	calls = rec.snapshot()
	if len(calls) != 2 || calls[1].skill != "research" || calls[1].conversation != "" {
		t.Fatalf("workspace assignment load_skill calls = %+v, want one research with no conversation", calls)
	}

	// A refused assignment (unknown target kind) must load nothing.
	response = h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{
		Target: skills.Target{Kind: "nope", ID: "x"}, RevisionID: pack.Revision.ID,
	}, "")
	if response.Code == http.StatusCreated {
		t.Fatal("an unknown target kind must not create an assignment")
	}
	if calls = rec.snapshot(); len(calls) != 2 {
		t.Fatalf("a refused assignment emitted load_skill calls: %+v", calls)
	}
}
