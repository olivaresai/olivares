// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"io"
	"net/http"
	"time"
)

// heartbeatInterval keeps an idle SSE connection alive and lets the server
// detect a dead peer (a failed write returns and ends the stream).
const heartbeatInterval = 25 * time.Second

// streamWriteTimeout bounds a single SSE write so a stalled client cannot pin
// the goroutine indefinitely.
const streamWriteTimeout = 30 * time.Second

// writeFrame arms a finite per-write deadline, writes the frame and flushes it.
func writeFrame(rc *http.ResponseController, w io.Writer, frame string) error {
	_ = rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	if _, err := io.WriteString(w, frame); err != nil {
		return err
	}
	return rc.Flush()
}
