// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

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
	// read is diff-too-large, not an upstream failure. Retention stays at
	// maxDiffFiles and maxDiffBytes.
	maxDiffRead = 8 << 20
)

// errReadCap is raised by the hard-cap reader. It is not returned to callers.
var errReadCap = errors.New("github: diff read cap")

// diffTooLargeError is a compare body past maxDiffRead.
type diffTooLargeError struct{}

func (diffTooLargeError) Error() string        { return "github: diff too large" }
func (diffTooLargeError) GitHostDiffTooLarge() {}

// ErrDiffTooLarge is a compare body past the hard read cap.
var ErrDiffTooLarge error = diffTooLargeError{}

// ErrUnknownRef is a compare or commit read 404, a commit read 422, or a
// repository or ref the read will not request. The upstream body is not part
// of the error.
var ErrUnknownRef = errors.New("github: unknown ref")

// ErrForbidden is a compare or commit read 401 or 403. The upstream body is
// not part of the error.
var ErrForbidden = errors.New("github: forbidden")

// ErrUpstream is any other non-200 response, a response that cannot be read,
// or a commit or tree id that is not 40 lowercase hex. The upstream body is
// not part of the error.
var ErrUpstream = errors.New("github: upstream error")

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
// HeadTree is the tree of HeadCommit.
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

// ReadContentDiff resolves base and head to commits, reads the head commit's
// tree, then reads the GitHub compare of the two resolved SHAs and bounds the
// unified hunks. A ref that moves during the read cannot mix two states. It
// only sends GET.
func (s *Source) ReadContentDiff(ctx context.Context, repository, base, head string) (ContentDiff, error) {
	repository = strings.TrimSpace(repository)
	base = strings.TrimSpace(base)
	head = strings.TrimSpace(head)
	if repository == "" || base == "" || head == "" {
		return ContentDiff{}, ErrUpstream
	}
	if err := scopeGitHubRepo(s.org, repository); err != nil {
		return ContentDiff{}, err
	}
	if !refPathOK(base) || !refPathOK(head) {
		return ContentDiff{}, ErrUnknownRef
	}
	segs := strings.Split(repository, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	repoURL := s.apiBase + "/repos/" + strings.Join(segs, "/")
	baseCommit, err := s.resolveCommit(ctx, repoURL, base)
	if err != nil {
		return ContentDiff{}, err
	}
	headCommit, err := s.resolveCommit(ctx, repoURL, head)
	if err != nil {
		return ContentDiff{}, err
	}
	headTree, err := s.commitTree(ctx, repoURL, headCommit)
	if err != nil {
		return ContentDiff{}, err
	}
	body, err := s.hostGET(ctx, repoURL+"/compare/"+baseCommit+"..."+headCommit, "application/vnd.github+json", maxDiffRead, mapDiffStatus)
	if err != nil {
		return ContentDiff{}, err
	}
	defer func() { _ = body.Close() }()
	diff, err := parseGitHubCompare(body)
	if err != nil {
		return ContentDiff{}, err
	}
	diff.Repository = repository
	diff.Base = base
	diff.Head = head
	diff.BaseCommit = baseCommit
	diff.HeadCommit = headCommit
	diff.HeadTree = headTree
	return diff, nil
}

// scopeGitHubRepo accepts exactly two non-empty segments, neither "." nor
// "..", whose owner matches the source org. Anything else is an unknown ref
// and must not be requested.
func scopeGitHubRepo(org, repository string) error {
	segs := strings.Split(repository, "/")
	if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
		return ErrUnknownRef
	}
	if segs[0] == "." || segs[0] == ".." || segs[1] == "." || segs[1] == ".." {
		return ErrUnknownRef
	}
	if !strings.EqualFold(segs[0], org) {
		return ErrUnknownRef
	}
	return nil
}

// hostGET sends one authenticated GET and caps the body it returns at limit
// bytes. A non-200 answer is mapped by mapStatus.
func (s *Source) hostGET(ctx context.Context, rawURL, accept string, limit int, mapStatus func(*http.Response) error) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrUpstream
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", accept)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, ErrUpstream
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, mapStatus(resp)
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

func (e *rateLimitError) Error() string      { return "github: rate limited" }
func (e *rateLimitError) RetryAfter() string { return e.after }

func mapDiffStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0") {
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

func parseGitHubCompare(r io.Reader) (ContentDiff, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return ContentDiff{}, mapReadErr(err)
	}
	if tok != json.Delim('{') {
		return ContentDiff{}, ErrUpstream
	}
	var files []DiffFile
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ContentDiff{}, mapReadErr(err)
		}
		key, _ := keyTok.(string)
		if key == "files" {
			parsed, err := decodeGitHubFiles(dec)
			if err != nil {
				return ContentDiff{}, err
			}
			files = append(files, parsed...)
			continue
		}
		if err := skipJSONValue(dec); err != nil {
			return ContentDiff{}, mapReadErr(err)
		}
	}
	if _, err := dec.Token(); err != nil && !errors.Is(err, io.EOF) {
		return ContentDiff{}, mapReadErr(err)
	}
	out, truncated := applyDiffBounds(files)
	return ContentDiff{Files: out, Truncated: truncated}, nil
}

type ghCompareFile struct {
	Filename         string  `json:"filename"`
	PreviousFilename string  `json:"previous_filename"`
	Status           string  `json:"status"`
	Patch            *string `json:"patch"`
	Additions        int     `json:"additions"`
	Deletions        int     `json:"deletions"`
	Changes          int     `json:"changes"`
}

func decodeGitHubFiles(dec *json.Decoder) ([]DiffFile, error) {
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
		var f ghCompareFile
		if err := dec.Decode(&f); err != nil {
			return nil, mapReadErr(err)
		}
		df := classifyGitHubFile(f)
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

// classifyGitHubFile maps one compare file. A pure rename is neither binary
// nor truncated. A missing patch with additions or deletions is truncated,
// because the host omitted the text. Binary is only the remaining case.
func classifyGitHubFile(f ghCompareFile) DiffFile {
	df := DiffFile{Path: f.Filename, Status: f.Status, PreviousPath: f.PreviousFilename}
	if f.Patch != nil && *f.Patch != "" {
		df.Hunks = splitUnifiedHunks(*f.Patch)
		return df
	}
	if f.Status == "renamed" && f.Changes == 0 {
		return df
	}
	if f.Additions+f.Deletions > 0 {
		df.Truncated = true
		return df
	}
	if f.Status != "removed" {
		df.Binary = true
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
