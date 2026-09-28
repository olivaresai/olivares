// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package helperclient is the caller's side of the appliance's privileged-helper seam, used by
// the Appliance Console and the repair console on tty1.
//
// A call sends one closed document to one helper's socket, /run/olivares-helpers/<name>.sock,
// and reads its one answer. The helper admits the caller from the connection, so the client
// sends nothing about who it is. An absent helper is 503 consumer_unavailable and asks nothing:
// the client has no fallback, not another helper, not another path, not an act of its own, and
// it never reports an act it did not see answered.
package helperclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// maxAnswerBytes bounds a helper's answer: a support bundle and its envelope.
const maxAnswerBytes = 68 * 1024

// Codes of the client's own answers, beside the helpers' refusal codes.
const (
	// CodeOutcomeUnknown: the document was sent but no answer could be read, so whether the
	// helper performed is unknown; it is neither performed nor failed.
	CodeOutcomeUnknown = "outcome_unknown"
)

// Client reaches the helpers on their sockets. Its zero value is the installed appliance's.
type Client struct {
	// Dir is the directory of the helpers' sockets; empty means helperschema.SocketDir.
	Dir string
	// Owner is the uid that must own a helper's socket: root (0) on an installed appliance,
	// where the service manager creates the sockets.
	Owner uint32
}

// Error is a call that got no answer from the helper.
type Error struct {
	Helper string
	// Status is the HTTP status the console answers for this call.
	Status int
	Code   string
	// Reason is fixed text; it never repeats a value of the request.
	Reason string
}

func (e *Error) Error() string { return e.Helper + " helper: " + e.Code + ": " + e.Reason }

// StatusOf returns the HTTP status for a call's error: 200 for none, the Error's own status,
// and 500 for anything else.
func StatusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusInternalServerError
}

func unavailable(helper, reason string) *Error {
	return &Error{Helper: helper, Status: http.StatusServiceUnavailable, Code: helperschema.CodeConsumerUnavailable, Reason: reason}
}

func unknownOutcome(helper, reason string) *Error {
	return &Error{Helper: helper, Status: http.StatusBadGateway, Code: CodeOutcomeUnknown, Reason: reason}
}

// Call sends request to the helper name and returns its answer. The helper must be one the seam
// knows, a root helper or a module helper (helperschema.ClassOf), and the request valid under its
// schema. The socket must be a socket, not a symbolic link,
// owned by c.Owner; an absent, foreign or unanswering socket asks nothing and is 503
// consumer_unavailable. Once the document is sent, an answer that cannot be read is 502
// outcome_unknown. The answer's own result (performed, answered, refused, failed) is the
// helper's to give and is returned as it came.
func (c Client) Call(ctx context.Context, name string, request helperschema.Request) (helperschema.Response, error) {
	if _, known := helperschema.ClassOf(name); !known {
		return helperschema.Response{}, &Error{Helper: "unknown", Status: http.StatusUnprocessableEntity,
			Code: helperschema.CodeInputRefused, Reason: "not a helper of the seam"}
	}
	if err := request.Validate(); err != nil {
		return helperschema.Response{}, &Error{Helper: name, Status: http.StatusUnprocessableEntity,
			Code: helperschema.CodeInputRefused, Reason: err.Error()}
	}
	document, err := json.Marshal(request)
	if err != nil || len(document) > helperschema.MaxDocument {
		return helperschema.Response{}, &Error{Helper: name, Status: http.StatusUnprocessableEntity,
			Code: helperschema.CodeInputRefused, Reason: "the request does not fit its schema's bound"}
	}
	dir := c.Dir
	if dir == "" {
		dir = helperschema.SocketDir
	}
	path := filepath.Join(dir, name+".sock")
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return helperschema.Response{}, unavailable(name, "the helper's socket is absent")
	case err != nil:
		return helperschema.Response{}, unavailable(name, "the helper's socket cannot be inspected")
	case info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0:
		return helperschema.Response{}, unavailable(name, "the path is not the helper's socket")
	case !ownedBy(info, c.Owner):
		return helperschema.Response{}, unavailable(name, "the socket is not the service manager's")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return helperschema.Response{}, unavailable(name, "the helper does not answer on its socket")
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(document); err != nil {
		return helperschema.Response{}, unknownOutcome(name, "the request could not be sent whole")
	}
	if unix, ok := conn.(*net.UnixConn); ok {
		if err := unix.CloseWrite(); err != nil {
			return helperschema.Response{}, unknownOutcome(name, "the request could not be ended")
		}
	}
	answer, err := io.ReadAll(io.LimitReader(conn, maxAnswerBytes+1))
	if err != nil || len(answer) == 0 || len(answer) > maxAnswerBytes {
		return helperschema.Response{}, unknownOutcome(name, "no answer could be read; whether the helper performed is unknown")
	}
	var response helperschema.Response
	decoder := json.NewDecoder(bytes.NewReader(answer))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return helperschema.Response{}, unknownOutcome(name, "the answer is not a helper's answer")
	}
	switch response.Result {
	case helperschema.ResultPerformed, helperschema.ResultAnswered, helperschema.ResultRefused, helperschema.ResultFailed:
		return response, nil
	}
	return helperschema.Response{}, unknownOutcome(name, "the answer has no result of the seam")
}
