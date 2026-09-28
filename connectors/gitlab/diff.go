// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	maxDiffFiles = 100
	maxDiffBytes = 256 << 10
	// maxDiffRead is the hard cap on bytes read from one compare. Past it the
	// read is diff-too-large, not an upstream failure.
	maxDiffRead = 8 << 20
)

var errReadCap = errors.New("gitlab: diff read cap")

type diffTooLargeError struct{}

func (diffTooLargeError) Error() string        { return "gitlab: diff too large" }
func (diffTooLargeError) GitHostDiffTooLarge() {}

// ErrDiffTooLarge is a compare body past the hard read cap.
var ErrDiffTooLarge error = diffTooLargeError{}

// ErrUnknownRef is a compare or commit read 404, or a project or ref the read
// will not request. The upstream body is not part of the error.
var ErrUnknownRef = errors.New("gitlab: unknown ref")

// ErrForbidden is a compare or commit read 401 or 403. The upstream body is
// not part of the error.
var ErrForbidden = errors.New("gitlab: forbidden")

// ErrUpstream is any other non-200 response, a response that cannot be read,
// or a commit id that is not 40 lowercase hex. The upstream body is not part
// of the error.
var ErrUpstream = errors.New("gitlab: upstream error")

// DiffFile is one path in a bounded content diff.
type DiffFile struct {
	Path         string
	Status       string
	Binary       bool
	Truncated    bool
	PreviousPath string
	Hunks        []string
}

// ContentDiff is a bounded read of one repository compare. Base and Head are
// the refs as requested. BaseCommit and HeadCommit are the commits they
// resolved to, and Files is the compare of exactly those two commits.
// HeadTree stays empty: GitLab's commit read exposes no tree id.
type ContentDiff struct {
	Repository string
	Base       string
	Head       string
	BaseCommit string
	HeadCommit string
	HeadTree   string
	Truncated  bool
	Files      []DiffFile
}

// ReadContentDiff resolves base and head to commits, then reads the GitLab
// repository compare of the two resolved SHAs and bounds the unified hunks.
// A ref that moves during the read cannot mix two states. It only sends GET.
func (s *Source) ReadContentDiff(ctx context.Context, repository, base, head string) (ContentDiff, error) {
	repository = strings.TrimSpace(repository)
	base = strings.TrimSpace(base)
	head = strings.TrimSpace(head)
	if repository == "" || base == "" || head == "" {
		return ContentDiff{}, ErrUpstream
	}
	if err := scopeGitLabRepo(s.group, repository); err != nil {
		return ContentDiff{}, err
	}
	if !refPathOK(base) || !refPathOK(head) {
		return ContentDiff{}, ErrUnknownRef
	}
	projectURL := s.apiBase + "/api/v4/projects/" + url.PathEscape(repository)
	baseCommit, err := s.resolveCommit(ctx, projectURL, base)
	if err != nil {
		return ContentDiff{}, err
	}
	headCommit, err := s.resolveCommit(ctx, projectURL, head)
	if err != nil {
		return ContentDiff{}, err
	}
	body, err := s.hostGET(ctx, projectURL+"/repository/compare?from="+baseCommit+"&to="+headCommit, maxDiffRead)
	if err != nil {
		return ContentDiff{}, err
	}
	defer func() { _ = body.Close() }()
	diff, err := parseGitLabCompare(body)
	if err != nil {
		return ContentDiff{}, err
	}
	diff.Repository = repository
	diff.Base = base
	diff.Head = head
	diff.BaseCommit = baseCommit
	diff.HeadCommit = headCommit
	return diff, nil
}

// scopeGitLabRepo accepts a path under the source group with no "." or ".."
// segment and no numeric project id. Anything else is an unknown ref and
// must not be requested.
func scopeGitLabRepo(group, repository string) error {
	if group == "" || !strings.HasPrefix(repository, group+"/") {
		return ErrUnknownRef
	}
	segs := strings.Split(repository, "/")
	if len(segs) < 2 {
		return ErrUnknownRef
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." || numericID(seg) {
			return ErrUnknownRef
		}
	}
	return nil
}

func numericID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// hostGET sends one authenticated GET and caps the body it returns at limit
// bytes.
func (s *Source) hostGET(ctx context.Context, rawURL string, limit int) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrUpstream
	}
	req.Header.Set("PRIVATE-TOKEN", s.token)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, ErrUpstream
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, mapDiffStatus(resp)
	}
	return &cappedBody{Reader: &capReader{r: resp.Body, max: limit}, Closer: resp.Body}, nil
}

type cappedBody struct {
	io.Reader
	io.Closer
}

type capReader struct {
	r   io.Reader
	n   int
	max int
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n >= c.max {
		var b [1]byte
		n, err := c.r.Read(b[:])
		if n > 0 || err == nil {
			return 0, errReadCap
		}
		return 0, err
	}
	if remain := c.max - c.n; len(p) > remain {
		p = p[:remain]
	}
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

type rateLimitError struct{ after string }

func (e *rateLimitError) Error() string      { return "gitlab: rate limited" }
func (e *rateLimitError) RetryAfter() string { return e.after }

func mapDiffStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusTooManyRequests {
		return &rateLimitError{after: resp.Header.Get("Retry-After")}
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return ErrUnknownRef
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrForbidden
	default:
		return ErrUpstream
	}
}

func mapReadErr(err error) error {
	if errors.Is(err, errReadCap) {
		return ErrDiffTooLarge
	}
	return ErrUpstream
}

func parseGitLabCompare(r io.Reader) (ContentDiff, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return ContentDiff{}, mapReadErr(err)
	}
	if tok != json.Delim('{') {
		return ContentDiff{}, ErrUpstream
	}
	var files []DiffFile
	timedOut := false
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ContentDiff{}, mapReadErr(err)
		}
		key, _ := keyTok.(string)
		switch key {
		case "diffs":
			parsed, err := decodeGitLabDiffs(dec)
			if err != nil {
				return ContentDiff{}, err
			}
			files = append(files, parsed...)
		case "compare_timeout":
			var v bool
			if err := dec.Decode(&v); err != nil {
				return ContentDiff{}, mapReadErr(err)
			}
			if v {
				timedOut = true
			}
		default:
			if err := skipJSONValue(dec); err != nil {
				return ContentDiff{}, mapReadErr(err)
			}
		}
	}
	if _, err := dec.Token(); err != nil && !errors.Is(err, io.EOF) {
		return ContentDiff{}, mapReadErr(err)
	}
	out, truncated := applyDiffBounds(files)
	if timedOut {
		truncated = true
	}
	return ContentDiff{Files: out, Truncated: truncated}, nil
}

type glCompareDiff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	TooLarge    bool   `json:"too_large"`
}

func decodeGitLabDiffs(dec *json.Decoder) ([]DiffFile, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, mapReadErr(err)
	}
	if tok == nil {
		return nil, nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, ErrUpstream
	}
	files := make([]DiffFile, 0)
	for dec.More() {
		var d glCompareDiff
		if err := dec.Decode(&d); err != nil {
			return nil, mapReadErr(err)
		}
		path := d.NewPath
		if path == "" {
			path = d.OldPath
		}
		status := "modified"
		switch {
		case d.NewFile:
			status = "added"
		case d.DeletedFile:
			status = "removed"
		case d.RenamedFile:
			status = "renamed"
		}
		df := classifyGitLabDiff(d, path, status)
		files = append(files, df)
	}
	if _, err := dec.Token(); err != nil {
		return nil, mapReadErr(err)
	}
	return files, nil
}

func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{', '[':
		for dec.More() {
			if d == '{' {
				if _, err := dec.Token(); err != nil {
					return err
				}
			}
			if err := skipJSONValue(dec); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	default:
		return nil
	}
}

// classifyGitLabDiff maps one compare diff. A pure rename (empty diff) is
// neither binary nor truncated. too_large marks the file truncated.
func classifyGitLabDiff(d glCompareDiff, path, status string) DiffFile {
	df := DiffFile{Path: path, Status: status}
	if d.OldPath != "" && d.OldPath != path {
		df.PreviousPath = d.OldPath
	}
	switch {
	case d.RenamedFile && d.Diff == "" && !d.TooLarge:
	case d.TooLarge:
		df.Truncated = true
		if d.Diff != "" && !strings.HasPrefix(d.Diff, "Binary files") {
			df.Hunks = splitUnifiedHunks(d.Diff)
		}
	case strings.HasPrefix(d.Diff, "Binary files"), d.Diff == "" && !d.DeletedFile:
		df.Binary = true
	case d.Diff != "":
		df.Hunks = splitUnifiedHunks(d.Diff)
	}
	return df
}

func splitUnifiedHunks(patch string) []string {
	if patch == "" {
		return nil
	}
	var hunks []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		hunks = append(hunks, strings.Join(cur, "\n"))
		cur = nil
	}
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") && len(cur) > 0 {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	return hunks
}

func applyDiffBounds(files []DiffFile) ([]DiffFile, bool) {
	var out []DiffFile
	used := 0
	truncated := false
	for _, f := range files {
		if len(out) >= maxDiffFiles {
			return out, true
		}
		var kept []string
		for _, h := range f.Hunks {
			if used >= maxDiffBytes {
				f.Truncated = true
				truncated = true
				break
			}
			if remain := maxDiffBytes - used; len(h) > remain {
				part := cutAtBound(h, remain)
				if part != "" {
					kept = append(kept, part)
					used += len(part)
				}
				f.Truncated = true
				truncated = true
				break
			}
			kept = append(kept, h)
			used += len(h)
		}
		f.Hunks = kept
		if f.Truncated {
			truncated = true
		}
		out = append(out, f)
	}
	return out, truncated
}

// cutAtBound keeps a prefix of at most remain bytes that ends on a newline
// when one fits, otherwise on a UTF-8 rune boundary.
func cutAtBound(h string, remain int) string {
	if remain <= 0 {
		return ""
	}
	if len(h) <= remain {
		return h
	}
	cut := h[:remain]
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		return cut[:i+1]
	}
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
