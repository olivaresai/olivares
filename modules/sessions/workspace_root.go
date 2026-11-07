// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// rootProbeTimeout is how long a folder root may take to answer before the folder counts
// as not responding. A root on an NFS or SMB mount whose file server is down blocks in the
// kernel with no error, and no deadline can interrupt it.
var rootProbeTimeout = 10 * time.Second

// errRootNotResponding is a folder root that did not answer within rootProbeTimeout.
var errRootNotResponding = errors.New("folder root is not responding")

// rootProbe is one lookup of a folder root. Later callers for the same root share it, so a
// lookup stuck in the kernel holds one goroutine until the server answers, however many
// requests arrive meanwhile.
type rootProbe struct {
	done chan struct{}
	real string
	info fs.FileInfo
	err  error
}

var (
	rootProbesMu sync.Mutex
	rootProbes   = map[string]*rootProbe{}
)

// probeRoot canonicalizes a root and reads it from the file server, not from a cache. It
// returns an empty path when the root cannot be resolved and a path with an error when the
// resolved root cannot be read. A variable so a test can hold it.
var probeRoot = func(root string) (string, fs.FileInfo, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", nil, err
	}
	if err := syncRoot(real); err != nil {
		return real, nil, err
	}
	info, err := os.Stat(real)
	return real, info, err
}

// resolveRoot is probeRoot with a deadline: errRootNotResponding when the root does not answer.
func resolveRoot(root string) (string, fs.FileInfo, error) {
	rootProbesMu.Lock()
	p := rootProbes[root]
	if p == nil {
		p = &rootProbe{done: make(chan struct{})}
		rootProbes[root] = p
		go func() {
			p.real, p.info, p.err = probeRoot(root)
			rootProbesMu.Lock()
			delete(rootProbes, root)
			rootProbesMu.Unlock()
			close(p.done)
		}()
	}
	rootProbesMu.Unlock()
	timer := time.NewTimer(rootProbeTimeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.real, p.info, p.err
	case <-timer.C:
		return "", nil, errRootNotResponding
	}
}

// RootResponds reports whether a folder root answers from its file server within the deadline.
// `olivares netfolder ls` reads it, so the CLI and the engine agree on what "not responding" is.
func RootResponds(root string) bool {
	_, _, err := resolveRoot(root)
	return err == nil
}

// rootNotResponding is the refusal for a folder whose file server does not answer.
func rootNotResponding(root string) error {
	return &runErr{http.StatusFailedDependency, "The folder " + root + " is not responding. If it is on a network share, check that its file server is up and reachable."}
}
