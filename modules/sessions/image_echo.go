// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/json"
	"sync"
)

// A tool that is sent an image echoes the whole message back, image included: Claude
// Code re-emits the user message with its base64 (--replay-user-messages, measured on
// 2.1.299 with a stub API) and Codex reports it as a userMessage item with a data URL.
// Carried through the output stream, that echo would
//   - end the session when it is over the one-line limit (maxOutputLine, 1 MiB: the
//     pump treats a longer line as a runaway and kills the process group), and
//   - push the image into the ring, the recorder and every attach client, which keep
//     frames and not files.
//
// So the engine does not carry it. The pump replaces an over-long echo with
// imageEchoOversize (it cannot read more than its limit), and the bridge replaces
// either form with one compact frame that names the stored attachments instead of
// holding them; the console shows the thumbnail from the attachment route.
//
// The turns waiting for their echo are kept in the order they were sent, and the tool
// echoes in that order. A turn the tool refused is dropped by the sender, so the
// queue is exact for every message the tool accepted.
//
// ponytail: a message the tool accepted but never echoes (it failed before replaying
// it) leaves its turn at the head, and the next echo wears its images. Match on the
// turn's uuid (Claude keeps it in a replay) if that is ever seen.
type imageEcho struct {
	mu      sync.Mutex
	pending []*imageTurn
	// item is the turn a Codex userMessage item has taken: the item is reported twice
	// (started, completed), and both reports are one message.
	item *imageTurn
}

type imageTurn struct {
	text   string
	images []attachmentRef
}

// The two shapes of the echo. Claude Code writes the message it was sent, and the
// engine always puts the images first, so the echo opens with an image block. A
// tool_result that holds a screenshot also begins `{"type":"user"` but never with an
// image block, so it is not mistaken for an echo. Codex reports the message as a
// userMessage item of an item/started and an item/completed notification; a
// thread/read response, which also lists userMessage items, is not a notification.
var (
	claudeEchoHead     = []byte(`{"type":"user","message":{"role":"user","content":[{"type":"image"`)
	codexStartedHead   = []byte(`{"method":"item/started"`)
	codexCompletedHead = []byte(`{"method":"item/completed"`)
)

// imageEchoOversize is what the pump leaves in place of an echo it may not carry.
var imageEchoOversize = []byte(`{"type":"user","isReplay":true,"olivares_oversize":"image"}`)

// Codex's two reports, as the pump leaves them when they are over the limit.
var (
	codexStartedOversize   = []byte(`{"method":"item/started","params":{"olivares_oversize":"image"}}`)
	codexCompletedOversize = []byte(`{"method":"item/completed","params":{"olivares_oversize":"image"}}`)
)

func isCodexImageItem(head []byte) bool {
	return bytes.Contains(head, []byte(`"userMessage"`)) && bytes.Contains(head, []byte(";base64,"))
}

// oversizedImageEcho recognizes, from the first bytes of a line over the limit, the
// echo of a message that carried an image. Any other over-long line stays terminal.
func oversizedImageEcho(prefix []byte) []byte {
	head := prefix[:min(len(prefix), 4<<10)]
	switch {
	case bytes.HasPrefix(head, claudeEchoHead):
		return imageEchoOversize
	case bytes.HasPrefix(head, codexStartedHead) && isCodexImageItem(head):
		return codexStartedOversize
	case bytes.HasPrefix(head, codexCompletedHead) && isCodexImageItem(head):
		return codexCompletedOversize
	}
	return nil
}

// queue notes a message with images that is about to be written to the tool.
func (e *imageEcho) queue(text string, images []turnImage) *imageTurn {
	t := &imageTurn{text: text}
	for _, img := range images {
		t.images = append(t.images, img.ref())
	}
	e.mu.Lock()
	e.pending = append(e.pending, t)
	e.mu.Unlock()
	return t
}

// drop forgets a turn the tool did not accept. A write whose outcome is unknown is
// dropped too: if the tool did take it, only that one message shows its echo as the
// tool wrote it, where a turn kept for an echo that never comes would give every later
// message the wrong pictures.
func (e *imageEcho) drop(t *imageTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, q := range e.pending {
		if q == t {
			e.pending = append(e.pending[:i], e.pending[i+1:]...)
			return
		}
	}
}

// replace decides what the stream gets for one tool frame. handled is false for any
// frame that is not the echo of a message with an image, and for an echo with no turn
// waiting: it passes as the tool wrote it. A handled frame is replaced by compact, or
// by nothing (compact nil) when it is the first of Codex's two reports of the message.
func (e *imageEcho) replace(frame []byte) (compact []byte, handled bool) {
	// Every stdout frame of every run passes here; almost none has an image turn waiting.
	e.mu.Lock()
	idle := len(e.pending) == 0 && e.item == nil
	e.mu.Unlock()
	if idle {
		return nil, false
	}
	var started, completed bool
	switch {
	case bytes.HasPrefix(frame, claudeEchoHead) ||
		(bytes.HasPrefix(frame, []byte(`{"type":"user"`)) && bytes.Contains(frame, []byte(`"olivares_oversize":"image"`))):
		completed = true
	case bytes.HasPrefix(frame, codexStartedHead):
		started = isCodexImageItem(frame) || bytes.Contains(frame, []byte(`"olivares_oversize":"image"`))
	case bytes.HasPrefix(frame, codexCompletedHead):
		completed = isCodexImageItem(frame) || bytes.Contains(frame, []byte(`"olivares_oversize":"image"`))
	}
	if !started && !completed {
		return nil, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.item
	if t == nil {
		if len(e.pending) == 0 {
			return nil, false
		}
		t, e.pending = e.pending[0], e.pending[1:]
	}
	if started {
		e.item = t
		return nil, true
	}
	e.item = nil
	return compactImageEcho(t), true
}

// compactImageEcho is the user message as the console reads one, with the images by
// reference.
func compactImageEcho(t *imageTurn) []byte {
	content := []map[string]string{}
	if t.text != "" {
		content = append(content, map[string]string{"type": "text", "text": t.text})
	}
	b, err := json.Marshal(map[string]any{
		"type":        "user",
		"isReplay":    true,
		"message":     map[string]any{"role": "user", "content": content},
		"attachments": t.images,
	})
	if err != nil {
		return imageEchoOversize
	}
	return b
}
