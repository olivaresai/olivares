// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// dabConfinementCase is one RAW double: a chosen subset of the five optional
// transaction capabilities crossed with a chosen subset of the two authority
// ports. The double embeds store.Scope plus the named optional interfaces, so
// its capability set is EXACTLY the declared one — a capability the double does
// not name is genuinely absent, not merely unused.
//
// The 32 capability subsets are the 32 named workspaceConfined*Scope decorators,
// so the table below drives every arm of the single port-attachment step. That
// exhaustiveness is the point: an arm the switch forgot returns
// ErrWorkspaceConfinement at run time and compiles perfectly.
type dabConfinementCase struct {
	name      string
	raw       func(sc *tenantScope) store.Scope
	clock     bool
	locker    bool
	authority bool
	directory bool
	epoch     bool
	bundle    bool
	dab       bool
}

var dabConfinementCases = []dabConfinementCase{
	{
		name: "Bare/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
			}{sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "Bare/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotBundleLocker
			}{sc, sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "Bare/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "Bare/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "Clock/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
			}{sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "Clock/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "Clock/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "Clock/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "Locker/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
			}{sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "Locker/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "Locker/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "Locker/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "ClockLocker/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
			}{sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "ClockLocker/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "ClockLocker/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "ClockLocker/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "Authority/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
			}{sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "Authority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "Authority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "Authority/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "ClockAuthority/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "ClockAuthority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "ClockAuthority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "ClockAuthority/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "LockerAuthority/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "LockerAuthority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "LockerAuthority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "LockerAuthority/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "ClockLockerAuthority/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "ClockLockerAuthority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "ClockLockerAuthority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "ClockLockerAuthority/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "Directory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
			}{sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "Directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "Directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "Directory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "ClockDirectory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
			}{sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "ClockDirectory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "ClockDirectory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "ClockDirectory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "LockerDirectory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "LockerDirectory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "LockerDirectory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "LockerDirectory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "ClockLockerDirectory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "ClockLockerDirectory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "ClockLockerDirectory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "ClockLockerDirectory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "AuthorityDirectory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "AuthorityDirectory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "AuthorityDirectory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "AuthorityDirectory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "ClockAuthorityDirectory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "ClockAuthorityDirectory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "ClockAuthorityDirectory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "ClockAuthorityDirectory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "LockerAuthorityDirectory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "LockerAuthorityDirectory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "LockerAuthorityDirectory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "LockerAuthorityDirectory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "ClockLockerAuthorityDirectory/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: false,
		bundle: false, dab: false,
	},
	{
		name: "ClockLockerAuthorityDirectory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: false,
		bundle: true, dab: false,
	},
	{
		name: "ClockLockerAuthorityDirectory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: false,
		bundle: false, dab: true,
	},
	{
		name: "ClockLockerAuthorityDirectory/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: false,
		bundle: true, dab: true,
	},
	{
		name: "AuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
			}{sc, sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "AuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "AuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "AuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "LockerAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "LockerAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "LockerAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "LockerAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockLockerAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockLockerAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockLockerAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockLockerAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "AuthorityAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "AuthorityAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "AuthorityAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "AuthorityAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockAuthorityAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockAuthorityAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockAuthorityAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockAuthorityAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "LockerAuthorityAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "LockerAuthorityAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "LockerAuthorityAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "LockerAuthorityAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockLockerAuthorityAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockLockerAuthorityAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockLockerAuthorityAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockLockerAuthorityAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: false, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "DirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "DirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "DirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "DirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: false, directory: true, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockDirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockDirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockDirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockDirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: false, directory: true, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "LockerDirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "LockerDirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "LockerDirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "LockerDirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: false, directory: true, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockLockerDirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockLockerDirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockLockerDirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockLockerDirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: false, directory: true, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "AuthorityDirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "AuthorityDirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "AuthorityDirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "AuthorityDirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: false, locker: false, authority: true, directory: true, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockAuthorityDirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockAuthorityDirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockAuthorityDirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockAuthorityDirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: false, authority: true, directory: true, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "LockerAuthorityDirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "LockerAuthorityDirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "LockerAuthorityDirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "LockerAuthorityDirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		clock: false, locker: true, authority: true, directory: true, epoch: true,
		bundle: true, dab: true,
	},
	{
		name: "ClockLockerAuthorityDirectoryAuthorizationEpoch/neither",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: true,
		bundle: false, dab: false,
	},
	{
		name: "ClockLockerAuthorityDirectoryAuthorizationEpoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: true,
		bundle: true, dab: false,
	},
	{
		name: "ClockLockerAuthorityDirectoryAuthorizationEpoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: true,
		bundle: false, dab: true,
	},
	{
		name: "ClockLockerAuthorityDirectoryAuthorizationEpoch/both",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		clock: true, locker: true, authority: true, directory: true, epoch: true,
		bundle: true, dab: true,
	},
}

// TestDirectoryAuthorityConfinementOptionalityMatrix walks all 32 named
// decorators crossed with the four port sets (neither, bundle-only, DAB1-only,
// both) and requires the confined scope's capability set to equal the raw one
// EXACTLY: nothing lost, nothing manufactured, ErrWorkspaceConfinement never
// reached for a decorator the switch is supposed to know.
func TestDirectoryAuthorityConfinementOptionalityMatrix(t *testing.T) {
	if len(dabConfinementCases) != 128 {
		t.Fatalf("matrix has %d cases, want 32 decorators x 4 port sets", len(dabConfinementCases))
	}
	ctx := context.Background()
	s, _, _, tenants := f2aFreshTarget(t, store.EngineSQLite)
	tenant := tenants[0]

	if err := s.Mutate(ctx, tenant, func(raw store.Scope) error {
		sc := raw.(*tenantScope)
		ws, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		for _, c := range dabConfinementCases {
			t.Run(c.name, func(t *testing.T) {
				confined, err := store.ConfineWorkspace(ctx, c.raw(sc), ws.ID)
				if err != nil {
					t.Fatalf("confine: %v", err)
				}
				assertDABCapabilities(t, confined, c)

				// Same-workspace re-confinement is idempotent and keeps every
				// selected port. A guard that returned early on the FIRST present
				// port would silently drop the other one right here.
				twice, err := store.ConfineWorkspace(ctx, confined, ws.ID)
				if err != nil {
					t.Fatalf("re-confine: %v", err)
				}
				if twice != confined {
					t.Error("same-workspace re-confinement was not idempotent")
				}
				assertDABCapabilities(t, twice, c)

				// A different workspace is a refusal, never a retargeting.
				if got, err := store.ConfineWorkspace(ctx, confined, model.NewID()); got != nil ||
					!errors.Is(err, store.ErrWorkspaceConfinement) {
					t.Errorf("cross-workspace confinement: scope=%v err=%v", got, err)
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("matrix transaction: %v", err)
	}
}

func assertDABCapabilities(t *testing.T, confined store.Scope, c dabConfinementCase) {
	t.Helper()
	_, clock := confined.(store.TransactionClock)
	_, locker := confined.(store.TransactionLocker)
	_, authority := confined.(store.AuthoritySnapshotLocker)
	_, directory := confined.(store.DirectorySnapshotReader)
	_, epoch := confined.(store.AuthorizationEpochStore)
	_, bundle := confined.(store.AuthoritySnapshotBundleLocker)
	_, dab := confined.(store.DirectoryAuthoritySnapshotLocker)
	for _, got := range []struct {
		what       string
		have, want bool
	}{
		{"TransactionClock", clock, c.clock},
		{"TransactionLocker", locker, c.locker},
		{"AuthoritySnapshotLocker", authority, c.authority},
		{"DirectorySnapshotReader", directory, c.directory},
		{"AuthorizationEpochStore", epoch, c.epoch},
		{"AuthoritySnapshotBundleLocker", bundle, c.bundle},
		{"DirectoryAuthoritySnapshotLocker", dab, c.dab},
	} {
		if got.have != got.want {
			verb := "manufactured"
			if got.want {
				verb = "lost"
			}
			t.Errorf("confinement %s %s", verb, got.what)
		}
	}
}

// TestDirectoryAuthorityThroughConfinement is the end-to-end half of the
// forwarding contract: the production shape (an ordinary SQL scope, which
// carries every optional capability and both ports) admits DAB1 THROUGH the
// confined handle, and the confinement it travels with is unchanged — a
// lineage-less tenant repository is still refused, and the workspace boundary
// still applies.
func TestDirectoryAuthorityThroughConfinement(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			s, _, users, tenants := f2aFreshTarget(t, engine)
			tenant, admin := tenants[0], users[0]
			live := ataFacts(t, s, tenant, admin.ID)
			epochBefore, hBefore := ataObserve(t, s, tenant, admin.ID)

			if err := s.Mutate(ctx, tenant, func(raw store.Scope) error {
				ws, err := raw.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				confined, err := store.ConfineWorkspace(ctx, raw, ws.ID)
				if err != nil {
					return err
				}
				barrier, ok := confined.(store.DirectoryAuthoritySnapshotLocker)
				if !ok {
					t.Fatal("confined ordinary scope lost the DAB1 port")
				}
				if _, ok := confined.(store.AuthoritySnapshotBundleLocker); !ok {
					t.Error("confined ordinary scope lost the bundle port")
				}
				if err := barrier.LockDirectoryAuthoritySnapshot(ctx, live); err != nil {
					return err
				}
				// The confinement is untouched by the new port: a tenant entity
				// that declares no workspace lineage is still refused outright.
				if _, _, err := confined.Identities().List(ctx, model.Query{}); !errors.Is(
					err, store.ErrWorkspaceLineageRequired) {
					t.Errorf("tenant repository widened: %v", err)
				}
				_, err = dabMarker(ctx, raw, "dab-confined")
				return err
			}); err != nil {
				t.Fatalf("confined admission: %v", err)
			}

			if got := dabCountMarkers(t, s, tenant, "dab-confined"); got != 1 {
				t.Errorf("markers=%d, want 1", got)
			}
			epochAfter, hAfter := ataObserve(t, s, tenant, admin.ID)
			if epochAfter != epochBefore {
				t.Errorf("confined admission bumped the generation: %d -> %d", epochBefore, epochAfter)
			}
			if len(hAfter) != len(hBefore) || hAfter[0] != hBefore[0] {
				t.Errorf("confined admission bumped H: %v -> %v", hBefore, hAfter)
			}
		})
	}
}

// TestDirectoryAuthorityNotPromotedToNamedWrappers keeps the port off the two
// scopes that hold their tenantScope in a NAMED field. Both would expose the
// business barrier from a SYSTEM or evidence-only context if they ever embedded
// it, and a comma-ok that answered true there is exactly the capability
// overclaim the auth partition's own wrappers were built to avoid.
func TestDirectoryAuthorityNotPromotedToNamedWrappers(t *testing.T) {
	ctx := context.Background()
	s, _, _, tenants := f2aFreshTarget(t, store.EngineSQLite)

	if err := s.AuthMutate(ctx, func(as store.AuthScope) error {
		if _, ok := as.(store.DirectoryAuthoritySnapshotLocker); ok {
			t.Error("AuthScope gained the ordinary directory-authority barrier")
		}
		if _, ok := as.(store.AuthoritySnapshotBundleLocker); ok {
			t.Error("AuthScope gained the tenant bundle locker")
		}
		return nil
	}); err != nil {
		t.Fatalf("auth mutate: %v", err)
	}
	if err := s.Custody(ctx, tenants[0], func(cs store.CustodyScope) error {
		if _, ok := cs.(store.DirectoryAuthoritySnapshotLocker); ok {
			t.Error("CustodyScope gained the directory-authority barrier")
		}
		return nil
	}); err != nil {
		t.Fatalf("custody: %v", err)
	}
}

// TestDirectoryAuthorityPortAttachmentIsSingleStep pins the structural premise
// the whole file rests on: after ConfineWorkspace, an ordinary SQL scope is an
// ANONYMOUS struct, not one of the 32 named decorators. A second attachment
// step chained after this one would therefore match no case and fail closed for
// every confined ordinary scope.
func TestDirectoryAuthorityPortAttachmentIsSingleStep(t *testing.T) {
	ctx := context.Background()
	s, _, _, tenants := f2aFreshTarget(t, store.EngineSQLite)
	if err := s.Mutate(ctx, tenants[0], func(raw store.Scope) error {
		ws, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		confined, err := store.ConfineWorkspace(ctx, raw, ws.ID)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(fmt.Sprintf("%T", confined), "*")
		if strings.HasPrefix(name, "store.workspaceConfined") {
			t.Fatalf("confined scope is still the named decorator %q: the premise of the "+
				"single-step attachment no longer holds", name)
		}
		if !strings.HasPrefix(name, "struct") {
			t.Fatalf("confined scope type %q is neither a named decorator nor an "+
				"anonymous port struct", name)
		}
		return nil
	}); err != nil {
		t.Fatalf("single-step premise: %v", err)
	}
}
