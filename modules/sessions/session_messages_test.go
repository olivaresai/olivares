// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// #2129: a session's messages are listed, searched and reached by time from the
// transcript the tool keeps, the way its activity is: on the server, scoped to the
// session the person may read, never the whole conversation in one answer.

// messagesTranscript is a Claude Code transcript with what a conversation holds that
// is not a message the person sent or the tool answered: a tool result, a meta record,
// a notification, a subagent's record and a compaction summary. Messages 1, 3 and 5
// are the person's.
const messagesTranscript = `{"type":"user","timestamp":"2026-10-14T10:00:00.000Z","message":{"role":"user","content":"Plan the migration\nthen list the risks"}}
{"type":"assistant","timestamp":"2026-10-14T10:00:05.000Z","message":{"content":[{"type":"text","text":"Here is the plan."},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","timestamp":"2026-10-14T10:00:06.000Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"a b"}]}}
{"type":"user","timestamp":"2026-10-14T10:00:07.000Z","isMeta":true,"message":{"content":"Base directory for this skill: /home/dev/skills/tdd"}}
{"type":"user","timestamp":"2026-10-14T10:00:08.000Z","message":{"content":"<task-notification>\n<task-id>b1</task-id>\n</task-notification>"}}
{"type":"user","timestamp":"2026-10-14T10:00:09.000Z","isSidechain":true,"message":{"content":"A subagent was asked this"}}
{"type":"user","timestamp":"2026-10-14T11:30:00.000Z","message":{"content":[{"type":"text","text":"Now the rollback: what about PostgreSQL?"}]}}
{"type":"assistant","timestamp":"2026-10-14T11:30:10.000Z","message":{"content":[{"type":"text","text":"PostgreSQL needs a separate down migration."}]}}
{"type":"user","timestamp":"2026-10-14T11:31:00.000Z","isCompactSummary":true,"message":{"content":"This session is being continued from a previous conversation."}}
{"type":"user","timestamp":"2026-10-15T09:00:00.000Z","message":{"content":"Third request, a day later"}}
`

func (f *moveFixture) messages(t *testing.T, query string) (messagesDTO, resp) {
	t.Helper()
	path := "/v1/m/sessions/runs/" + f.runRef + "/messages"
	if query != "" {
		path += "?" + query
	}
	r := f.h.doJSON("GET", path, f.admin, nil, f.hdr)
	var dto messagesDTO
	if r.code == http.StatusOK {
		if err := json.Unmarshal([]byte(r.raw), &dto); err != nil {
			t.Fatal(err)
		}
	}
	return dto, r
}

func messageIDs(items []messageDTO) []int {
	ids := make([]int, 0, len(items))
	for _, m := range items {
		ids = append(ids, m.ID)
	}
	return ids
}

func messagesFixture(t *testing.T, transcript string) *moveFixture {
	t.Helper()
	f := newMoveFixture(t)
	writeFile(t, filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl"), transcript)
	return f
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// By default the index is what the person sent; "all" adds the tool's answers, and a
// message keeps its id either way.
func TestMessages_ListsWhatWasSentWithItsTimeAndFirstLine(t *testing.T) {
	f := messagesFixture(t, messagesTranscript)

	got, r := f.messages(t, "")
	if r.code != http.StatusOK || got.Driver != "claude" || got.Transcript != activityTranscriptRead {
		t.Fatalf("messages = %d %s", r.code, r.raw)
	}
	if want := []int{1, 3, 5}; !sameInts(messageIDs(got.Items), want) || got.Total != 3 {
		t.Fatalf("a person's messages = %v of %d, want %v: %s", messageIDs(got.Items), got.Total, want, r.raw)
	}
	first := got.Items[0]
	if first.Role != "user" || first.At != "2026-10-14T10:00:00.000Z" || first.FirstLine != "Plan the migration" || first.Text != "" {
		t.Fatalf("first message = %+v", first)
	}
	if got.Items[1].FirstLine != "Now the rollback: what about PostgreSQL?" {
		t.Fatalf("a text block's message = %+v", got.Items[1])
	}

	all, _ := f.messages(t, "role=all")
	if want := []int{1, 2, 3, 4, 5}; !sameInts(messageIDs(all.Items), want) || all.Total != 5 {
		t.Fatalf("all messages = %v of %d", messageIDs(all.Items), all.Total)
	}
	if all.Items[1].Role != "assistant" || all.Items[1].FirstLine != "Here is the plan." {
		t.Fatalf("an answer = %+v", all.Items[1])
	}
}

// The answer is a page: a long session is read in windows, from either end.
func TestMessages_AreReadInPagesFromEitherEnd(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, `{"type":"user","timestamp":"2026-10-14T10:%02d:%02d.000Z","message":{"content":"request %d"}}`+"\n", i/60, i%60, i)
		fmt.Fprintf(&b, `{"type":"assistant","timestamp":"2026-10-14T10:%02d:%02d.500Z","message":{"content":[{"type":"text","text":"answer %d"}]}}`+"\n", i/60, i%60, i)
	}
	f := messagesFixture(t, b.String())

	// The user's messages are the odd ids; 200 is the most a page holds.
	got, _ := f.messages(t, "limit=3")
	if want := []int{1, 3, 5}; !sameInts(messageIDs(got.Items), want) || got.Total != 500 || got.Next != "5" {
		t.Fatalf("first page = %v total %d next %q", messageIDs(got.Items), got.Total, got.Next)
	}
	got, _ = f.messages(t, "limit=3&cursor="+got.Next)
	if want := []int{7, 9, 11}; !sameInts(messageIDs(got.Items), want) || got.Next != "11" {
		t.Fatalf("second page = %v next %q", messageIDs(got.Items), got.Next)
	}

	// The newest end, oldest first inside the page; its cursor goes further back.
	got, _ = f.messages(t, "order=desc&limit=3")
	if want := []int{995, 997, 999}; !sameInts(messageIDs(got.Items), want) || got.Next != "995" {
		t.Fatalf("last page = %v next %q", messageIDs(got.Items), got.Next)
	}
	got, _ = f.messages(t, "order=desc&limit=3&cursor="+got.Next)
	if want := []int{989, 991, 993}; !sameInts(messageIDs(got.Items), want) {
		t.Fatalf("page before = %v", messageIDs(got.Items))
	}

	// The end of the list has no next.
	got, _ = f.messages(t, "limit=200&cursor=900")
	if len(got.Items) != 50 || got.Next != "" {
		t.Fatalf("tail = %d items, next %q", len(got.Items), got.Next)
	}
	got, _ = f.messages(t, "order=desc&limit=200&cursor=3")
	if want := []int{1}; !sameInts(messageIDs(got.Items), want) || got.Next != "" {
		t.Fatalf("head = %v next %q", messageIDs(got.Items), got.Next)
	}
	// A page never holds more than the bound, whatever was asked.
	got, _ = f.messages(t, "limit=100000")
	if len(got.Items) != messagesPageMax || got.Next == "" {
		t.Fatalf("an oversized limit gave %d items", len(got.Items))
	}
}

// Search reads the whole message, not its first line, and ignores case.
func TestMessages_SearchReadsTheWholeMessage(t *testing.T) {
	f := messagesFixture(t, messagesTranscript)

	got, r := f.messages(t, "q=LIST+THE+RISKS")
	if r.code != http.StatusOK || !sameInts(messageIDs(got.Items), []int{1}) || got.Total != 1 {
		t.Fatalf("a phrase on the second line = %s", r.raw)
	}
	if got.Items[0].FirstLine != "Plan the migration" {
		t.Fatalf("a result shows the first line: %+v", got.Items[0])
	}
	// Only the person's messages by default; the answers on request.
	if got, _ = f.messages(t, "q=postgresql"); !sameInts(messageIDs(got.Items), []int{3}) {
		t.Fatalf("search among sent = %v", messageIDs(got.Items))
	}
	if got, _ = f.messages(t, "q=postgresql&role=all"); !sameInts(messageIDs(got.Items), []int{3, 4}) {
		t.Fatalf("search among all = %v", messageIDs(got.Items))
	}
	if got, _ = f.messages(t, "q=absent+phrase"); len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("no match = %+v", got)
	}
}

// A time reaches the first message at or after it.
func TestMessages_ATimeReachesTheFirstMessageAtOrAfterIt(t *testing.T) {
	f := messagesFixture(t, messagesTranscript)

	for _, tc := range []struct {
		at   string
		want int
	}{
		{"2026-10-14T10:00:00Z", 1},
		{"2026-10-14T10:00:01Z", 3},
		{"2026-10-14T11:30:00Z", 3},
		{"2026-10-14T13:00:00%2B02:00", 3}, // 11:00Z, a zone other than UTC
		{"2026-10-14T23:59:59Z", 5},
	} {
		got, r := f.messages(t, "limit=1&at="+tc.at)
		if r.code != http.StatusOK || len(got.Items) != 1 || got.Items[0].ID != tc.want {
			t.Fatalf("at %s = %s, want %d", tc.at, r.raw, tc.want)
		}
	}
	if got, _ := f.messages(t, "at=2026-10-16T00:00:00Z"); len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("after the last message = %+v", got)
	}
}

// One message is read whole by its id, so a jump reaches words the index only clips.
func TestMessages_OneMessageIsReadWhole(t *testing.T) {
	long := strings.Repeat("word ", 400)
	f := messagesFixture(t, `{"type":"user","timestamp":"2026-10-14T10:00:00.000Z","message":{"content":"`+long+`"}}`+"\n")

	got, _ := f.messages(t, "")
	if len(got.Items) != 1 || got.Items[0].Text != "" || len([]rune(got.Items[0].FirstLine)) > messageFirstLineMax+1 {
		t.Fatalf("the index carries a clipped first line only: %+v", got.Items)
	}
	got, r := f.messages(t, "id=1")
	if r.code != http.StatusOK || len(got.Items) != 1 || got.Items[0].Text != strings.TrimSpace(long) {
		t.Fatalf("message by id = %s", r.raw)
	}
	if _, r = f.messages(t, "id=2"); r.code != http.StatusNotFound {
		t.Fatalf("an id the session has not = %d %s", r.code, r.raw)
	}
}

// A reader that holds only the newest part of a conversation tells which of several
// equal rows a message is by how many later messages repeat its words.
func TestMessages_ARepeatedMessageSaysHowManyLaterOnesRepeatIt(t *testing.T) {
	f := messagesFixture(t, `{"type":"user","timestamp":"2026-10-14T10:00:00Z","message":{"content":"continue"}}
{"type":"assistant","timestamp":"2026-10-14T10:00:01Z","message":{"content":[{"type":"text","text":"continue"}]}}
{"type":"user","timestamp":"2026-10-14T10:01:00Z","message":{"content":"continue"}}
{"type":"user","timestamp":"2026-10-14T10:02:00Z","message":{"content":"other"}}
{"type":"user","timestamp":"2026-10-14T10:03:00Z","message":{"content":"continue\nwith more"}}
`)
	for id, want := range map[int]int{1: 2, 3: 1, 5: 0, 4: 0} {
		got, r := f.messages(t, "id="+strconv.Itoa(id))
		if r.code != http.StatusOK || len(got.Items) != 1 || got.Items[0].LaterSame != want {
			t.Fatalf("message %d: later_same = %+v, want %d (%s)", id, got.Items, want, r.raw)
		}
	}
	// The index does not carry it.
	got, _ := f.messages(t, "role=all")
	for _, m := range got.Items {
		if m.LaterSame != 0 {
			t.Fatalf("the index carries later_same: %+v", m)
		}
	}
}

// text=1 is a page whose items carry their whole text, bounded as one message read by
// id is, for a reader that copies the conversation; the index stays without it.
func TestMessages_APageCarriesWholeTextsOnRequest(t *testing.T) {
	long := strings.Repeat("x", messageTextMax+10)
	f := messagesFixture(t, messagesTranscript+
		`{"type":"assistant","timestamp":"2026-10-15T09:00:05.000Z","message":{"content":[{"type":"text","text":"`+long+`"}]}}`+"\n")

	got, r := f.messages(t, "role=all&order=asc&text=1&limit=2")
	if r.code != http.StatusOK || !sameInts(messageIDs(got.Items), []int{1, 2}) || got.Next != "2" || got.Total != 6 {
		t.Fatalf("first page with text = %s", r.raw)
	}
	if got.Items[0].Text != "Plan the migration\nthen list the risks" || got.Items[1].Text != "Here is the plan." {
		t.Fatalf("texts = %+v", got.Items)
	}
	got, r = f.messages(t, "role=all&order=asc&text=1&limit=50&cursor="+got.Next)
	if r.code != http.StatusOK || !sameInts(messageIDs(got.Items), []int{3, 4, 5, 6}) || got.Next != "" {
		t.Fatalf("rest with text = %s", r.raw)
	}
	if got.Items[1].Text != "PostgreSQL needs a separate down migration." || got.Items[3].Text != clipBytes(long, messageTextMax) {
		t.Fatalf("texts = %d bytes last, items %+v", len(got.Items[3].Text), got.Items[:3])
	}
	// A text cut at the bound says so, so a copy of it cannot pass for the whole message;
	// a whole one does not.
	if !got.Items[3].Clipped || got.Items[1].Clipped {
		t.Fatalf("clipped = %v (cut), %v (whole)", got.Items[3].Clipped, got.Items[1].Clipped)
	}
	// Newest first, the page keeps the newest `limit` and carries their whole texts.
	got, r = f.messages(t, "role=all&order=desc&text=1&limit=2")
	if r.code != http.StatusOK || !sameInts(messageIDs(got.Items), []int{5, 6}) || got.Next != "5" {
		t.Fatalf("newest page with text = %s", r.raw[:min(len(r.raw), 300)])
	}
	if got.Items[0].Text == "" || got.Items[0].Clipped || got.Items[1].Text != clipBytes(long, messageTextMax) || !got.Items[1].Clipped {
		t.Fatalf("newest texts = %+v", got.Items[0])
	}
	if one, r := f.messages(t, "id=6"); r.code != http.StatusOK || len(one.Items) != 1 || !one.Items[0].Clipped {
		t.Fatalf("one long message read by id = %s", r.raw[:min(len(r.raw), 300)])
	}
	if strings.Contains(r.raw, "later_same") {
		t.Fatalf("a page with text carries later_same: %s", r.raw)
	}
	// A page of whole texts is a smaller page: past its own ceiling the read clamps, as the
	// index does at its own, and the cursor carries the rest.
	if parsed, err := parseMessageQuery(url.Values{"text": {"1"}, "limit": {"200"}}); err != nil || parsed.limit != messagesTextPageMax {
		t.Fatalf("text page limit = %d (%v), want %d", parsed.limit, err, messagesTextPageMax)
	}
	// Without it the index is what it was.
	if got, _ = f.messages(t, "role=all"); got.Items[0].Text != "" {
		t.Fatalf("the index carries text: %+v", got.Items[0])
	}
}

func TestMessages_RefuseWhatTheyCannotAnswer(t *testing.T) {
	f := messagesFixture(t, messagesTranscript)
	for _, q := range []string{
		"at=yesterday", "role=tool", "limit=0", "limit=x", "order=sideways", "cursor=-1", "cursor=x", "id=0",
		"text=0", "text=true", "text=yes",
		"q=" + strings.Repeat("a", messageQueryMax+1),
	} {
		if _, r := f.messages(t, q); r.code != http.StatusBadRequest {
			t.Fatalf("%q = %d %s", q, r.code, r.raw)
		}
	}
	if got, r := f.messages(t, "q="+url.QueryEscape("100% sure \x00?")); r.code != http.StatusOK || got.Total != 0 {
		t.Fatalf("a query is text, not a pattern: %d %s", r.code, r.raw)
	}
}

// The same rules as the activity: a transcript that is not there yet is said so, a
// session whose transcript could carry a secret is refused, and a read is recorded.
func TestMessages_FollowTheActivityRules(t *testing.T) {
	f := newMoveFixture(t)
	transcript := filepath.Join(f.homeA, "projects", moveTestProject, moveTestSID+".jsonl")
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	if got, r := f.messages(t, ""); r.code != http.StatusOK || got.Transcript != activityTranscriptPending || got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("no transcript yet = %d %s", r.code, r.raw)
	}
	f.stop(t)
	if got, _ := f.messages(t, ""); got.Transcript != activityTranscriptMissing {
		t.Fatalf("a stopped session without its transcript = %q", got.Transcript)
	}
	writeFile(t, transcript, messagesTranscript)
	if got, _ := f.messages(t, ""); got.Transcript != activityTranscriptRead || got.Total != 3 {
		t.Fatalf("a stopped session's messages = %+v", got)
	}

	// Reads of the same session by the same person are recorded once a window, and a
	// search is a read of conversation like the list.
	f.messages(t, "q=postgresql")
	f.messages(t, "")
	if n := countAudit(t, f, "sessions.run.messages"); n != 1 {
		t.Fatalf("message reads recorded %d times, want 1", n)
	}
	// A read of whole texts is an export of the conversation: it leaves its own record,
	// which the index read before it in the same window does not stand in for.
	f.messages(t, "role=all&text=1")
	if n := countAudit(t, f, "sessions.run.messages.text"); n != 1 {
		t.Fatalf("whole-text reads recorded %d times, want 1", n)
	}
	// The activity's own record is not what dedupes this one.
	f.activity(t)
	if countAudit(t, f, "sessions.run.activity") != 1 {
		t.Fatal("the activity read was not recorded beside the messages")
	}

	setRunColumn(t, f, colRunGitRead, "binding:owner/name")
	if _, r := f.messages(t, ""); r.code != http.StatusConflict || !strings.Contains(r.raw, "repository credential") {
		t.Fatalf("secret-bearing session = %d %s", r.code, r.raw)
	}
	setRunColumn(t, f, colRunGitRead, "")
	setRunColumn(t, f, colRunProfileDriver, "opencode")
	if _, r := f.messages(t, ""); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("another tool's session = %d %s", r.code, r.raw)
	}
}

// What the session's vault secrets are is withheld from a message as from its stream.
func TestMessages_AreRedactedLikeTheStream(t *testing.T) {
	const secret = "sk-live-messages-0123456789"
	redact := newSecretRedactor([]SecretEnvRef{{Env: "API_KEY", Secret: "deploy-key"}}, []EnvVar{{Name: "API_KEY", Value: secret}})
	var got []messageDTO
	_, err := scanMessages(t.Context(), strings.NewReader(`{"type":"user","timestamp":"2026-10-14T10:00:00Z","message":{"content":"use `+secret+` to deploy"}}`+"\n"),
		providerDriverClaude, "", redact, func(m scannedMessage) {
			got = append(got, messageDTO{ID: m.id, FirstLine: messageFirstLine(m.text)})
		})
	if err != nil || len(got) != 1 || strings.Contains(got[0].FirstLine, secret) || !strings.Contains(got[0].FirstLine, "deploy-key") {
		t.Fatalf("scanned = %+v err %v", got, err)
	}
}

// A record the tool is still writing waits for the next read, and one past the bound
// is skipped without losing the messages after it.
func TestMessages_PartialAndOversizedRecords(t *testing.T) {
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("x", activityLineMax+10) + `"}}` + "\n"
	f := messagesFixture(t, huge+messagesTranscript+`{"type":"user","timestamp":"2026-10-15T10:00:00Z","message":{"content":"being wri`)
	got, r := f.messages(t, "")
	if r.code != http.StatusOK || got.Total != 3 || got.Unreadable != 1 {
		t.Fatalf("messages = %s", r.raw)
	}
}

// Codex keeps messages in its rollout as response items; what it injects as context
// for the model is not what the person typed.
func TestMessages_ReadACodexRollout(t *testing.T) {
	f, file := newCodexActivityFixture(t)
	writeFile(t, file, `{"timestamp":"2026-10-13T12:00:00Z","type":"session_meta","payload":{"id":"`+moveTestSID+`","source":"cli"}}
{"timestamp":"2026-10-13T12:00:01Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>"}]}}
{"timestamp":"2026-10-13T12:00:02Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for /work\n\n<INSTRUCTIONS>x</INSTRUCTIONS>"},{"type":"input_text","text":"<environment_context>\n<cwd>/work</cwd>\n</environment_context>"}]}}
{"timestamp":"2026-10-13T12:00:03Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Fix the flaky test\nin the parser"}]}}
{"timestamp":"2026-10-13T12:00:04Z","type":"response_item","payload":{"type":"reasoning","summary":[]}}
{"timestamp":"2026-10-13T12:00:09Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"The parser reads the clock."}]}}
{"timestamp":"2026-10-13T12:05:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Run it again"}]}}
`)
	got, r := f.messages(t, "role=all")
	if r.code != http.StatusOK || got.Driver != "codex" || got.Transcript != activityTranscriptRead {
		t.Fatalf("codex messages = %d %s", r.code, r.raw)
	}
	if want := []int{1, 2, 3}; !sameInts(messageIDs(got.Items), want) {
		t.Fatalf("ids = %v: %s", messageIDs(got.Items), r.raw)
	}
	if got.Items[0].Role != "user" || got.Items[0].FirstLine != "Fix the flaky test" || got.Items[1].Role != "assistant" || got.Items[2].FirstLine != "Run it again" {
		t.Fatalf("codex items = %+v", got.Items)
	}
	if got, _ = f.messages(t, "q=parser"); !sameInts(messageIDs(got.Items), []int{1}) || got.Items[0].At != "2026-10-13T12:00:03Z" {
		t.Fatalf("a sent message by a word of its second line = %+v", got.Items)
	}
	if got, _ = f.messages(t, "q=clock"); len(got.Items) != 0 || got.Total != 0 {
		t.Fatalf("an answer is not among the sent messages: %+v", got)
	}

	// A rollout whose first record is not this session's root thread is not read.
	writeFile(t, file, strings.Replace(codexActivityRecords, moveTestSID, "other-thread", 1))
	if _, r := f.messages(t, ""); r.code == http.StatusOK || r.code >= http.StatusInternalServerError || strings.Contains(r.raw, "Fix the flaky") {
		t.Fatalf("another thread's rollout must be refused, not read or failed: %d %s", r.code, r.raw)
	}
}

// What a tool injects is dropped block by block, in whichever order it comes.
func TestMessages_InjectedBlocksAreDroppedNotTheWordsAroundThem(t *testing.T) {
	f := messagesFixture(t, `{"type":"user","timestamp":"2026-10-14T10:00:00Z","message":{"content":[{"type":"text","text":"<system-reminder>Today is a Wednesday</system-reminder>"},{"type":"text","text":"fix the bug"}]}}
{"type":"user","timestamp":"2026-10-14T10:01:00Z","message":{"content":[{"type":"text","text":"now the linter"},{"type":"text","text":"<system-reminder>use pnpm</system-reminder>"}]}}
{"type":"user","timestamp":"2026-10-14T10:02:00Z","message":{"content":"<command-name>/model</command-name>"}}
{"type":"user","timestamp":"2026-10-14T10:03:00Z","message":{"content":"[Request interrupted by user]"}}
{"type":"user","timestamp":"2026-10-14T10:04:00Z","message":{"content":"<user-prompt-submit-hook>blocked</user-prompt-submit-hook>"}}
`)
	got, r := f.messages(t, "id=1")
	if r.code != http.StatusOK || len(got.Items) != 1 || got.Items[0].Text != "fix the bug" {
		t.Fatalf("a reminder before the words = %s", r.raw)
	}
	got, r = f.messages(t, "id=2")
	if r.code != http.StatusOK || len(got.Items) != 1 || got.Items[0].Text != "now the linter" {
		t.Fatalf("a reminder after the words = %s", r.raw)
	}
	if all, _ := f.messages(t, ""); all.Total != 2 {
		t.Fatalf("slash commands, interruptions and hook output are messages: %+v", all)
	}
	if hit, _ := f.messages(t, "q=system-reminder"); hit.Total != 0 {
		t.Fatalf("an injected block is searchable: %+v", hit)
	}
}

// An empty rollout is pending while the tool runs and missing once it does not.
func TestMessages_AnEmptyCodexRolloutIsPendingThenMissing(t *testing.T) {
	f, file := newCodexActivityFixture(t)
	writeFile(t, file, `{"timestamp":"2026-10-13T12:00:00Z","type":"session_meta",`)
	if got, r := f.messages(t, ""); r.code != http.StatusOK || got.Transcript != activityTranscriptPending || got.Driver != "codex" {
		t.Fatalf("a header being written = %d %s", r.code, r.raw)
	}
	setRunColumn(t, f, colState, stateStopped)
	if got, _ := f.messages(t, ""); got.Transcript != activityTranscriptMissing {
		t.Fatalf("a stopped session's empty rollout = %q", got.Transcript)
	}
}
