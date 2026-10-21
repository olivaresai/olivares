// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// Images attached to a message (#2158), proved the way the person meets them: over the
// module's HTTP surface with its real authentication and store, against a real child
// process that records what it received.

// tinyPNG is a real 1x1 PNG.
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

func tinyPNGBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyPNG)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *codexTextFixture) upload(mediaType string, data []byte) resp {
	return f.h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+f.runRef+"/attachments", f.admin,
		map[string]any{"media_type": mediaType, "data": base64.StdEncoding.EncodeToString(data)}, tenantHdr(f.tenant))
}

func (f *codexTextFixture) images(t *testing.T) []string {
	t.Helper()
	return readFixtureRecord(t, f.record).Images
}

func (f *codexTextFixture) storedFiles(t *testing.T) int {
	t.Helper()
	dir, err := f.m.attachmentRunDir(f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	return len(entries)
}

// The picture reaches the tool as the tool's own image input, beside the sentence, and
// the bytes are the ones that were uploaded.
func TestAttachedImageReachesCodexAsAnImageInput(t *testing.T) {
	f := newCodexTextFixture(t, "attach-codex", "thread-attach")
	stored := f.upload("image/png", tinyPNGBytes(t))
	id, _ := stored.body["id"].(string)
	if stored.code != http.StatusCreated || !strings.HasPrefix(id, attachmentIDPrefix) || stored.body["media_type"] != "image/png" {
		t.Fatalf("upload = %d %s", stored.code, stored.raw)
	}
	sent := f.post(map[string]any{"text": "what is wrong here?", "attachments": []string{id}})
	if sent.code != http.StatusAccepted || sent.body["accepted"] != true {
		t.Fatalf("message with an image = %d %s", sent.code, sent.raw)
	}
	f.waitForInputs(t, 1)
	if got := f.inputs(t); got[0] != "what is wrong here?" {
		t.Fatalf("the child received the sentence %q", got[0])
	}
	want := "data:image/png;base64," + tinyPNG
	if got := f.images(t); len(got) != 1 || got[0] != want {
		t.Fatalf("the child received images %q, want the uploaded picture %q", got, want)
	}
}

// What the engine cannot take is refused when the image is attached, before a message
// exists, and nothing is stored.
func TestAttachmentRefusalsHappenBeforeAnythingIsSent(t *testing.T) {
	f := newCodexTextFixture(t, "attach-refuse", "thread-refuse")
	gif := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	for _, tc := range []struct {
		name      string
		mediaType string
		data      []byte
		code      int
		message   string
	}{
		{"text claiming to be a PNG", "image/png", []byte("not an image at all"), http.StatusUnsupportedMediaType, "the file is not a PNG image"},
		{"a type no tool takes", "application/pdf", tinyPNGBytes(t), http.StatusUnsupportedMediaType, "attach a PNG, JPEG, WebP or GIF image"},
		{"a GIF declared as a PNG", "image/png", gif, http.StatusUnsupportedMediaType, "the file is not a PNG image"},
		{"over the limit", "image/png", append(tinyPNGBytes(t), make([]byte, maxAttachmentBytes)...), http.StatusRequestEntityTooLarge, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f.upload(tc.mediaType, tc.data)
			if got.code != tc.code {
				t.Fatalf("status = %d %s, want %d", got.code, got.raw, tc.code)
			}
			msg := errorMessageOf(got)
			if tc.code == http.StatusRequestEntityTooLarge {
				if !strings.Contains(msg, "the limit is 3.8 MB") {
					t.Fatalf("size refusal = %q, want a sentence with the limit", msg)
				}
			} else if msg != tc.message {
				t.Fatalf("refusal = %q, want %q", msg, tc.message)
			}
		})
	}
	bad := f.h.doJSON(http.MethodPost, "/v1/m/sessions/runs/"+f.runRef+"/attachments", f.admin,
		map[string]any{"media_type": "image/png", "data": "%%%"}, tenantHdr(f.tenant))
	if bad.code != http.StatusBadRequest {
		t.Fatalf("invalid base64 = %d %s, want 400", bad.code, bad.raw)
	}
	if n := f.storedFiles(t); n != 0 {
		t.Fatalf("%d files were stored for refused uploads", n)
	}
	// An id nobody stored is refused by the message too, and nothing reaches the tool.
	missing := f.post(map[string]any{"text": "look", "attachments": []string{attachmentIDPrefix + string(model.NewID())}})
	if missing.code != http.StatusNotFound {
		t.Fatalf("message naming an unknown image = %d %s, want 404", missing.code, missing.raw)
	}
	// Images never ride with a raw frame or a work fence.
	for name, body := range map[string]map[string]any{
		"raw line":   {"line": "{}", "attachments": []string{"att_x"}},
		"work fence": {"text": "look", "attachments": []string{"att_x"}, "work_lease_fence": 1},
	} {
		if got := f.post(body); got.code != http.StatusBadRequest {
			t.Fatalf("%s with an image = %d %s, want 400", name, got.code, got.raw)
		}
	}
	if got := f.inputs(t); len(got) != 0 {
		t.Fatalf("a refused message reached the tool: %q", got)
	}
}

// The stored image is read only through its own session, by the tenant that owns it.
func TestAttachmentIsReadableOnlyInsideItsTenantAndSession(t *testing.T) {
	f := newCodexTextFixture(t, "attach-iso", "thread-iso")
	png := tinyPNGBytes(t)
	id, _ := f.upload("image/png", png).body["id"].(string)
	path := "/v1/m/sessions/runs/" + f.runRef + "/attachments/" + id

	own := f.h.do(http.MethodGet, path, f.admin, tenantHdr(f.tenant))
	if own.code != http.StatusOK || own.raw != string(png) {
		t.Fatalf("the owner reads %d with %d bytes, want the %d bytes uploaded", own.code, len(own.raw), len(png))
	}
	if ct := own.header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if own.header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(own.header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("the image is served without nosniff and a sandbox: %v", own.header)
	}

	other := f.h.createOrg(f.admin, "attach-other")
	foreign := f.h.do(http.MethodGet, path, f.admin, tenantHdr(other))
	if foreign.code != http.StatusNotFound && foreign.code != http.StatusForbidden {
		t.Fatalf("another tenant reads %d %s, want 403/404", foreign.code, foreign.raw)
	}
	if strings.Contains(foreign.raw, string(png)) {
		t.Fatal("another tenant received the image bytes")
	}
	// The same session and id asked for by another tenant: the tenant is part of where
	// the image is kept, so it is not found.
	if _, err := f.m.readAttachment(other, f.runRef, id); err == nil {
		t.Fatal("another tenant read the image through the same session reference")
	}
	// The same id under another session of the same tenant, and names that climb out.
	if _, err := f.m.readAttachment(f.tenant, string(model.NewID()), id); err == nil {
		t.Fatal("an image was read through a session that does not hold it")
	}
	for _, bad := range []string{"..%2F..%2Fetc%2Fpasswd", "att_..", attachmentIDPrefix, "x"} {
		got := f.h.do(http.MethodGet, "/v1/m/sessions/runs/"+f.runRef+"/attachments/"+bad, f.admin, tenantHdr(f.tenant))
		if got.code != http.StatusNotFound && got.code != http.StatusBadRequest {
			t.Fatalf("attachment %q = %d, want 404", bad, got.code)
		}
	}
	// Under its tenant in the attachment root, outside every folder the session works in.
	dir, err := f.m.attachmentRunDir(f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(f.m.rt.attachmentRoot, dir)
	if err != nil || !strings.HasPrefix(rel, f.tenant.String()+string(filepath.Separator)) {
		t.Fatalf("the image is kept at %q, not under its tenant in the attachment root", dir)
	}
}

// Deleting the session deletes what was attached to it.
func TestRemovingARunRemovesItsAttachments(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithAttachmentRoot(t.TempDir()))
	dir, err := m.attachmentRunDir(tenant, "run-to-delete")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeAttachment(filepath.Join(dir, attachmentIDPrefix+string(model.NewID())+".png"), tinyPNGBytes(t)); err != nil {
		t.Fatal(err)
	}
	m.removeRunAttachments(tenant, "run-to-delete")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the run's attachments remain after removal: %v", err)
	}
}

// Without a root there is nowhere safe to keep an image: attaching is refused (503),
// not parked in a guessed directory.
func TestAttachingWithoutARootIsRefused(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	if _, err := m.attachmentRunDir(tenant, "run"); err == nil || !strings.Contains(err.Error(), "not available on this node") {
		t.Fatalf("no root: err = %v, want the not-available refusal", err)
	}
}

// Claude Code takes the picture as an image content block ahead of the sentence, in the
// one user-message line, and the bytes are the stored ones.
func TestClaudeTurnWithImagesIsOneUserMessageWithImageBlocks(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{initSID: "sess-attach"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	err = m.sendImageInput(ctx, tenant, dto.RunRef, "what is wrong here?", []turnImage{
		{ID: attachmentIDPrefix + string(model.NewID()), MediaType: "image/png", Data: tinyPNGBytes(t)},
	})
	if err != nil {
		t.Fatalf("image to a Claude session: %v", err)
	}
	proc := fr.lastProc()
	waitFor(t, "the turn reached the child", func() bool { return proc.sentCount() == 1 })
	proc.mu.Lock()
	line := proc.sent[0]
	proc.mu.Unlock()
	if strings.ContainsAny(string(line), "\r\n") {
		t.Fatalf("the message became more than one stdin line: %q", line)
	}
	var frame struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("sent line is not one JSON frame: %v\n%.200s", err, line)
	}
	c := frame.Message.Content
	if frame.Type != "user" || frame.Message.Role != "user" || len(c) != 2 {
		t.Fatalf("frame = %.300s, want a user message with an image block and a text block", line)
	}
	if c[0].Type != "image" || c[0].Source.Type != "base64" || c[0].Source.MediaType != "image/png" || c[0].Source.Data != tinyPNG {
		t.Fatalf("first block = %+v, want the uploaded PNG as a base64 image block", c[0])
	}
	if c[1].Type != "text" || c[1].Text != "what is wrong here?" {
		t.Fatalf("second block = %+v, want the sentence", c[1])
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	lr.imageEcho.mu.Lock()
	waiting := len(lr.imageEcho.pending)
	lr.imageEcho.mu.Unlock()
	if waiting != 1 {
		t.Fatalf("%d turns wait for their echo, want 1", waiting)
	}
}

// The tool echoes the whole message, picture included. The engine does not carry that
// echo: the bridge swaps it for a compact frame that names the stored image.
func TestImageEchoIsReplacedByAFrameThatNamesTheStoredImage(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{initSID: "sess-attach-echo"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	id := attachmentIDPrefix + string(model.NewID())
	if err := m.sendImageInput(ctx, tenant, dto.RunRef, "look", []turnImage{{ID: id, MediaType: "image/png", Data: tinyPNGBytes(t)}}); err != nil {
		t.Fatal(err)
	}
	// What Claude Code 2.1.299 writes for that message (measured against a stub API).
	echo := `{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNG + `"}},{"type":"text","text":"look"}]},"session_id":"s","parent_tool_use_id":null,"uuid":"u","timestamp":"t","isReplay":true}`
	fr.lastProc().out <- OutputFrame{Stream: streamStdout, Data: []byte(echo)}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	var compact string
	waitFor(t, "the echo reached the stream", func() bool {
		for _, f := range lr.ring.readFrom(0).frames {
			if strings.Contains(string(f.Data), `"attachments"`) {
				compact = string(f.Data)
				return true
			}
		}
		return false
	})
	if strings.Contains(compact, tinyPNG) || !strings.Contains(compact, id) || !strings.Contains(compact, `"text":"look"`) {
		t.Fatalf("compact frame = %s, want the text and the attachment id and no picture", compact)
	}
	for _, f := range lr.ring.readFrom(0).frames {
		if strings.Contains(string(f.Data), tinyPNG) {
			t.Fatalf("the picture itself is in the output stream: %.200s", f.Data)
		}
	}
	lr.imageEcho.mu.Lock()
	waiting := len(lr.imageEcho.pending)
	lr.imageEcho.mu.Unlock()
	if waiting != 0 {
		t.Fatalf("%d turns still wait for an echo that arrived", waiting)
	}
}

// A message the tool did not take leaves nothing waiting for an echo.
func TestRefusedImageTurnIsNotWaitedFor(t *testing.T) {
	var e imageEcho
	turn := e.queue("look", []turnImage{{ID: "att_1", MediaType: "image/png"}})
	e.drop(turn)
	if _, handled := e.replace([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{}}]},"isReplay":true}`)); handled {
		t.Fatal("a dropped turn was matched to the next echo")
	}
}

// A tool whose protocol has no image input refuses the message and names itself.
func TestToolWithoutImageInputRefusesAndNamesTheTool(t *testing.T) {
	fx := newFakeTurnFixture(t)
	attempted, err := fx.m.driverInputImages(context.Background(), fx.live, "look",
		[]turnImage{{ID: "att_1", MediaType: "image/png", Data: tinyPNGBytes(t)}})
	if attempted {
		t.Fatal("a refused image crossed to the tool")
	}
	if err == nil || !strings.Contains(err.Error(), fakeTurnDriverKey+" does not take images") {
		t.Fatalf("err = %v, want a refusal that names %s", err, fakeTurnDriverKey)
	}
	rec := model.Record{colTransport: string(TransportStreamJSON)}
	for driver, want := range map[string]string{
		providerDriverGrok: "Grok Build does not take images", providerDriverOpenCode: "OpenCode does not take images",
		providerDriverGemini: "Gemini CLI does not take images", providerDriverClaude: "", providerDriverCodex: "",
	} {
		rec[colRunProfileDriver] = driver
		if got := imageRefusal(rec); got != want {
			t.Fatalf("imageRefusal(%s) = %q, want %q", driver, got, want)
		}
	}
}

// Over-long lines stay terminal, except the echo of a message with a picture, and even
// that one only up to a ceiling.
func TestOversizedImageEchoIsReplacedAndAnyOtherLongLineIsNot(t *testing.T) {
	big := strings.Repeat("A", 2*maxOutputLine)
	claude := `{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + big + `"}}]},"isReplay":true}`
	codexDone := `{"method":"item/completed","params":{"item":{"type":"userMessage","content":[{"type":"image","url":"data:image/png;base64,` + big + `"}]}}}`
	codexStart := `{"method":"item/started","params":{"item":{"type":"userMessage","content":[{"type":"image","url":"data:image/png;base64,` + big + `"}]}}}`
	other := `{"type":"assistant","message":"` + big + `"}`
	// A screenshot a tool read is a user line too, and it is not the person's message.
	toolResult := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"data":"` + big + `"}}]}]}}`
	runaway := claude[:len(claude)-3] + strings.Repeat("A", maxOverflowDrain)
	for name, tc := range map[string]struct {
		line string
		want []byte
	}{
		"claude echo": {claude, imageEchoOversize}, "codex started": {codexStart, codexStartedOversize},
		"codex completed":     {codexDone, codexCompletedOversize},
		"any other long line": {other, nil}, "an oversized tool result": {toolResult, nil},
		"an echo that never ends": {runaway, nil},
	} {
		line, err := readBoundedLineWithOverflow(bufio.NewReader(strings.NewReader(tc.line+"\n"+`{"type":"result"}`+"\n")), maxOutputLine, oversizedImageEcho)
		if (err != nil) != (tc.want == nil) {
			t.Fatalf("%s: err = %v, want an error exactly when no replacement applies", name, err)
		}
		if tc.want != nil && string(line) != string(tc.want) {
			t.Fatalf("%s: line = %.80s, want %s", name, line, tc.want)
		}
	}
}

// Codex reports one message twice. Both reports are one turn: the second never takes
// the next message's pictures, and the person's message shows once.
func TestCodexReportsOfOneMessageAreOneTurn(t *testing.T) {
	var e imageEcho
	e.queue("first", []turnImage{{ID: "att_a", MediaType: "image/png"}})
	e.queue("second", []turnImage{{ID: "att_b", MediaType: "image/webp"}})
	item := func(method string) []byte {
		return []byte(`{"method":"` + method + `","params":{"item":{"type":"userMessage","content":[{"type":"image","url":"data:image/png;base64,AAAA"}]}}}`)
	}
	if got, handled := e.replace(item("item/started")); !handled || got != nil {
		t.Fatalf("first report = %q handled=%v, want it dropped", got, handled)
	}
	got, handled := e.replace(item("item/completed"))
	if !handled || !strings.Contains(string(got), `"att_a"`) || strings.Contains(string(got), "AAAA") || !strings.Contains(string(got), `"first"`) {
		t.Fatalf("second report = %s, want the first message's compact frame", got)
	}
	// The next message is the second turn, whichever report arrives first.
	got, handled = e.replace(item("item/completed"))
	if !handled || !strings.Contains(string(got), `"att_b"`) {
		t.Fatalf("next message = %s, want the second turn", got)
	}
	// A response that lists userMessage items (thread/read) is no echo and is left alone.
	if _, handled := e.replace([]byte(`{"id":3,"result":{"thread":{"turns":[{"items":[{"type":"userMessage","content":[{"type":"image","url":"data:image/png;base64,AAAA"}]}]}]}}}`)); handled {
		t.Fatal("a thread/read response was taken for an echo")
	}
}

// The limits are said in a sentence, whichever way the image is too big, and a session
// keeps a bounded number of images.
func TestUploadLimitsAreSaidAndKept(t *testing.T) {
	f := newCodexTextFixture(t, "attach-limits", "thread-limits")
	// Far over the limit: the request is refused whole, with the limit named.
	huge := f.upload("image/png", append(tinyPNGBytes(t), make([]byte, 2*maxAttachmentBytes)...))
	if huge.code != http.StatusRequestEntityTooLarge || !strings.Contains(errorMessageOf(huge), "3.8 MB") {
		t.Fatalf("a huge image = %d %s, want 413 naming the 3.8 MB limit", huge.code, huge.raw)
	}
	// A session that holds as many images as it keeps takes no more.
	dir, err := f.m.attachmentRunDir(f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxStoredAttachments; i++ {
		if err := os.WriteFile(filepath.Join(dir, attachmentIDPrefix+string(model.NewID())+".png"), tinyPNGBytes(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	full := f.upload("image/png", tinyPNGBytes(t))
	if full.code != http.StatusInsufficientStorage || !strings.Contains(errorMessageOf(full), "as many images as it keeps") {
		t.Fatalf("a full session = %d %s, want 507 and a sentence", full.code, full.raw)
	}
	if n := f.storedFiles(t); n != maxStoredAttachments {
		t.Fatalf("%d images stored, want the %d it already held", n, maxStoredAttachments)
	}
	// Too many uploads at once on a node: the caller is told to retry.
	for i := 0; i < attachmentUploadSlots; i++ {
		attachmentUploads <- struct{}{}
	}
	busy := f.upload("image/png", tinyPNGBytes(t))
	for i := 0; i < attachmentUploadSlots; i++ {
		<-attachmentUploads
	}
	if busy.code != http.StatusTooManyRequests || busy.header.Get("Retry-After") == "" {
		t.Fatalf("a busy node = %d %v, want 429 with Retry-After", busy.code, busy.header)
	}
}

// A real screenshot's echo is far over the one-line limit. Through the real pump the
// session survives it, and the next frame is the tool's own.
func TestOversizedImageEchoDoesNotEndTheSessionThroughThePump(t *testing.T) {
	t.Parallel()
	script := filepath.Join(t.TempDir(), "echo.sh")
	body := "#!/bin/sh\n" +
		`printf '%s' '{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"'` + "\n" +
		"head -c 1500000 /dev/zero | tr '\\0' A\n" +
		`printf '%s\n' '"}},{"type":"text","text":"look"}]},"isReplay":true}'` + "\n" +
		`printf '%s\n' '{"type":"result","subtype":"success"}'` + "\n" +
		"sleep 5\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proc, err := NewProcRunner().Launch(ctx, LaunchSpec{Program: script, Dir: t.TempDir(), WaitDelay: 2 * time.Second})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	defer func() { _ = proc.Stop(context.Background()) }()
	echo := recvFrame(t, proc, 5*time.Second)
	if string(echo.Data) != string(imageEchoOversize) {
		t.Fatalf("first frame = %.120s, want the oversize marker in place of a 1.5 MB echo", echo.Data)
	}
	if next := recvFrame(t, proc, 5*time.Second); !strings.Contains(string(next.Data), `"type":"result"`) {
		t.Fatalf("next frame = %.120s, want the tool's own result: the session must outlive the echo", next.Data)
	}
}

// A write the tool refuses leaves no turn waiting for an echo, so the next message
// shows its own pictures.
func TestRefusedWriteLeavesNoTurnWaitingAndTheNextMessageKeepsItsOwnImages(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{initSID: "sess-attach-refused"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	lr, _ := m.rt.getLive(tenant, dto.RunRef)
	proc := fr.lastProc()
	proc.mu.Lock()
	proc.done = true // the child's stdin no longer takes a line
	proc.mu.Unlock()
	if err := m.sendImageInput(ctx, tenant, dto.RunRef, "lost", []turnImage{{ID: "att_lost", MediaType: "image/png", Data: tinyPNGBytes(t)}}); err == nil {
		t.Fatal("a write the tool refused was reported as sent")
	}
	lr.imageEcho.mu.Lock()
	waiting := len(lr.imageEcho.pending)
	lr.imageEcho.mu.Unlock()
	if waiting != 0 {
		t.Fatalf("%d turns wait for the echo of a message that was never taken", waiting)
	}
	proc.mu.Lock()
	proc.done = false
	proc.mu.Unlock()
	// The engine counts the refused write as an open turn (it may have crossed); the
	// tool's result closes it, as in the other turn tests.
	m.onStdout(ctx, lr, []byte(resultFrame), m.now())
	if err := m.sendImageInput(ctx, tenant, dto.RunRef, "kept", []turnImage{{ID: "att_kept", MediaType: "image/png", Data: tinyPNGBytes(t)}}); err != nil {
		t.Fatal(err)
	}
	compact, handled := lr.imageEcho.replace([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"image","source":{}}]},"isReplay":true}`))
	if !handled || !strings.Contains(string(compact), "att_kept") || strings.Contains(string(compact), "att_lost") {
		t.Fatalf("echo = %s, want the second message's own image", compact)
	}
}

// The shape of a message: images alone are a message, a message holds at most four, and
// the same image twice, or one that is not this session's, is refused.
func TestMessageShapeOfAttachments(t *testing.T) {
	rec := model.Record{colTransport: string(TransportStreamJSON)}
	line, ok := claudeTextTurn(rec, "  ", []turnImage{{ID: "att_1", MediaType: "image/png", Data: []byte{1}}})
	if !ok {
		t.Fatal("a Claude run takes an image with no words")
	}
	var frame struct {
		Message struct {
			Content []struct{ Type string } `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &frame); err != nil || len(frame.Message.Content) != 1 || frame.Message.Content[0].Type != "image" {
		t.Fatalf("image-only message = %s, want one image block and no empty text block", line)
	}

	m, _, tenant, _ := newRuntimeHarness(t, WithAttachmentRoot(t.TempDir()))
	dir, err := m.attachmentRunDir(tenant, "run-shape")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i <= maxTurnAttachments; i++ {
		id := attachmentIDPrefix + string(model.NewID())
		if err := writeAttachment(filepath.Join(dir, id+".png"), tinyPNGBytes(t)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := m.loadTurnImages(tenant, "run-shape", ids); err == nil || !strings.Contains(err.Error(), "at most 4") {
		t.Fatalf("five images: err = %v, want a refusal that names the limit", err)
	}
	if _, err := m.loadTurnImages(tenant, "run-shape", []string{ids[0], ids[0]}); err == nil {
		t.Fatal("the same image twice was accepted")
	}
	if _, err := m.loadTurnImages(tenant, "another-run", ids[:1]); err == nil {
		t.Fatal("an image of another session was accepted")
	}
	if got, err := m.loadTurnImages(tenant, "run-shape", ids[:2]); err != nil || len(got) != 2 || got[0].ID != ids[0] || got[1].ID != ids[1] {
		t.Fatalf("two images: %v %v, want them in the order given", got, err)
	}
}

// Deleting a session through the module removes what was attached to it.
func TestDeleteRunRemovesItsAttachments(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{initSID: "sess-attach-delete"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithAttachmentRoot(t.TempDir()))
	ctx := context.Background()
	dto, err := createProfiledTestRun(t, m, ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, PermissionMode: "default", Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, t.TempDir()), Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := m.attachmentRunDir(tenant, dto.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeAttachment(filepath.Join(dir, attachmentIDPrefix+string(model.NewID())+".png"), tinyPNGBytes(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.cleanupRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatal(err)
	}
	if err := m.deleteRun(ctx, tenant, dto.RunRef); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the deleted session's images remain: %v", err)
	}
}

// Claude Code over the module's HTTP surface: the image is uploaded, the message names
// it by id, and the child's stdin gets one user message with the image block first.
func TestClaudeMessageWithAnImageOverHTTP(t *testing.T) {
	fr := &fakeRunner{initSID: "sess-http-attach"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithAttachmentRoot(t.TempDir()),
		WithRunner(fr), WithCredentialSource(staticCred()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "attach-claude-http")
	created := h.doJSON(http.MethodPost, "/v1/m/sessions/runs", admin, map[string]any{
		"transport": "stream-json", "permission_mode": "default", "isolation": "native",
		"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant),
	}, tenantHdr(tenant))
	runRef, _ := created.body["run_ref"].(string)
	if created.code != http.StatusCreated || runRef == "" {
		t.Fatalf("create run = %d %s", created.code, created.raw)
	}
	base := "/v1/m/sessions/runs/" + runRef
	stored := h.doJSON(http.MethodPost, base+"/attachments", admin,
		map[string]any{"media_type": "image/png", "data": tinyPNG}, tenantHdr(tenant))
	id, _ := stored.body["id"].(string)
	if stored.code != http.StatusCreated || id == "" {
		t.Fatalf("upload = %d %s", stored.code, stored.raw)
	}
	sent := h.doJSON(http.MethodPost, base+"/input", admin,
		map[string]any{"text": "what is wrong here?", "attachments": []string{id}}, tenantHdr(tenant))
	if sent.code != http.StatusAccepted || sent.body["accepted"] != true {
		t.Fatalf("message with an image = %d %s", sent.code, sent.raw)
	}
	proc := fr.lastProc()
	waitFor(t, "the message reached the child", func() bool { return proc.sentCount() == 1 })
	proc.mu.Lock()
	line := proc.sent[0]
	proc.mu.Unlock()
	var frame struct {
		Message struct {
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Data string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &frame); err != nil {
		t.Fatalf("stdin line is not one JSON frame: %v", err)
	}
	c := frame.Message.Content
	if len(c) != 2 || c[0].Type != "image" || c[0].Source.Data != tinyPNG || c[1].Type != "text" || c[1].Text != "what is wrong here?" {
		t.Fatalf("stdin content = %+v, want the stored image then the sentence", c)
	}
	// An id this session never stored is not found, and nothing more reaches the child.
	missing := h.doJSON(http.MethodPost, base+"/input", admin,
		map[string]any{"text": "again", "attachments": []string{attachmentIDPrefix + string(model.NewID())}}, tenantHdr(tenant))
	if missing.code != http.StatusNotFound || proc.sentCount() != 1 {
		t.Fatalf("unknown image = %d, %d lines at the child; want 404 and 1", missing.code, proc.sentCount())
	}
}
