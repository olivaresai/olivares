// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// Git-host content diff (J10-S1). The route is a read under the same
// system:admin permission as the source roster. nil answers 501 until the
// composition root attaches a reader.

// ErrContentDiffUnknownRef is a compare that names a ref the host does not have.
var ErrContentDiffUnknownRef = errors.New("content diff: unknown ref")

// ErrContentDiffForbidden is a compare the host refused.
var ErrContentDiffForbidden = errors.New("content diff: forbidden")

// ErrContentDiffUpstream is any other failed compare. Handlers must not copy
// the wrapped text into a response: it may carry upstream material.
var ErrContentDiffUpstream = errors.New("content diff: upstream error")

// GitHostDiffQuery is the read input: which roster source, which host, which
// repository, which refs.
type GitHostDiffQuery struct {
	Source     string
	Host       string
	Repository string
	Base       string
	Head       string
}

// GitHostDiffFile is one bounded file in a content diff.
type GitHostDiffFile struct {
	Path         string   `json:"path"`
	PreviousPath string   `json:"previous_path,omitempty"`
	Status       string   `json:"status"`
	Binary       bool     `json:"binary"`
	Truncated    bool     `json:"truncated"`
	Hunks        []string `json:"hunks"`
}

// GitHostDiff is the bounded compare result returned by the read route. Base
// and Head are the refs as requested. BaseCommit and HeadCommit are the
// commits they resolved to, and Files is the compare of exactly those two
// commits. HeadTree is the tree of HeadCommit; it is absent on GitLab, whose
// commit read exposes no tree id.
type GitHostDiff struct {
	Source     string            `json:"source,omitempty"`
	Host       string            `json:"host"`
	Repository string            `json:"repository"`
	Base       string            `json:"base"`
	Head       string            `json:"head"`
	BaseCommit string            `json:"base_commit"`
	HeadCommit string            `json:"head_commit"`
	HeadTree   string            `json:"head_tree,omitempty"`
	Truncated  bool              `json:"truncated"`
	Files      []GitHostDiffFile `json:"files"`
}

// ContentDiffReader reads one bounded content diff. The composition root
// supplies an implementation backed by the GitHub and GitLab connectors.
type ContentDiffReader interface {
	ReadContentDiff(ctx context.Context, q GitHostDiffQuery) (GitHostDiff, error)
}

func (s *Server) handleGitHostDiff(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	if s.contentDiff == nil {
		s.writeGitHostDiffError(w, r, http.StatusNotImplemented, "git_host_diff_unavailable", "git host diff is not wired on this deployment")
		return
	}
	q := GitHostDiffQuery{
		Source:     strings.TrimSpace(r.URL.Query().Get("source")),
		Host:       strings.TrimSpace(r.URL.Query().Get("host")),
		Repository: strings.TrimSpace(r.URL.Query().Get("repository")),
		Base:       strings.TrimSpace(r.URL.Query().Get("base")),
		Head:       strings.TrimSpace(r.URL.Query().Get("head")),
	}
	if q.Source == "" || q.Repository == "" || q.Base == "" || q.Head == "" || (q.Host != "github" && q.Host != "gitlab") {
		s.writeGitHostDiffError(w, r, http.StatusBadRequest, "bad_request", "source, host, repository, base, and head are required")
		return
	}
	if !s.gitHostSourceKnown(r.Context(), q.Source) {
		s.writeGitHostDiffError(w, r, http.StatusBadRequest, "bad_request", "unknown source")
		return
	}
	diff, err := s.contentDiff.ReadContentDiff(r.Context(), q)
	if err != nil {
		switch {
		case errors.Is(err, ErrContentDiffUnknownRef):
			s.writeGitHostDiffError(w, r, http.StatusNotFound, "unknown_ref", "unknown ref")
		case errors.Is(err, ErrContentDiffForbidden):
			s.writeGitHostDiffError(w, r, http.StatusForbidden, "git_host_forbidden", "git host refused the read")
		default:
			var tooLarge interface{ GitHostDiffTooLarge() }
			if errors.As(err, &tooLarge) {
				s.writeGitHostDiffError(w, r, http.StatusUnprocessableEntity, "diff_too_large", "diff exceeds the read cap")
				return
			}
			var limited interface{ RetryAfter() string }
			if errors.As(err, &limited) {
				if after := limited.RetryAfter(); after != "" {
					w.Header().Set("Retry-After", after)
				}
				s.writeGitHostDiffError(w, r, http.StatusServiceUnavailable, "rate_limited", "git host rate limited")
				return
			}
			s.writeGitHostDiffError(w, r, http.StatusBadGateway, "upstream", "git host read failed")
		}
		return
	}
	if !gitHostCommitIdentityOK(q.Host, diff) {
		s.writeGitHostDiffError(w, r, http.StatusBadGateway, "upstream", "git host read failed")
		return
	}
	diff.Source = q.Source
	diff.Host = q.Host
	diff.Repository = q.Repository
	diff.Base = q.Base
	diff.Head = q.Head
	if diff.Files == nil {
		diff.Files = []GitHostDiffFile{}
	}
	for i := range diff.Files {
		if diff.Files[i].Hunks == nil {
			diff.Files[i].Hunks = []string{}
		}
	}
	writeJSON(w, http.StatusOK, diff)
}

// gitHostCommitIdentityOK checks the resolved identity before it is returned.
// base_commit and head_commit are required. head_tree is required on GitHub;
// elsewhere it may be absent, and when present it must be valid too.
func gitHostCommitIdentityOK(host string, d GitHostDiff) bool {
	if !isGitObjectID(d.BaseCommit) || !isGitObjectID(d.HeadCommit) {
		return false
	}
	if d.HeadTree == "" {
		return host != "github"
	}
	return isGitObjectID(d.HeadTree)
}

// isGitObjectID reports a SHA-1 object id: exactly 40 lowercase hex digits.
func isGitObjectID(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Server) gitHostSourceKnown(ctx context.Context, name string) bool {
	if s.sourceRoster == nil || name == "" {
		return false
	}
	entries, err := s.sourceRoster.ListSources(ctx)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) writeGitHostDiffError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if status > http.StatusInternalServerError {
		w.Header().Set("Cache-Control", "no-store")
		if s.log != nil {
			s.log.Info("api: refused by design", "code", code, "status", status,
				"path", r.URL.Path, "request_id", requestID(r.Context()))
		}
	}
	var b errorBody
	b.Error.Code = code
	b.Error.Message = message
	writeJSON(w, status, b)
}
