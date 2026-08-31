// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// canonicalizeHookPayloadPaths rewrites only the copy forwarded to the governed PEP.
// It runs on the agent host, the only side that can resolve that host's symlinks.
func canonicalizeHookPayloadPaths(body []byte) []byte {
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
		canon, didChange := canonicalizeExistingAncestorPath(s)
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
	if !filepath.IsAbs(p) {
		return p, false
	}

	clean := filepath.Clean(p)
	ancestor := clean
	for {
		if _, err := os.Lstat(ancestor); err == nil {
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

	realAnc, err := filepath.EvalSymlinks(ancestor)
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
