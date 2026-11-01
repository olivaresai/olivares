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

func canonicalizeHookPayloadPaths(body []byte) ([]byte, error) {
	return canonicalizeHookPayloadPathsWith(context.Background(), body, hostHookPathCalls)
}

func canonicalizeHookPayloadPathsWith(ctx context.Context, body []byte, calls hookPathCalls) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return body, nil
	}
	ti, ok := m["tool_input"].(map[string]any)
	if !ok {
		return body, nil
	}

	changed := false
	for _, key := range []string{"file_path", "notebook_path"} {
		s, ok := ti[key].(string)
		if !ok || s == "" {
			continue
		}
		canon, didChange, err := canonicalizeExistingAncestorPathWith(ctx, s, calls)
		if err != nil {
			return nil, err
		}
		if didChange {
			ti[key] = canon
			changed = true
		}
	}
	if !changed {
		return body, nil
	}

	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func canonicalizeExistingAncestorPath(p string) (string, bool, error) {
	return canonicalizeExistingAncestorPathWith(context.Background(), p, hostHookPathCalls)
}

func canonicalizeExistingAncestorPathWith(ctx context.Context, p string, calls hookPathCalls) (string, bool, error) {
	if !filepath.IsAbs(p) {
		return p, false, nil
	}

	clean := filepath.Clean(p)
	ancestor := clean
	for {
		if ctx.Err() != nil {
			return p, false, ctx.Err()
		}
		if _, err := calls.lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return p, false, err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return p, false, nil
		}
		ancestor = parent
	}

	if ctx.Err() != nil {
		return p, false, ctx.Err()
	}
	realAnc, err := calls.evalSymlinks(ancestor)
	if ctx.Err() != nil {
		return p, false, ctx.Err()
	}
	if err != nil {
		return p, false, err
	}
	tail, err := filepath.Rel(ancestor, clean)
	if err != nil {
		return p, false, err
	}
	canon := filepath.Join(realAnc, tail)
	if canon == clean {
		return clean, false, nil
	}
	return canon, true, nil
}
