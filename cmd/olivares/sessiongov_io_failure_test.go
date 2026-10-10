// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Only the I/O anchor append fails; lifecycle writes still use the real store.
type ioFaultStore struct {
	store.Store
	fail  atomic.Bool
	calls atomic.Int64
}

func (s *ioFaultStore) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(ioFaultScope{Scope: sc, audit: ioFaultAudit{AuditLog: sc.Audit(), fault: s}})
	})
}

type ioFaultScope struct {
	store.Scope
	audit store.AuditLog
}

func (s ioFaultScope) Audit() store.AuditLog { return s.audit }

type ioFaultAudit struct {
	store.AuditLog
	fault *ioFaultStore
}

var errIOAppend = errors.New("I/O audit append unavailable: private-path secret-value")

func (a ioFaultAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if draft.Action == sessionIOAction {
		a.fault.calls.Add(1)
		if a.fault.fail.Load() {
			return model.AuditEvent{}, errIOAppend
		}
	}
	return a.AuditLog.Append(ctx, draft)
}

func TestSessionIORecorder_FailedSealCanBeRetried(t *testing.T) {
	_, st, tenant := newSessionsStore(t)
	fault := &ioFaultStore{Store: st}
	fault.fail.Store(true)
	rec := newSessionIORecorder(fault, nil)
	ctx := context.Background()
	const ref = "retry-seal"
	frame := sessions.RecordedFrame{Seq: 1, Stream: "stdout", Data: []byte("evidence")}
	if err := rec.Record(ctx, tenant, ref, frame); err != nil {
		t.Fatal(err)
	}
	expected := append([]byte(nil), rec.chains[tenant.String()+"|"+ref].tip...)
	for i := 0; i < 2; i++ {
		if err := rec.Finalize(ctx, tenant, ref); !errors.Is(err, errIOAppend) {
			t.Errorf("failed seal attempt %d returned %v, want append failure", i+1, err)
		}
	}
	fault.fail.Store(false)
	if err := rec.Finalize(ctx, tenant, ref); err != nil {
		t.Fatal(err)
	}
	if err := rec.Finalize(ctx, tenant, ref); err != nil {
		t.Fatal(err)
	}
	anchors := 0
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().(store.CanonicalWalker).WalkCanonical(ctx, 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action == sessionIOAction && ev.TargetID == model.ID(ref) {
				anchors++
				if !bytes.Equal(ev.PayloadHash, expected) || !strings.Contains(meta, `"sealed":true`) || !strings.Contains(meta, `"frames":1`) {
					t.Errorf("retry lost the original chain or seal: %s", meta)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if anchors != 1 || len(rec.chains) != 0 {
		t.Fatalf("anchors=%d retained chains=%d, want one seal and no retained chain", anchors, len(rec.chains))
	}
}

func TestSessionIORecorder_ResumeSealsTheRetainedChainFirst(t *testing.T) {
	_, st, tenant := newSessionsStore(t)
	fault := &ioFaultStore{Store: st}
	fault.fail.Store(true)
	rec := newSessionIORecorder(fault, nil)
	ctx := context.Background()
	const ref = "resume-after-failed-seal"
	if err := rec.Record(ctx, tenant, ref, sessions.RecordedFrame{Seq: 1, Stream: "stdout", Data: []byte("old output")}); err != nil {
		t.Fatal(err)
	}
	oldTip := append([]byte(nil), rec.chains[tenant.String()+"|"+ref].tip...)
	if err := rec.Finalize(ctx, tenant, ref); !errors.Is(err, errIOAppend) {
		t.Fatal(err)
	}
	newFrame := sessions.RecordedFrame{Seq: 65, Stream: "stdout", Data: []byte("new output")}
	if err := rec.Record(ctx, tenant, ref, newFrame); !errors.Is(err, errIOAppend) {
		t.Fatalf("resume must fail while the previous seal still fails: %v", err)
	}
	if !bytes.Equal(rec.chains[tenant.String()+"|"+ref].tip, oldTip) {
		t.Fatal("failed resume changed the retained chain")
	}
	fault.fail.Store(false)
	if err := rec.Record(ctx, tenant, ref, newFrame); err != nil {
		t.Fatal(err)
	}
	if err := rec.Finalize(ctx, tenant, ref); err != nil {
		t.Fatal(err)
	}
	var tips [][]byte
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().(store.CanonicalWalker).WalkCanonical(ctx, 0, func(ev model.AuditEvent, meta string, _ []byte) error {
			if ev.Action == sessionIOAction && ev.TargetID == model.ID(ref) && strings.Contains(meta, `"sealed":true`) {
				tips = append(tips, ev.PayloadHash)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if len(tips) != 2 || !bytes.Equal(tips[0], oldTip) || bytes.Equal(tips[0], tips[1]) {
		t.Fatalf("resume must seal the old chain separately before recording the new one; seals=%d", len(tips))
	}
}

func TestCriticalRun_IOAppendFailureIsVisible(t *testing.T) {
	for _, stage := range []string{"batch", "seal"} {
		t.Run(stage, func(t *testing.T) {
			s := bootStubSessionEngine(t, false)
			// On input the real child produces enough frames for a periodic anchor.
			path := filepath.Join(s.folder, "agent.py")
			stub, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			stub = bytes.Replace(stub, []byte("for line in sys.stdin:\n"), []byte("for line in sys.stdin:\n    for i in range(64):\n        print(json.dumps({'type':'system','subtype':'status','message':'frame'}), flush=True)\n"), 1)
			if err := os.WriteFile(path, stub, 0o700); err != nil {
				t.Fatal(err)
			}
			fault := &ioFaultStore{Store: s.eng.store}
			fault.fail.Store(true)
			rec := newSessionIORecorder(fault, nil)
			sessions.WithRecorder(rec)(s.eng.sessionsMod)
			// Production CRITICAL classification/recording; approval is a labelled stand-in.
			sessions.WithLaunchGate(&sessionLaunchGate{bridge: &fakeOpener{status: nbApproved}, pep: s.eng.hookCredentials().provisioner()})(s.eng.sessionsMod)
			ws := s.do("POST", "/v1/m/sessions/workspaces", map[string]any{"root_path": s.folder, "name": "folder"}, http.StatusCreated)
			profile := s.do("POST", "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "config_home": s.home, "user_home": s.home, "auth_source": "provider_account_home", "display_name": "stub"}, http.StatusCreated)
			run := s.do("POST", "/v1/m/sessions/runs", map[string]any{"permission_mode": "dontAsk", "workspace_ref": ws["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
			ref := run["run_ref"].(string)
			if run["critical"] != true || run["record_io"] != true {
				t.Fatalf("run is not CRITICAL and recorded: %v", run)
			}
			eventually(t, "init frame", func() bool {
				return s.do("GET", "/v1/m/sessions/runs/"+ref, nil, http.StatusOK)["claude_session_id"] == "dormant-stub"
			})
			if stage == "seal" {
				s.do("POST", "/v1/m/sessions/runs/"+ref+"/stop", map[string]any{}, http.StatusOK)
			} else {
				s.do("POST", "/v1/m/sessions/runs/"+ref+"/input", map[string]any{"line": `{"type":"user","message":{"role":"user","content":"burst"}}`}, http.StatusAccepted)
			}
			var ended map[string]any
			eventually(t, "failed run with an I/O evidence reason", func() bool {
				ended = s.do("GET", "/v1/m/sessions/runs/"+ref, nil, http.StatusOK)
				return ended["state"] == "failed"
			})
			if reason, _ := ended["reason"].(string); !strings.Contains(reason, "I/O evidence") || strings.Contains(reason, "private-path") || strings.Contains(reason, "secret-value") {
				t.Fatalf("missing or unsafe failure reason: %q", reason)
			}
			if fault.calls.Load() == 0 {
				t.Fatal("no actual audit append attempted")
			}
			rec.mu.Lock()
			retained := rec.chains[s.tenant+"|"+ref] != nil
			rec.mu.Unlock()
			if !retained {
				t.Fatal("failed seal discarded the chain")
			}
			fault.fail.Store(false)
			if err := rec.Finalize(context.Background(), model.TenantID(s.tenant), ref); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: state=%v reason=%v; retained seal retried", stage, ended["state"], ended["reason"])
		})
	}
}
