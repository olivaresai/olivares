// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/accounthome"
)

const accountCustodyMarker = ".olivares-account-operation"

// plannedAccountRoot resolves a configured alias without creating anything.
// Missing suffixes are joined to the nearest existing canonical ancestor.
func plannedAccountRoot(configured string) (string, error) {
	tail := []string{}
	p := filepath.Clean(configured)
	for {
		resolved, err := filepath.EvalSymlinks(p)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		// A dangling symlink must not be interpreted as a missing directory.
		if st, e := os.Lstat(p); e == nil && st.Mode()&fs.ModeSymlink != 0 {
			return "", errAccountHomeOccupied
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		tail = append(tail, filepath.Base(p))
		p = parent
	}
}

// openAccountRoot creates missing canonical components through one root handle.
// It never chmods an existing directory. Aliases are checked again after open.
func openAccountRoot(configured, canonical string) (*os.Root, error) {
	planned, err := plannedAccountRoot(configured)
	if err != nil || planned != canonical {
		return nil, errAccountHomeOccupied
	}
	volume := filepath.VolumeName(canonical) + string(filepath.Separator)
	r, err := os.OpenRoot(volume)
	if err != nil {
		return nil, ErrAccountsRootUnavailable
	}
	defer r.Close()
	rel, err := filepath.Rel(volume, canonical)
	if err != nil {
		return nil, errAccountHomeOccupied
	}
	path := ""
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		path = filepath.Join(path, part)
		if err := ensureAccountDir(r, path, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, ErrAccountsRootUnavailable
	}
	if err := verifyAccountRoot(root, configured, canonical); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}
func verifyAccountRoot(root *os.Root, configured, canonical string) error {
	actual, err := filepath.EvalSymlinks(configured)
	if err != nil || actual != canonical {
		return errAccountHomeOccupied
	}
	held, err := root.Stat(".")
	if err != nil {
		return errAccountHomeOccupied
	}
	named, err := os.Stat(canonical)
	if err != nil || !os.SameFile(held, named) {
		return errAccountHomeOccupied
	}
	return nil
}
func syncAccountDir(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func accountDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errAccountHomeOccupied
	}
	return nil
}
func ensureAccountDir(root *os.Root, name string, mode fs.FileMode) error {
	err := root.Mkdir(name, mode)
	if errors.Is(err, fs.ErrExist) {
		return accountDirectory(root, name)
	}
	if err != nil {
		return err
	}
	if err := root.Chmod(name, mode); err != nil {
		return err
	}
	return syncAccountDir(root, filepath.Dir(name))
}

// acquireAccountDirectory proves custody by exclusive acquisition and a marker
// bound to the durable operation. An incomplete marker is ambiguous, not ours.
func acquireAccountDirectory(root *os.Root, name, marker string) error {
	err := root.Mkdir(name, 0700)
	if errors.Is(err, fs.ErrExist) {
		if err := accountDirectory(root, name); err != nil {
			return err
		}
		owned, err := root.Lstat(name)
		if err != nil || owned.Mode().Perm() != 0700 {
			return errAccountHomeOccupied
		}
		f, err := root.Open(filepath.Join(name, accountCustodyMarker))
		if err != nil {
			return errAccountHomeOccupied
		}
		defer f.Close()
		info, err := root.Lstat(filepath.Join(name, accountCustodyMarker))
		if err != nil || (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
			return errAccountHomeOccupied
		}
		data, err := io.ReadAll(io.LimitReader(f, 513))
		if err != nil || string(data) != marker {
			return errAccountHomeOccupied
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := root.Chmod(name, 0700); err != nil {
		return err
	}
	f, err := root.OpenFile(filepath.Join(name, accountCustodyMarker), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errAccountHomeOccupied
	}
	_, writeErr := io.WriteString(f, marker)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := syncAccountDir(root, name); err != nil {
		return err
	}
	return syncAccountDir(root, filepath.Dir(name))
}

func (m *Module) materializeAccountHome(tenant model.TenantID, op model.Record) ([2]string, error) {
	var homes [2]string
	canonical := op.String(colHORoot)
	root, err := openAccountRoot(m.accountsRoot, canonical)
	if err != nil {
		return homes, err
	}
	defer root.Close()
	c := accounthome.Custody{Tenant: tenant.String(), Environment: op.String(colHOEnv), AccountRef: op.String(colHORef)}
	final, err := c.Relative()
	if err != nil {
		return homes, accountHomeComponentRefusal(err)
	}
	stage, err := accounthome.Staging(op.String(colHOToken))
	if err != nil {
		return homes, errAccountHomeOccupied
	}
	marker := op.String(colHOToken) + "\n" + tenant.String() + "\n" + op.String(colHOEnv) + "\n" + op.String(colHORef) + "\n"
	for _, parent := range []string{accounthome.StagingDir, c.Tenant, filepath.Join(c.Tenant, c.Environment)} {
		mode := accounthome.ParentMode
		if parent == accounthome.StagingDir {
			mode = accounthome.StagingMode
		}
		if err := ensureAccountDir(root, parent, mode); err != nil {
			return homes, err
		}
	}
	if err := acquireAccountDirectory(root, stage, marker); err != nil {
		return homes, err
	}
	// Leaves already moved to final are never recreated in staging on a retry.
	for _, leaf := range []string{accounthome.ConfigDir, accounthome.UserDir} {
		if _, err := root.Lstat(filepath.Join(final, leaf)); errors.Is(err, fs.ErrNotExist) {
			if err := ensureAccountDir(root, filepath.Join(stage, leaf), 0700); err != nil {
				return homes, err
			}
		} else if err != nil {
			return homes, err
		}
	}
	if err := m.accountHomePoint("staged"); err != nil {
		return homes, err
	}
	if err := verifyAccountRoot(root, m.accountsRoot, canonical); err != nil {
		return homes, err
	}
	if err := acquireAccountDirectory(root, final, marker); err != nil {
		return homes, err
	}
	for _, leaf := range []string{accounthome.ConfigDir, accounthome.UserDir} {
		to, from := filepath.Join(final, leaf), filepath.Join(stage, leaf)
		if err := accountDirectory(root, to); errors.Is(err, fs.ErrNotExist) {
			if err := accountDirectory(root, from); err != nil {
				return homes, err
			}
			if err := root.Rename(from, to); err != nil {
				return homes, err
			}
		} else if err != nil {
			return homes, err
		}
	}
	if op.String(colHODriver) == providerDriverGemini {
		if err := ensureAccountDir(root, filepath.Join(final, accounthome.ConfigDir, ".gemini"), 0700); err != nil {
			return homes, err
		}
	}
	// Capture the actual published leaves before any interruption callback.
	// Re-resolving two pathnames later would only prove that both now name the
	// same replacement, not that the published inode survived.
	var published [2]fs.FileInfo
	for i, leaf := range []string{accountConfigLeaf(op.String(colHODriver)), accounthome.UserDir} {
		published[i], err = root.Lstat(filepath.Join(final, leaf))
		if err != nil || !published[i].IsDir() {
			return homes, errAccountHomeOccupied
		}
	}
	if err := syncAccountDir(root, stage); err != nil {
		return homes, err
	}
	if err := syncAccountDir(root, final); err != nil {
		return homes, err
	}
	if err := m.accountHomePoint("published"); err != nil {
		return homes, err
	}
	if err := verifyAccountRoot(root, m.accountsRoot, canonical); err != nil {
		return homes, err
	}
	for i, leaf := range []string{accountConfigLeaf(op.String(colHODriver)), accounthome.UserDir} {
		relative := filepath.Join(final, leaf)
		if err := accountDirectory(root, relative); err != nil {
			return homes, err
		}
		homes[i], err = canonicalHome(leaf, filepath.Join(canonical, relative))
		if err != nil {
			return homes, err
		}
		held, e1 := root.Stat(relative)
		named, e2 := os.Stat(homes[i])
		if e1 != nil || e2 != nil || !os.SameFile(held, named) || !os.SameFile(published[i], named) || homes[i] != filepath.Join(canonical, relative) {
			return homes, errAccountHomeOccupied
		}
	}
	return homes, nil
}

// Account custody keeps its config leaf; Gemini selects the native directory inside it.
func accountConfigLeaf(driver string) string {
	if driver == providerDriverGemini {
		return filepath.Join(accounthome.ConfigDir, ".gemini")
	}
	return accounthome.ConfigDir
}
