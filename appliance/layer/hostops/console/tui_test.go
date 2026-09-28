// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package console

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

func TestTUI_RendersAt80x24Monochrome(t *testing.T) {
	s := &session{}
	ui := NewTUI(s)
	for _, step := range []struct{ command, title string }{{"", "Categories"}, {"1", "Tasks"}, {"1", "Detail"}, {"next", "Inputs"}, {"next", "Plan"}, {"next", "Summary"}, {"next", "Confirmation"}, {"confirm", "Confirmation"}} {
		if step.command != "" {
			_, _ = ui.Handle(step.command)
		}
		frame := ui.Frame()
		lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
		if len(lines) != 24 || !strings.Contains(frame, step.title) || strings.Contains(frame, "\x1b") {
			t.Fatalf("%s: rows=%d frame=%q", step.title, len(lines), frame)
		}
		for _, line := range lines {
			if len(line) != 80 {
				t.Fatalf("%s: width=%d", step.title, len(line))
			}
		}
	}
	if s.calls != 1 || !strings.Contains(ui.Frame(), "act_not_adopted") {
		t.Fatalf("confirmation attempted an effect: calls=%d", s.calls)
	}
}

func TestTUI_RefreshReadsTheSameOperationInsteadOfItsOwnCopy(t *testing.T) {
	id := strings.Repeat("12", 16)
	s := &session{view: hostops.View{Record: hostops.Record{OperationID: id, State: hostops.StateRunning}, Permissions: hostops.Permissions{Read: true, Code: "act_not_adopted"}}}
	ui := NewTUI(s)
	if _, err := ui.Handle("op " + id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ui.Frame(), "running") {
		t.Fatal("missing running record")
	}
	s.view.Record.State = hostops.StatePartial
	s.view.Record.Target = "network\x1b[31m"
	if _, err := ui.Handle("refresh"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ui.Frame(), "partial") || strings.Contains(ui.Frame(), "\x1b") || s.calls != 2 {
		t.Fatalf("stale/unsafe frame: %q", ui.Frame())
	}
}

func TestTUI_CommandUsesTheLocalSessionAndClosesIt(t *testing.T) {
	s := &session{}
	var out, diagnostic bytes.Buffer
	code := Run(context.Background(), []string{"tui"}, strings.NewReader("1\n1\nnext\nnext\nq\n"), &out, &diagnostic, func() (Session, error) { return s, nil })
	if code != 0 || !s.closed || s.calls != 1 || !strings.Contains(out.String(), "Command: olivares-appliance") || diagnostic.Len() != 0 {
		t.Fatalf("exit=%d calls=%d closed=%v stderr=%s", code, s.calls, s.closed, diagnostic.String())
	}
}

func (s *session) Descriptors(string) ([]hostops.Descriptor, error) {
	return []hostops.Descriptor{hostops.StatusDescriptor()}, s.err
}
