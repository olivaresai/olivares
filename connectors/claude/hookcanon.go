// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// canonicalizeHookPayloadPaths rewrites only the copy forwarded to the governed PEP.
// It runs on the agent host, the only side that can resolve that host's symlinks.
type hookPathCalls struct {
	lstat        func(string) (os.FileInfo, error)
	evalSymlinks func(string) (string, error)
}

var hostHookPathCalls = hookPathCalls{lstat: os.Lstat, evalSymlinks: filepath.EvalSymlinks}

func canonicalizeHookPayloadPaths(body []byte) []byte {
	return canonicalizeHookPayloadPathsWith(context.Background(), body, hostHookPathCalls)
}

func canonicalizeHookPayloadPathsWith(ctx context.Context, body []byte, calls hookPathCalls) []byte {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body
	}
	ti, ok := m["tool_input"].(map[string]any)
	if !ok {
		return body
	}

	changed := false
	for _, key := range []string{"file_path", "notebook_path"} {
		s, ok := ti[key].(string)
		if !ok || s == "" {
			continue
		}
		canon, didChange := canonicalizeExistingAncestorPathWith(ctx, s, calls)
		if didChange {
			ti[key] = canon
			changed = true
		}
	}
	if !changed {
		return body
	}

	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

func canonicalizeExistingAncestorPath(p string) (string, bool) {
	return canonicalizeExistingAncestorPathWith(context.Background(), p, hostHookPathCalls)
}

func canonicalizeExistingAncestorPathWith(ctx context.Context, p string, calls hookPathCalls) (string, bool) {
	if !filepath.IsAbs(p) {
		return p, false
	}

	clean := filepath.Clean(p)
	ancestor := clean
	for {
		if ctx.Err() != nil {
			return p, false
		}
		if _, err := calls.lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return p, false
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return p, false
		}
		ancestor = parent
	}

	if ctx.Err() != nil {
		return p, false
	}
	realAnc, err := calls.evalSymlinks(ancestor)
	if ctx.Err() != nil {
		return p, false
	}
	if err != nil {
		return p, false
	}
	tail, err := filepath.Rel(ancestor, clean)
	if err != nil {
		return p, false
	}
	canon := filepath.Join(realAnc, tail)
	if canon == clean {
		return clean, false
	}
	return canon, true
}
