// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// An OpenCode edit is approved through session/request_permission and then written
// by the agent asking the CLIENT to do it (fs/write_text_file). These tests drive
// that request through a real owned child and the real runtime wiring.

func openCodeWriteTurn(t *testing.T, fixture openCodeFixture) (workDir string, reply json.RawMessage) {
	t.Helper()
	m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome)
	fixture.SessionID, fixture.PromptMode = "ses-fs", "hold"
	fixture.PromptReleasePath = filepath.Join(t.TempDir(), "never")
	record := setOpenCodeFixture(t, prof, fixture)
	dto, err := openCodeLaunch(t, m, tenant, prof)
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	t.Cleanup(func() { _, _ = m.stopRun(context.Background(), tenant, dto.RunRef, "user:u1", model.ActorUser) })
	if err := m.sendTextInput(context.Background(), tenant, dto.RunRef, "edit"); err != nil {
		t.Fatalf("input: %v", err)
	}
	waitFor(t, "the write request was answered", func() bool {
		rec, ok := tryReadOpenCodeFixtureRecord(record)
		return ok && len(rec.Replies) > 0
	})
	rec := readOpenCodeFixtureRecord(t, record)
	return rec.NewCwds[0], rec.Replies[0]
}

func TestOpenCodeRuntimeWritesAnEditInsideTheSessionFolder(t *testing.T) {
	workDir, reply := openCodeWriteTurn(t, openCodeFixture{
		WritePath: filepath.Join("notes", "hello.txt"), WriteContent: "written by the client\n",
	})
	if string(reply) != "{}" {
		t.Fatalf("answer = %s, want the empty result", reply)
	}
	got, err := os.ReadFile(filepath.Join(workDir, "notes", "hello.txt"))
	if err != nil {
		t.Fatalf("the file was not written: %v", err)
	}
	if string(got) != "written by the client\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestOpenCodeRuntimeRefusesAWriteOutsideTheSessionFolder(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "escape.txt")
	_, reply := openCodeWriteTurn(t, openCodeFixture{WritePath: outside, WriteContent: "no"})
	var refusal rpcError
	if err := json.Unmarshal(reply, &refusal); err != nil || refusal.Code != acpErrInvalidParams {
		t.Fatalf("answer = %s, want a protocol error with code %d", reply, acpErrInvalidParams)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("the file outside the folder exists: %v", err)
	}
}
