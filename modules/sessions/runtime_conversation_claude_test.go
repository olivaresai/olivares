// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A long Claude Code session outgrows the live output ring (defaultRingFrames frames
// or defaultRingBytes). The conversation view reads every turn before the ring's
// window from the transcript the tool keeps, so no turn the person had is dropped:
// not while the session runs, and not after the engine no longer holds it.

// claudeTurns is a conversation of n turns as Claude Code stores it (transcript) and
// as it prints it (stream-json frames). Claude Code 2.1.316 gives a message the same
// uuid in both, measured with a stub Messages API on 2026-11-01.
func claudeTurns(n int) (transcript string, frames []string) {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		user := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"turn %04d"},"uuid":"u-%04d"`, i, i)
		reply := fmt.Sprintf(`{"type":"assistant","message":{"id":"msg_%04d","role":"assistant","content":[{"type":"text","text":"reply %04d"}]},"uuid":"a-%04d"`, i, i, i)
		b.WriteString(user + `,"timestamp":"2026-11-01T10:00:00.000Z","cwd":"/home/dev/secret-project","gitBranch":"main","sessionId":"` + moveTestSID + `"}` + "\n")
		b.WriteString(reply + `,"timestamp":"2026-11-01T10:00:01.000Z","cwd":"/home/dev/secret-project","sessionId":"` + moveTestSID + `"}` + "\n")
		frames = append(frames, user+`,"isReplay":true,"session_id":"`+moveTestSID+`"}`, reply+`,"parent_tool_use_id":null,"session_id":"`+moveTestSID+`"}`)
	}
	return b.String(), frames
}

// attachLines attaches as the console does and returns the line of each history and
// output event, in order, and whether the page event offered earlier frames.
func attachLines(t *testing.T, f *moveFixture, query string) (lines []string, earlier bool, raw string) {
	t.Helper()
	r := f.h.do("GET", "/v1/m/sessions/runs/"+f.runRef+"/attach?"+query, f.admin, f.hdr)
	if r.code != 200 {
		t.Fatalf("attach = %d %s", r.code, r.raw)
	}
	for _, ev := range parseSSE(r.raw) {
		switch ev.Event {
		case "history", "output":
			var data struct {
				Line string `json:"line"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
				t.Fatalf("%s event %q: %v", ev.Event, ev.Data, err)
			}
			lines = append(lines, data.Line)
		case "page":
			earlier = strings.Contains(ev.Data, `"earlier":true`)
		}
	}
	return lines, earlier, r.raw
}

// wantEveryTurnOnce checks that the view names turns 1..n once each, in order, and
// carries none of the transcript's path metadata.
func wantEveryTurnOnce(t *testing.T, lines []string, n int) {
	t.Helper()
	var turns []string
	for _, line := range lines {
		if strings.Contains(line, "secret-project") {
			t.Fatalf("the view carries the transcript's path metadata: %s", line)
		}
		if i := strings.Index(line, `"turn `); i >= 0 {
			turns = append(turns, line[i+1:i+10])
		}
	}
	if len(turns) != n {
		t.Fatalf("the view holds %d turns, want %d (first %v)", len(turns), n, turns[:min(len(turns), 3)])
	}
	for i, got := range turns {
		if want := fmt.Sprintf("turn %04d", i+1); got != want {
			t.Fatalf("turn %d of the view is %q, want %q", i+1, got, want)
		}
	}
}

func TestAttachHTTPLongClaudeSessionShowsEveryTurn(t *testing.T) {
	// 2100 turns are 4200 frames: the ring keeps the newest defaultRingFrames.
	const n = 2100
	transcript, frames := claudeTurns(n)
	f := messagesFixture(t, transcript)
	lr, ok := f.m.rt.getLive(f.tenant, f.runRef)
	if !ok {
		t.Fatal("the session is not live")
	}
	for _, frame := range frames {
		lr.ring.append(streamStdout, []byte(frame), time.Now())
	}
	if rd := lr.ring.readFrom(0); len(rd.frames) != defaultRingFrames {
		t.Fatalf("the ring holds %d frames, want it full at %d", len(rd.frames), defaultRingFrames)
	}
	lr.ring.close()

	t.Run("live", func(t *testing.T) {
		lines, earlier, _ := attachLines(t, f, "from=0&history=1&tail=500")
		wantEveryTurnOnce(t, lines, n)
		// The transcript already gave every earlier turn: the ring's older frames are
		// not offered a second time.
		if earlier {
			t.Fatal("the view offers earlier frames the transcript already showed")
		}
	})

	t.Run("not live in this engine", func(t *testing.T) {
		f.m.rt.dropLive(f.tenant, f.runRef)
		lines, _, raw := attachLines(t, f, "from=0&history=1&tail=500")
		wantEveryTurnOnce(t, lines, n)
		if !strings.Contains(raw, "not_live_on_node") {
			t.Fatalf("the attach no longer says the session is not live: %s", raw[max(0, len(raw)-400):])
		}
	})
}

// The transcript is the tool's own, unredacted: a session given a repository
// credential is not read, and the view says why instead of silently being shorter.
func TestAttachHTTPClaudeHistoryOfASecretBearingSessionIsRefused(t *testing.T) {
	transcript, _ := claudeTurns(2)
	f := messagesFixture(t, transcript)
	setRunColumn(t, f, colRunGitRead, "binding:owner/name")
	f.m.rt.dropLive(f.tenant, f.runRef)
	lines, _, raw := attachLines(t, f, "from=0&history=1")
	if len(lines) != 1 || !strings.Contains(lines[0], "olivares_notice") || !strings.Contains(lines[0], "repository credential") {
		t.Fatalf("a secret-bearing session's history = %s", raw)
	}
}

// What Claude Code keeps beside a message (its folder, the person's images as base64)
// stays out of the view; a record larger than one output line is said, not dropped
// silently, and the records after it are still shown.
func TestAttachHTTPClaudeHistoryRecordsAreProjectedAndBounded(t *testing.T) {
	image := `{"type":"user","uuid":"u-img","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` +
		strings.Repeat("A", 2<<20) + `"}},{"type":"text","text":"what is in this picture?"}]},"cwd":"/home/dev/secret-project"}`
	huge := `{"type":"assistant","uuid":"a-huge","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("x", maxOutputLine) + `"}]}}`
	after := `{"type":"assistant","uuid":"a-after","message":{"role":"assistant","content":[{"type":"text","text":"still shown"}]}}`
	f := messagesFixture(t, image+"\n"+huge+"\n"+after+"\n")
	f.m.rt.dropLive(f.tenant, f.runRef)
	lines, _, raw := attachLines(t, f, "from=0&history=1")
	if len(lines) != 3 {
		t.Fatalf("history = %d lines: %.600s", len(lines), raw)
	}
	for _, line := range lines {
		if len(line) > maxOutputLine {
			t.Fatalf("a history line of %d bytes is over the console's bound", len(line))
		}
	}
	if !strings.Contains(lines[0], "what is in this picture?") || strings.Contains(lines[0], "AAAA") || strings.Contains(lines[0], "secret-project") {
		t.Fatalf("the image message = %.300s", lines[0])
	}
	if !strings.Contains(lines[1], "olivares_notice") {
		t.Fatalf("an oversized record = %.300s", lines[1])
	}
	if !strings.Contains(lines[2], "still shown") {
		t.Fatalf("the record after it = %.300s", lines[2])
	}
}
