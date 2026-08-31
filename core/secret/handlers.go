// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secret

import (
	"bytes"
	"context"
	"fmt"
	"os"
)

// The dependency-free built-in handlers: `env:` (a process environment variable)
// and `file:` (a file's contents — the 12-factor / mounted-Secret path). The
// sealed store handler and the external network handlers (vault, cloud secret
// managers) are injected by the composition root, which owns the store and the
// transports.

// EnvHandler resolves `env:<VAR>` to the process environment variable VAR. It
// fails closed: a variable that is unset, or set to the empty string, is a
// misconfiguration (a referenced secret must have a value), not a silent empty.
type EnvHandler struct {
	// Lookup reads an environment variable and whether it was present; nil uses
	// os.LookupEnv. Injectable for tests.
	Lookup func(string) (string, bool)
}

func (h EnvHandler) Resolve(_ context.Context, locator string) ([]byte, error) {
	lookup := h.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	v, ok := lookup(locator)
	if !ok {
		return nil, fmt.Errorf("environment variable %q is not set", locator)
	}
	if v == "" {
		return nil, fmt.Errorf("environment variable %q is set but empty", locator)
	}
	return []byte(v), nil
}

// FileHandler resolves `file:<path>` to the file's contents. A single trailing
// newline (the convention for a secret written with `echo > file` or a Kubernetes
// projected Secret) is trimmed; any other content is returned verbatim. It fails
// closed: a missing/unreadable or empty file is an error.
type FileHandler struct {
	// ReadFile reads a file; nil uses os.ReadFile. Injectable for tests.
	ReadFile func(string) ([]byte, error)
}

func (h FileHandler) Resolve(_ context.Context, locator string) ([]byte, error) {
	read := h.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	b, err := read(locator)
	if err != nil {
		return nil, fmt.Errorf("read secret file %q: %w", locator, err)
	}
	b = bytes.TrimSuffix(b, []byte("\n"))
	b = bytes.TrimSuffix(b, []byte("\r"))
	if len(b) == 0 {
		return nil, fmt.Errorf("secret file %q is empty", locator)
	}
	return b, nil
}
