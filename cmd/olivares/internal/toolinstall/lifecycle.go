// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// InstalledVersion resolves only a verified, executable release for this host.
// A missing or damaged pin never falls back to a different installed version.
func (e *Engine) InstalledVersion(ctx context.Context, root, driver, version string) (Installed, error) {
	if !ValidVersion(version) {
		return Installed{}, refuse(KindInvalidRequest, "choose an exact installed release version")
	}
	if _, err := e.Capability(driver); err != nil {
		return Installed{}, err
	}
	inv, err := e.list(ctx, root, driver)
	if err != nil {
		return Installed{}, err
	}
	host := HostPlatform()
	var selected *Installed
	for i := range inv.Installed {
		in := &inv.Installed[i]
		if in.Driver != driver || in.Version != version || in.Platform.OS != host.OS || in.Platform.Arch != host.Arch || (in.Platform.Libc != "" && in.Platform.Libc != host.Libc) {
			continue
		}
		if in.State != StateInstalled || !in.IsExecutable {
			return Installed{}, refuse(KindConflict, "release %s %s is not verified and executable: %s", driver, version, in.Reason)
		}
		if selected != nil {
			return Installed{}, refuse(KindConflict, "more than one verified host release matches %s %s", driver, version)
		}
		selected = in
	}
	if selected == nil {
		return Installed{}, refuse(KindConflict, "release %s %s is not installed for this host; install it or clear its version pin", driver, version)
	}
	return *selected, nil
}

// Remove requires the host's session admission and retained audit before effects.
// The existing install lock serializes inspection and removal against installs.
func (e *Engine) Remove(ctx context.Context, rootPath, driver, version string, admit func(Installed) (func(), error)) (Installed, error) {
	if admit == nil {
		return Installed{}, refuse(KindInvalidRequest, "release removal requires session admission")
	}
	root, err := openRoot(rootPath, false)
	if err != nil {
		return Installed{}, err
	}
	defer root.Close()
	unlock, err := lockRoot(root, rootPath)
	if err != nil {
		return Installed{}, err
	}
	defer unlock()
	in, err := e.InstalledVersion(ctx, rootPath, driver, version)
	if err != nil {
		return Installed{}, err
	}
	rel, err := filepath.Rel(rootPath, in.ReleaseDir)
	if err != nil || rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || filepath.Dir(rel) != driver {
		return Installed{}, refuse(KindDestinationUnsafe, "release is outside its managed driver directory")
	}
	fi, err := root.Lstat(rel)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return Installed{}, refuse(KindDestinationUnsafe, "release directory is unsafe")
	}
	release, err := admit(in)
	if err != nil {
		return Installed{}, err
	}
	if release == nil {
		return Installed{}, refuse(KindInvalidRequest, "release removal admission did not hold the launch lock")
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return Installed{}, err
	}
	if err := root.RemoveAll(rel); err != nil {
		return Installed{}, refuse(KindDestinationNotWritable, "remove release: %v", err)
	}
	e.ForgetVerified(driver)
	if err := syncDir(root, driver); err != nil {
		return in, err
	}
	return in, nil
}
