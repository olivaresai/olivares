// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// The limits of a conversation are the vendor's, not picked by eye: one message, its
// images included, reaches the model in one request of at most 128 MB on the first-party
// Claude API (https://platform.claude.com/docs/en/api/overview#request-size-limits), and
// one request carries at most 100 images on every model
// (https://platform.claude.com/docs/en/build-with-claude/vision#request-limits).
const (
	vendorRequestBytes  = 128_000_000
	vendorRequestImages = 100
)

// A pasted log far over the old 1 MiB body reaches the tool whole; a message over what
// one request takes is refused with the limit named, not as an invalid body.
func TestAMessageAsLargeAsTheToolTakesIsSent(t *testing.T) {
	f := newCodexTextFixture(t, "limits-body", "thread-body")
	pasted := "build log\n" + strings.Repeat("x", 2<<20)
	if got := f.post(map[string]any{"text": pasted}); got.code != http.StatusAccepted {
		t.Fatalf("a 2 MiB message = %d %.200s, want it accepted", got.code, got.raw)
	}
	f.waitForInputs(t, 1)
	if got := f.inputs(t); got[0] != pasted {
		t.Fatalf("the tool received %d bytes, want the whole %d", len(got[0]), len(pasted))
	}
	over := f.post(map[string]any{"text": strings.Repeat("x", vendorRequestBytes)})
	if over.code != http.StatusRequestEntityTooLarge || !strings.Contains(errorMessageOf(over), "128 MB") {
		t.Fatalf("a message over one request = %d %.200s, want 413 naming 128 MB", over.code, over.raw)
	}
}

// One message carries as many images as one request does, and no more bytes of them
// than one request takes; each refusal names its limit.
func TestAMessageCarriesTheImagesOneRequestTakes(t *testing.T) {
	t.Parallel()
	m, _, tenant, _ := newRuntimeHarness(t, WithAttachmentRoot(t.TempDir()))
	dir, err := m.attachmentRunDir(tenant, "run-many")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := func(size int64) string {
		id := attachmentIDPrefix + string(model.NewID())
		path := filepath.Join(dir, id+".png")
		if err := writeAttachment(path, tinyPNGBytes(t)); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			// A sparse file: the size is what counts, not the disk it takes.
			if err := os.Truncate(path, size); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	var ids []string
	for range vendorRequestImages + 1 {
		ids = append(ids, store(0))
	}
	if got, err := m.loadTurnImages(tenant, "run-many", ids[:vendorRequestImages], 0); err != nil || len(got) != vendorRequestImages {
		t.Fatalf("%d images: %d loaded, err %v, want all of them", vendorRequestImages, len(got), err)
	}
	if _, err := m.loadTurnImages(tenant, "run-many", ids, 0); err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("101 images: err = %v, want a refusal naming 100", err)
	}
	// Thirty-five of the largest images are more base64 than one request takes.
	var big []string
	for range 35 {
		big = append(big, store(maxAttachmentBytes))
	}
	if _, err := m.loadTurnImages(tenant, "run-many", big, 0); err == nil || !strings.Contains(err.Error(), "128 MB") {
		t.Fatalf("35 images of 3.75 MiB: err = %v, want a refusal naming 128 MB", err)
	}
}

// A session keeps every image it can still send: an upload past what one request takes
// removes the session's oldest images, never refuses the new one.
func TestASessionKeepsItsNewestImagesThatOneRequestTakes(t *testing.T) {
	f := newCodexTextFixture(t, "limits-store", "thread-store")
	dir, err := f.m.attachmentRunDir(f.tenant, f.runRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Forty small images: more than the 32 a session kept before.
	for range 40 {
		if err := writeAttachment(filepath.Join(dir, attachmentIDPrefix+string(model.NewID())+".png"), tinyPNGBytes(t)); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.upload("image/png", tinyPNGBytes(t)); got.code != http.StatusCreated {
		t.Fatalf("the 41st small image = %d %s, want it stored", got.code, got.raw)
	}
	if n := f.storedFiles(t); n != 41 {
		t.Fatalf("%d images stored, want all 41", n)
	}
	// Thirty-three of the largest images, oldest first, hold more than one request.
	old := time.Now().Add(-time.Hour)
	var first, last string
	for i := range 33 {
		name := filepath.Join(dir, attachmentIDPrefix+string(model.NewID())+".png")
		if err := writeAttachment(name, tinyPNGBytes(t)); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(name, maxAttachmentBytes); err != nil {
			t.Fatal(err)
		}
		at := old.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(name, at, at); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = name
		}
		last = name
	}
	if got := f.upload("image/png", tinyPNGBytes(t)); got.code != http.StatusCreated {
		t.Fatalf("an image past one request = %d %s, want it stored", got.code, got.raw)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("the oldest image is still kept (%v): the session holds more than one request", err)
	}
	if _, err := os.Stat(last); err != nil {
		t.Fatalf("the newest large image was removed: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var size int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}
	if size > vendorRequestBytes {
		t.Fatalf("the session holds %d bytes of images, over one request", size)
	}
}

// A long session's transcript is read whole: its messages and its activity, past the
// 64 MiB the reads refused before (real Claude Code transcripts reach 172 MB).
func TestMessagesAndActivityReadALongTranscript(t *testing.T) {
	pad := `{"type":"system","subtype":"padding","content":"` + strings.Repeat("x", 1<<20) + `"}` + "\n"
	f := messagesFixture(t, messagesTranscript+strings.Repeat(pad, 66)+
		`{"type":"user","timestamp":"2026-10-16T09:00:00.000Z","message":{"content":"after a long day"}}`+"\n")
	got, r := f.messages(t, "q=after+a+long+day")
	if r.code != http.StatusOK || got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("messages of a 66 MiB transcript = %d %.300s, want the last message found", r.code, r.raw)
	}
	if _, r := f.activity(t); r.code != http.StatusOK {
		t.Fatalf("activity of a 66 MiB transcript = %d %.300s, want it read", r.code, r.raw)
	}
}

// One message read by id is the whole message, as long as the tool recorded it: a copy
// of it is never cut.
func TestOneMessageReadByIDIsWhole(t *testing.T) {
	long := strings.Repeat("y", 300<<10)
	f := messagesFixture(t, messagesTranscript+
		`{"type":"assistant","timestamp":"2026-10-15T09:00:05.000Z","message":{"content":[{"type":"text","text":"`+long+`"}]}}`+"\n")
	got, r := f.messages(t, "id=6")
	if r.code != http.StatusOK || len(got.Items) != 1 {
		t.Fatalf("a 300 KiB message read by id = %d %.300s", r.code, r.raw)
	}
	if got.Items[0].Text != long || got.Items[0].Clipped {
		t.Fatalf("a 300 KiB message read by id = %d bytes, clipped %v; want it whole", len(got.Items[0].Text), got.Items[0].Clipped)
	}
}

// A terminal request bigger than one chunk says how big a chunk is.
func TestTerminalInputOverOneChunkNamesItsSize(t *testing.T) {
	t.Parallel()
	err := (&terminal{}).send(context.Background(), strings.Repeat("x", 64<<10+1))
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("a request one byte over 64 KiB = %v, want a refusal naming 64 KiB", err)
	}
}
