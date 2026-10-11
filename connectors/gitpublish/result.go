// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Effect names what a token is minted for. It selects the token permissions.
type Effect string

const (
	EffectPush        Effect = "push"
	EffectPullRequest Effect = "pull_request"
	EffectMerge       Effect = "merge"
	// EffectObserve is read-only reconciliation and preflight.
	EffectObserve Effect = "observe"
	// EffectRead is a session's clone and fetch: contents:read only.
	EffectRead Effect = "read"
)

// Class is the classification of one host WRITE.
type Class int

const (
	// Applied: the host acknowledged the write with success.
	Applied Class = iota + 1
	// Rejected: a documented, definitive non-application.
	Rejected
	// Ambiguous: the write may or may not have been applied, or may still be.
	Ambiguous
)

func (c Class) String() string {
	switch c {
	case Applied:
		return "applied"
	case Rejected:
		return "rejected"
	case Ambiguous:
		return "ambiguous"
	}
	return "unknown"
}

// Result is the classified result of one host write.
type Result struct {
	Class  Class
	Reason string
	Host   HostError
}

var (
	// ErrCredentialRefused: the host refused the credential or its mint.
	ErrCredentialRefused = errors.New("gitpublish: host credential refused")
	// ErrTokenScope: the minted token covers more or other repositories.
	ErrTokenScope = errors.New("gitpublish: minted token is not narrowed to the target repository")
	// ErrLookupIncomplete: a host list could not be read completely.
	ErrLookupIncomplete = errors.New("gitpublish: host lookup incomplete")
	// ErrHostUnavailable: a read failed without a definitive answer.
	ErrHostUnavailable = errors.New("gitpublish: host unavailable")
	// ErrEndpoint: an endpoint outside the trust rules.
	ErrEndpoint = errors.New("gitpublish: endpoint not allowed")
	// ErrNotSupported: the host offers no such read.
	ErrNotSupported = errors.New("gitpublish: not supported by this host")
)

// HostError carries ONLY bounded fields from a host response: the status, a
// closed code this package derives, and the request id. A host body never
// enters it.
type HostError struct {
	Status    int
	Code      string
	RequestID string
	kind      error
}

func (e *HostError) Error() string {
	s := fmt.Sprintf("git host: status %d code %s", e.Status, e.Code)
	if e.RequestID != "" {
		s += " request " + e.RequestID
	}
	return s
}

// Unwrap returns the sentinel the error belongs to.
func (e *HostError) Unwrap() error { return e.kind }

func codeFor(status int) string {
	switch {
	case status == 0:
		return "transport"
	case status == http.StatusBadRequest:
		return "bad_request"
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status == http.StatusForbidden:
		return "forbidden"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusUnprocessableEntity:
		return "validation_failed"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500:
		return "server_error"
	}
	return "unexpected"
}

// requestID keeps a request id only in a bounded, printable form.
func requestID(h http.Header) string {
	v := h.Get("X-GitHub-Request-Id")
	if v == "" {
		v = h.Get("X-Request-Id")
	}
	if len(v) > 128 {
		v = v[:128]
	}
	if strings.IndexFunc(v, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(":._-", r))
	}) >= 0 {
		return ""
	}
	return v
}

func hostError(status int, h http.Header, kind error) HostError {
	e := HostError{Status: status, Code: codeFor(status), kind: kind}
	if h != nil {
		e.RequestID = requestID(h)
	}
	return e
}

// classifyWrite maps a write's status to a Class. definite lists the
// documented refusals of that endpoint with their reason; any other 4xx that
// the host documents as a refusal of the credential is Rejected; everything
// else, including transport failures, is Ambiguous.
func classifyWrite(status int, h http.Header, ok int, definite map[int]string) Result {
	if status == ok {
		return Result{Class: Applied, Host: hostError(status, h, nil)}
	}
	he := hostError(status, h, nil)
	if reason, found := definite[status]; found {
		return Result{Class: Rejected, Reason: reason, Host: he}
	}
	switch status {
	case http.StatusUnauthorized:
		return Result{Class: Rejected, Reason: "unauthorized", Host: he}
	case http.StatusForbidden:
		return Result{Class: Rejected, Reason: "forbidden", Host: he}
	case http.StatusNotFound:
		return Result{Class: Rejected, Reason: "not_found", Host: he}
	}
	return Result{Class: Ambiguous, Reason: he.Code, Host: he}
}

// RefState is what a ref read showed.
type RefState int

const (
	RefUnknown RefState = iota
	RefPresent
	// RefNotFoundOrHidden: the host answered 404. That is absence OR a ref the
	// token may not see; it is never proof of absence.
	RefNotFoundOrHidden
)

// BranchInfo is what the host reports about a branch: the repository's
// default branch, and whether this branch exists and is protected.
type BranchInfo struct {
	Default   string
	Exists    bool
	Protected bool
}

// RefObservation is one ref read.
type RefObservation struct {
	State RefState
	SHA   string
}

// Change is a pull request (GitHub) or merge request (GitLab).
type Change struct {
	Number         int
	Open           bool
	Merged         bool
	HeadRef        string
	HeadSHA        string
	BaseRef        string
	MergeCommitSHA string
	CreatedAt      time.Time
}

// ChangeSpec is a create request. Hosts bind branches, not SHAs.
type ChangeSpec struct {
	Head, Base, Title, Body string
	Draft                   bool
}

// MergeOutcome is what a successful merge reported.
type MergeOutcome struct {
	Merged         bool
	MergeCommitSHA string
}
