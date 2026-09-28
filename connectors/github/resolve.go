// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxResolveRead caps one commit read. Past it the read is an upstream error.
const maxResolveRead = 1 << 20

// refPathOK rejects a ref with an empty, "." or ".." segment. Git refuses
// such names, and in a request path they would leave the repository.
func refPathOK(ref string) bool {
	for _, seg := range strings.Split(ref, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// isObjectID reports a SHA-1 object id: exactly 40 lowercase hex digits.
func isObjectID(s string) bool {
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

// resolveCommit reads the commit a ref names. The sha media type answers with
// the commit's SHA-1 as text. The caller has checked the ref with refPathOK.
func (s *Source) resolveCommit(ctx context.Context, repoURL, ref string) (string, error) {
	segs := strings.Split(ref, "/")
	for i := range segs {
		segs[i] = url.PathEscape(segs[i])
	}
	body, err := s.hostGET(ctx, repoURL+"/commits/"+strings.Join(segs, "/"), "application/vnd.github.sha", maxResolveRead, mapResolveStatus)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	b, err := io.ReadAll(body)
	if err != nil {
		return "", ErrUpstream
	}
	sha := strings.TrimSpace(string(b))
	if !isObjectID(sha) {
		return "", ErrUpstream
	}
	return sha, nil
}

// commitTree reads a commit object by its SHA and returns its tree. The
// object must name the commit that was asked for.
func (s *Source) commitTree(ctx context.Context, repoURL, commit string) (string, error) {
	body, err := s.hostGET(ctx, repoURL+"/git/commits/"+commit, "application/vnd.github+json", maxResolveRead, mapDiffStatus)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	var obj struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	dec := json.NewDecoder(body)
	if err := dec.Decode(&obj); err != nil {
		return "", ErrUpstream
	}
	// The object must be the whole response: only whitespace may follow it, within
	// the read cap. A second value, other trailing bytes or a read past the cap is
	// an upstream error.
	var rest json.RawMessage
	if err := dec.Decode(&rest); err != io.EOF {
		return "", ErrUpstream
	}
	if obj.SHA != commit || !isObjectID(obj.Tree.SHA) {
		return "", ErrUpstream
	}
	return obj.Tree.SHA, nil
}

// mapResolveStatus maps a commit read. Besides 404, the host lists 422
// (validation failed) for this read; for a ref that is the unresolved case,
// so it is an unknown ref too.
func mapResolveStatus(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return ErrUnknownRef
	}
	return mapDiffStatus(resp)
}
