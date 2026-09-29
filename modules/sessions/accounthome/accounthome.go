// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package accounthome is the naming rule of a provider account's home: where
// the homes of a node live, which directory one account's home is, what a
// reader of an account is shown instead of the absolute path, and which
// components are refused before any of them reaches a filesystem call.
//
// It is pure on purpose. It opens nothing, creates nothing and stats nothing:
// it turns identities into relative paths and refuses the ones it cannot turn
// into a path safely. The module beside it does the filesystem work, through
// os.Root, and then asks the profile plane's own canonicalHome what the finished
// directory really is — there is exactly one home resolver and this package is
// not it.
//
// ⛔ A NAME IS NOT PART OF A PATH. An account's home is kept under the stable
// tuple of its tenant, its execution environment and its account reference. A
// name is a label an operator may change, so keeping it out of the path is what
// makes a rename a rename and not a move; and two tenants, or two environments,
// that happen to choose the same name never meet in one directory.
package accounthome

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// Home modes: who made the home, which decides who may archive or recreate it.
const (
	// ModeManaged is a home the product built under the accounts root.
	ModeManaged = "managed"
	// ModeAdopted is a home the operator already had, somewhere of their own
	// choosing. It has no path under the accounts root, and the product never
	// archives or recreates it.
	ModeAdopted = "adopted"
)

// The directories of one account's home, and the tree a build happens in.
const (
	// ConfigDir holds the driver's own configuration state.
	ConfigDir = "config"
	// UserDir is the HOME the launched child runs under. It is kept apart from
	// ConfigDir so a driver's state and its user home are never one directory.
	UserDir = "home"
	// StagingDir is where a home is built before it is published. It starts with
	// a dot, which no component this package accepts may do, so no account's
	// custody can ever be confused with it.
	StagingDir = ".staging"
	// accountsDir is what the accounts root is called under a data directory.
	accountsDir = "accounts"
)

// The file modes of the tree.
const (
	// HomeMode is private to the owner: a home, and the account directory that
	// holds one, are readable by nobody else.
	HomeMode fs.FileMode = 0o700
	// StagingMode is the same, because a half-built home is no more public than
	// a finished one.
	StagingMode fs.FileMode = 0o700
	// ParentMode permits traversal for other Unix users, not directory listing.
	// The service UID owns it and CAN list it; provider accounts share that UID.
	ParentMode fs.FileMode = 0o711
)

// maxComponent bounds one path component. It is well above every identity this
// package is handed and well below any filesystem's own limit.
const maxComponent = 255

var (
	// ErrRoot is wrapped by every refusal of an accounts root.
	ErrRoot = errors.New("unusable accounts root")
	// ErrComponent is wrapped by every refusal of a path component.
	ErrComponent = errors.New("unusable account home component")
	// ErrMode is wrapped when a home mode is not one this package knows.
	ErrMode = errors.New("unknown account home mode")
)

// Root reports the directory a node keeps its account homes under.
//
// A deployment that states its own root gets that root: the layer that
// configured it knows something the data directory does not — on an appliance,
// for instance, that account homes must sit outside the system snapshot so a
// rollback does not rewind a login. Every other node derives the root from its
// data directory, so one backup unit covers the store and the homes together.
//
// Both forms must be absolute. A relative root would be resolved against
// whatever directory the process happened to start in, which is the one answer
// that is never correct.
func Root(dataDir, configured string) (string, error) {
	if p := strings.TrimSpace(configured); p != "" {
		return absoluteRoot("the configured accounts root", p)
	}
	if p := strings.TrimSpace(dataDir); p != "" {
		base, err := absoluteRoot("the data directory", p)
		if err != nil {
			return "", err
		}
		return filepath.Join(base, accountsDir), nil
	}
	return "", fmt.Errorf("%w: this node states neither a data directory nor an accounts root", ErrRoot)
}

func absoluteRoot(what, p string) (string, error) {
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: %s contains a control character", ErrRoot, what)
		}
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%w: %s %q is not an absolute path", ErrRoot, what, p)
	}
	return filepath.Clean(p), nil
}

// Custody is the tuple an account's home is kept under for the whole life of
// the account. Every field is an identity the product minted or resolved, never
// a label an operator may change.
type Custody struct {
	// Tenant is the tenant the account belongs to.
	Tenant string
	// Environment is the execution environment whose node holds the directory.
	Environment string
	// AccountRef is the account's own reference, which is its profile's.
	AccountRef string
}

// Relative returns the account's home directory under the accounts root. It is
// the ONLY form of the path a reader of an account is shown: it says which
// directory the home is without saying where on the node the root sits.
func (c Custody) Relative() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	return path.Join(c.Tenant, c.Environment, c.AccountRef), nil
}

// ParentRelative returns the directory an account's home sits in: one per
// tenant and execution environment. Mode 0711 restricts listing by other Unix
// users, not by provider accounts running as the shared service UID.
func (c Custody) ParentRelative() (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	return path.Join(c.Tenant, c.Environment), nil
}

// ConfigRelative returns the account's configuration home.
func (c Custody) ConfigRelative() (string, error) { return c.under(ConfigDir) }

// UserRelative returns the account's user home.
func (c Custody) UserRelative() (string, error) { return c.under(UserDir) }

func (c Custody) under(dir string) (string, error) {
	rel, err := c.Relative()
	if err != nil {
		return "", err
	}
	return path.Join(rel, dir), nil
}

func (c Custody) validate() error {
	if err := component("the tenant", c.Tenant); err != nil {
		return err
	}
	if err := component("the execution environment", c.Environment); err != nil {
		return err
	}
	return component("the account reference", c.AccountRef)
}

// Staging returns the directory one build works in, named by the identity of
// that build and of no other. The identity is never reused, so a cleanup can
// only ever reach what its own operation made.
func Staging(operation string) (string, error) {
	if err := component("the build identity", operation); err != nil {
		return "", err
	}
	return path.Join(StagingDir, operation), nil
}

// RelativeFor is what an account of the given home mode shows a reader.
//
// A managed home is the product's own directory under the accounts root, so it
// has a relative path. An adopted home is the operator's directory somewhere
// else entirely: there is no path under the accounts root to show, and the
// answer is nothing at all rather than a guess.
func RelativeFor(mode string, c Custody) (string, error) {
	switch mode {
	case ModeManaged:
		return c.Relative()
	case ModeAdopted:
		return "", nil
	default:
		return "", fmt.Errorf("%w: %q is neither %s nor %s", ErrMode, mode, ModeManaged, ModeAdopted)
	}
}

// component refuses everything that could make a joined path mean something
// other than one directory inside the tree it was joined into.
//
// The execution-environment reference is why this is not paranoia: the profile
// plane accepts every printable character in it except ':' and '|', so '/',
// '\' and ".." all reach this package, and a component starting with '.' could
// name the staging tree itself. The refusal happens here, once, rather than in
// each caller that remembers to check.
func component(what, s string) error {
	if s == "" {
		return fmt.Errorf("%w: %s is empty", ErrComponent, what)
	}
	if len(s) > maxComponent {
		return fmt.Errorf("%w: %s is longer than %d bytes", ErrComponent, what, maxComponent)
	}
	if s == "." || s == ".." {
		return fmt.Errorf("%w: %s is a relative reference and not a name", ErrComponent, what)
	}
	if strings.HasPrefix(s, ".") {
		return fmt.Errorf("%w: %s starts with a dot, which only this package's own directories may do", ErrComponent, what)
	}
	for _, r := range s {
		switch {
		case r < 0x20, r == 0x7f:
			return fmt.Errorf("%w: %s contains a control character", ErrComponent, what)
		case r == '/', r == '\\':
			return fmt.Errorf("%w: %s contains a path separator, so it names more than one directory", ErrComponent, what)
		}
	}
	return nil
}
