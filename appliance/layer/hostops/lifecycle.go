// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Role is who is holding the lifecycle lock. The portal may hold it shared
// and may not write it or replace it. The initializer creates it once.
type Role string

const (
	// RolePortal is the console. It cannot take the lock exclusively.
	RolePortal Role = "portal"
	// RoleInitializer creates the lock and may hold it exclusively.
	RoleInitializer Role = "initializer"
)

// Lifecycle is one open lifecycle lock. Closing it releases the hold.
type Lifecycle struct {
	role Role
	file *os.File
}

// InitLifecycle creates the lifecycle lock if it is absent and otherwise
// opens the existing file. It does not replace the file.
func InitLifecycle(dir string) error {
	if dir == "" {
		return errors.New("the lifecycle directory is missing")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(dir, "lifecycle.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o640)
	if errors.Is(err, os.ErrExist) {
		existing, openErr := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if openErr != nil {
			return openErr
		}
		return existing.Close()
	}
	if err != nil {
		return err
	}
	if err := f.Chmod(0o640); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// OpenLifecycle opens the existing lifecycle lock for role. It does not
// create or replace the file.
func OpenLifecycle(dir string, role Role) (*Lifecycle, error) {
	var flag int
	switch role {
	case RolePortal:
		flag = os.O_RDONLY | syscall.O_NOFOLLOW
	case RoleInitializer:
		flag = os.O_RDWR | syscall.O_NOFOLLOW
	default:
		return nil, errors.New("the lifecycle role is not one of the closed roles")
	}
	f, err := os.OpenFile(filepath.Join(dir, "lifecycle.lock"), flag, 0)
	if err != nil {
		return nil, err
	}
	return &Lifecycle{role: role, file: f}, nil
}

// Exclusive takes the lifecycle lock for the initializer. The portal is
// refused before any lock is taken: a read-only open is not what stops an
// exclusive hold.
func (l *Lifecycle) Exclusive() error {
	if l == nil || l.file == nil {
		return errors.New("the lifecycle lock is not open")
	}
	if l.role == RolePortal {
		return errors.New("the portal holds the lifecycle lock shared, never exclusive")
	}
	return lockFile(l.file)
}

// Shared takes the lifecycle lock shared.
func (l *Lifecycle) Shared() error {
	if l == nil || l.file == nil {
		return errors.New("the lifecycle lock is not open")
	}
	return lockShared(l.file)
}

// CanWrite reports whether this role may write the lock file. The portal
// cannot.
func (l *Lifecycle) CanWrite() bool {
	return l != nil && l.role == RoleInitializer
}

// Close releases the file. It does not remove the lock.
func (l *Lifecycle) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// TryShared takes only a shared, nonblocking hold. The portal refuses a read
// during exclusive maintenance instead of retaining an unbounded lock waiter.
func (l *Lifecycle) TryShared() error {
	if l == nil || l.file == nil {
		return errors.New("the lifecycle lock is not open")
	}
	return tryLockShared(l.file)
}
