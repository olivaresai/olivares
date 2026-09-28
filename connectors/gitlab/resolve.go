// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"
)

// maxResolveRead caps one commit read. Past it the read is an upstream error.
const maxResolveRead = 1 << 20

// refPathOK rejects a ref with an empty, "." or ".." segment. Git refuses
// such names, and in a request path they would leave the project.
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

// resolveCommit reads the commit a ref names and returns its id. GitLab's
// commit read carries no tree id, so no tree is read. The caller has checked
// the ref with refPathOK.
func (s *Source) resolveCommit(ctx context.Context, projectURL, ref string) (string, error) {
	body, err := s.hostGET(ctx, projectURL+"/repository/commits/"+url.PathEscape(ref), maxResolveRead)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	var c struct {
		ID string `json:"id"`
	}
	dec := json.NewDecoder(body)
	if err := dec.Decode(&c); err != nil {
		return "", ErrUpstream
	}
	// The object must be the whole response: only whitespace may follow it, within
	// the read cap. A second value, other trailing bytes or a read past the cap is
	// an upstream error.
	var rest json.RawMessage
	if err := dec.Decode(&rest); err != io.EOF {
		return "", ErrUpstream
	}
	if !isObjectID(c.ID) {
		return "", ErrUpstream
	}
	return c.ID, nil
}
