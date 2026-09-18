// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/modules/sessions"
)

// hostToolObserver is the production sessions.HostToolObserver (HC1 revision 1):
// the existing official-CLI Detect over locations captured once at composition.
//
// ⛔ IT IS NOT toolInstallEngine. That engine wires a GPG verifier, and Detect
// calls List, which re-verifies retained signed material — so Probe=false alone
// would still let a console read run gpg. This observer builds the same Claude
// catalog with an explicitly UNAVAILABLE verifier and an HTTP client that refuses
// every request: nothing here constructs or invokes gpg, a detected CLI, a shell
// or a network connection. A managed release whose signature is not re-verified
// therefore stays unverified; it is never reported registered.
//
// ⛔ AND AN EMPTY DETECT RESULT IS NOT ABSENCE BY ITSELF (Root correction C1).
// Detect skips every Lstat error on a vendor or PATH candidate, and the catalog's
// versions glob drops directory I/O errors, which is right for the CLI and wrong
// for a published none_observed. So before publishing, this adapter re-examines
// the same search locations and answers an error — unknown — unless each one is
// present or genuinely missing. The CLI's Detect is unchanged.
type hostToolObserver struct {
	engine  *toolinstall.Engine
	root    string
	rootErr error
	home    string
	homeErr error
	pathEnv string
	// admit allows one filesystem observation at a time. It is internal resource
	// admission, not a user quota.
	admit   chan struct{}
	network *refusedNetwork
}

// refusedNetwork is the transport of the observer's catalog. Detection never
// fetches; a request would be a regression, so it is refused and counted.
type refusedNetwork struct{ attempts atomic.Int64 }

func (n *refusedNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.attempts.Add(1)
	return nil, errors.New("host-tool observation never uses the network")
}

var (
	errHostToolHomeUnavailable = errors.New("host-tool observation: the service account home could not be established, so its vendor locations were not searched")
	errHostToolScopeUnexamined = errors.New("host-tool observation: a configured search location could not be examined, so absence is not established")
)

// newHostToolObserver captures the service account's managed tools root, home
// and PATH ONCE. The root uses the CLI's own resolution (resolveAgentToolRoot,
// same precedence as `olivares agent tool detect`); nothing is re-read per
// request, and no detection runs here.
func newHostToolObserver(getenv func(string) string) *hostToolObserver {
	root, rootErr := resolveAgentToolRoot("")
	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = strings.TrimSpace(h)
	}
	return newHostToolObserverAt(root, rootErr, home, getenv("PATH"))
}

// newHostToolObserverAt builds the observer over explicit locations. A home that
// is not absolute is an unavailable home: without it the vendor locations under
// it cannot be searched, and silently skipping them would claim complete scope.
func newHostToolObserverAt(root string, rootErr error, home, pathEnv string) *hostToolObserver {
	var homeErr error
	if filepath.IsAbs(home) {
		home = filepath.Clean(home)
	} else {
		home, homeErr = "", errHostToolHomeUnavailable
	}
	network := &refusedNetwork{}
	client := &http.Client{Transport: network}
	claude := toolinstall.NewClaude(toolinstall.ClaudeOptions{
		Verifier: toolinstall.UnavailableVerifier{},
		// cli-transport-exempt: refusedNetwork is this client's only transport and its RoundTrip
		// refuses every request, so host-tool observation never reaches the network through it.
		Client: client,
	})
	codex := toolinstall.NewCodex(toolinstall.CodexOptions{
		Verifier: toolinstall.UnavailableSubjectVerifier{},
		Client:   client,
	})
	grok := toolinstall.NewGrok(toolinstall.GrokOptions{Client: client})
	cat, err := toolinstall.NewCapabilityCatalog(toolinstall.NewCatalog(claude), codex, grok)
	var engine *toolinstall.Engine
	if err != nil {
		engine = toolinstall.NewEngine(toolinstall.NewCatalog(claude), toolinstall.EngineOptions{InstallerVersion: version})
	} else {
		engine = toolinstall.NewEngineWithCapabilities(cat, toolinstall.EngineOptions{InstallerVersion: version})
	}
	return &hostToolObserver{
		engine:  engine,
		root:    root,
		rootErr: rootErr,
		home:    home,
		homeErr: homeErr,
		pathEnv: pathEnv,
		admit:   make(chan struct{}, 1),
		network: network,
	}
}

// ObserveHostTools implements sessions.HostToolObserver. Raw paths stay here:
// the observation carries closed codes only.
func (o *hostToolObserver) ObserveHostTools(ctx context.Context, driver, configuredProgram string) (sessions.HostToolObservation, error) {
	if err := ctx.Err(); err != nil {
		return sessions.HostToolObservation{}, err
	}
	select {
	case o.admit <- struct{}{}:
	case <-ctx.Done():
		return sessions.HostToolObservation{}, ctx.Err()
	}
	defer func() { <-o.admit }()
	if err := ctx.Err(); err != nil {
		return sessions.HostToolObservation{}, err
	}
	// The catalog's supported set decides first; another driver is never answered
	// with Claude's detector, and no filesystem error can hide that answer.
	paths, err := o.engine.ProviderDefaultPaths(driver, o.home)
	if err != nil {
		if toolinstall.KindOf(err) == toolinstall.KindUnsupportedProvider {
			return sessions.HostToolObservation{UnsupportedDriver: true}, nil
		}
		return sessions.HostToolObservation{}, err
	}
	if o.rootErr != nil {
		return sessions.HostToolObservation{}, o.rootErr
	}
	if o.homeErr != nil {
		return sessions.HostToolObservation{}, o.homeErr
	}
	cands, err := o.engine.Detect(ctx, toolinstall.DetectOptions{
		Driver: driver, Root: o.root, Home: o.home, PathEnv: o.pathEnv,
		Probe: false, ProbePaths: nil, Material: nil,
	})
	if err != nil {
		return sessions.HostToolObservation{}, err
	}
	// Managed-root errors other than absence already came back from Detect (List).
	// A dated observation across filesystem calls, not an atomic snapshot.
	if err := examineHostToolScope(driver, o.home, o.pathEnv, paths); err != nil {
		return sessions.HostToolObservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return sessions.HostToolObservation{}, err
	}
	configured := resolveConfiguredProgram(configuredProgram, o.pathEnv)
	out := sessions.HostToolObservation{Candidates: make([]sessions.HostToolCandidate, 0, len(cands))}
	for _, c := range cands {
		out.Candidates = append(out.Candidates, sessions.HostToolCandidate{
			Origin:     c.Origin,
			Match:      c.Match,
			Executable: c.Executable,
			Configured: configured.compare(c.Resolved),
		})
	}
	return out, nil
}

// examineHostToolScope re-examines the search locations Detect used for this
// driver: the catalog's vendor default paths under the captured home, the
// directory whose enumeration feeds them, and each absolute PATH entry (Detect
// ignores relative ones). A location is acceptable only when it is present or
// fs.ErrNotExist. It uses Lstat and one directory listing, so it never opens a
// FIFO, a candidate file or provider configuration, and it executes nothing.
func examineHostToolScope(driver, home, pathEnv string, defaultPaths []string) error {
	if driver == claudeHostToolDriver {
		if err := enumerableOrMissing(claudeVersionsDir(home)); err != nil {
			return err
		}
	}
	for _, path := range defaultPaths {
		if err := presentOrMissing(path); err != nil {
			return err
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if err := presentOrMissing(filepath.Join(dir, driver)); err != nil {
			return err
		}
	}
	return nil
}

func presentOrMissing(path string) error {
	if _, err := os.Lstat(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errHostToolScopeUnexamined
	}
	return nil
}

// enumerableOrMissing follows filepath.Glob's walk of one directory — Stat, and
// list only a directory — but keeps the errors Glob drops, from the open and from
// the listing alike. A path that exists and is not a directory yields no Glob
// matches, which is an observation, not a failure.
func enumerableOrMissing(dir string) error {
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return errHostToolScopeUnexamined
	case !fi.IsDir():
		return nil
	}
	if _, err := os.ReadDir(dir); err != nil {
		return errHostToolScopeUnexamined
	}
	return nil
}

const claudeHostToolDriver = "claude"

// claudeVersionsDir is the directory the Claude catalog's DefaultPaths globs for
// versioned executables (toolinstall/claude.go). Its own battery fails if the
// catalog stops enumerating it.
func claudeVersionsDir(home string) string {
	return filepath.Join(home, ".local", "share", "claude", "versions")
}

// configuredIdentity is the file the effective configured program resolves to,
// when that could be established. It is an observation, not a launch authority:
// the file can change before any launch.
type configuredIdentity struct {
	info os.FileInfo
}

// resolveConfiguredProgram follows exec.Command's rule (a bare name is searched
// on the captured PATH, anything with a separator is used as written) and stats
// the resolved file. Anything uncertain leaves the identity empty.
func resolveConfiguredProgram(program, pathEnv string) configuredIdentity {
	program = strings.TrimSpace(program)
	var file string
	switch {
	case program == "":
		return configuredIdentity{}
	case filepath.IsAbs(program):
		file = program
	case filepath.Base(program) != program:
		// Relative to whatever directory a future launch runs in.
		return configuredIdentity{}
	default:
		found, certain := lookPathIn(program, pathEnv)
		if !certain || found == "" {
			return configuredIdentity{}
		}
		file = found
	}
	fi, err := os.Stat(file)
	if err != nil || !fi.Mode().IsRegular() {
		return configuredIdentity{}
	}
	return configuredIdentity{info: fi}
}

// lookPathIn walks a captured PATH in order. It reports certain=false when an
// entry before any match could not be examined or is not absolute, because the
// launch's own resolution could then land on a different file.
func lookPathIn(name, pathEnv string) (string, bool) {
	for _, dir := range filepath.SplitList(pathEnv) {
		if !filepath.IsAbs(dir) {
			return "", false
		}
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		switch {
		case err == nil:
			if fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
				return p, true
			}
		case errors.Is(err, fs.ErrNotExist):
		default:
			return "", false
		}
	}
	return "", true
}

// compare answers same or different only from two successful stats of real
// files; every failure is unknown, never different.
func (o *hostToolObserver) latestProgram(driver string) string {
	if o == nil || o.rootErr != nil || o.root == "" {
		return ""
	}
	in, ok, err := o.engine.LatestInstalled(context.Background(), o.root, driver)
	if err != nil || !ok || strings.TrimSpace(in.Executable) == "" {
		return ""
	}
	return in.Executable
}

func (c configuredIdentity) compare(resolved string) string {
	if c.info == nil || resolved == "" {
		return sessions.HostToolCodeUnknown
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return sessions.HostToolCodeUnknown
	}
	if os.SameFile(c.info, fi) {
		return sessions.HostToolConfiguredSame
	}
	return sessions.HostToolConfiguredDifferent
}
