// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import "errors"

// Mounts names paths of a writable grant that the child must see read-only. A
// Landlock right on a directory applies to everything beneath it, so a grant
// that contains a path cannot also deny it; a mount can. They are applied before
// Landlock, in a user and mount namespace of the child's own.
type Mounts struct {
	// ReadOnly are directories or files bound read-only: the child cannot create,
	// change, remove, rename or replace anything beneath them. The mount sits on
	// the node's own entry in its directory, so it lasts as long as the node: when
	// something outside the child's namespace deletes the node or renames another
	// over it (git replaces .git/config that way), the kernel detaches the mount
	// there and the child reaches the new node, writable.
	ReadOnly []string
	// Pin are directories or files bound onto themselves, still writable, so the
	// child cannot rename or remove the path to put another one in its place.
	Pin []string
	// Create are nodes to make, when missing, once the host is known to apply
	// the mounts: a name the child could otherwise create itself, for a program
	// that reads it. Each is also listed in ReadOnly. They are not sent to the
	// helper.
	Create []Node `json:"-"`
}

// Node is a directory, or a file with Content, that WrapMounts makes when it is
// missing.
type Node struct {
	Path    string
	Dir     bool
	Content string
}

// ErrMountsUnavailable is wrapped by WrapMounts when this host cannot give the
// child a mount namespace. The child can still be started without the mounts.
var ErrMountsUnavailable = errors.New("confine: this host does not allow unprivileged user and mount namespaces")

const (
	mountsArg      = "--mounts-v1"
	mountsProbeArg = "--mounts-probe-v1"
)
