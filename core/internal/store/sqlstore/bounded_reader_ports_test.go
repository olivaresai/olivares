// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Generated matrix: 32 named decorators x 4 authority port sets x the
// independent bounded-read factory bit = 256 capability sets.
type boundedPortCase struct {
	name string
	raw  func(sc *tenantScope) store.Scope
	want [8]bool
}

var boundedPortCases = []boundedPortCase{
	{
		name: "none",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
			}{sc}
		},
		want: [8]bool{false, false, false, false, false, false, false, false},
	},
	{
		name: "clock",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
			}{sc, sc}
		},
		want: [8]bool{true, false, false, false, false, false, false, false},
	},
	{
		name: "locker",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
			}{sc, sc}
		},
		want: [8]bool{false, true, false, false, false, false, false, false},
	},
	{
		name: "clock/locker",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
			}{sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, false, false, false},
	},
	{
		name: "authority",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
			}{sc, sc}
		},
		want: [8]bool{false, false, true, false, false, false, false, false},
	},
	{
		name: "clock/authority",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, false, false, false},
	},
	{
		name: "locker/authority",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, false, false, false},
	},
	{
		name: "clock/locker/authority",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, false, false, false, false},
	},
	{
		name: "directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
			}{sc, sc}
		},
		want: [8]bool{false, false, false, true, false, false, false, false},
	},
	{
		name: "clock/directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
			}{sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, false, false, false},
	},
	{
		name: "locker/directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, false, false, false},
	},
	{
		name: "clock/locker/directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, false, false, false, false},
	},
	{
		name: "authority/directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, false, false, false},
	},
	{
		name: "clock/authority/directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, false, false, false, false},
	},
	{
		name: "locker/authority/directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, false, false, false, false},
	},
	{
		name: "clock/locker/authority/directory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, false, false, false, false},
	},
	{
		name: "epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
			}{sc, sc}
		},
		want: [8]bool{false, false, false, false, true, false, false, false},
	},
	{
		name: "clock/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, false, false, false},
	},
	{
		name: "locker/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, false, false, false},
	},
	{
		name: "clock/locker/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, true, false, false, false},
	},
	{
		name: "authority/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, false, false, false},
	},
	{
		name: "clock/authority/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, true, false, false, false},
	},
	{
		name: "locker/authority/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, true, false, false, false},
	},
	{
		name: "clock/locker/authority/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, true, false, false, false},
	},
	{
		name: "directory/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, false, false, false},
	},
	{
		name: "clock/directory/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, true, false, false, false},
	},
	{
		name: "locker/directory/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, true, false, false, false},
	},
	{
		name: "clock/locker/directory/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, true, false, false, false},
	},
	{
		name: "authority/directory/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, true, false, false, false},
	},
	{
		name: "clock/authority/directory/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, true, false, false, false},
	},
	{
		name: "locker/authority/directory/epoch",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, true, false, false, false},
	},
	{
		name: "clock/locker/authority/directory/epoch",
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
		want: [8]bool{true, true, true, true, true, false, false, false},
	},
	{
		name: "bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotBundleLocker
			}{sc, sc}
		},
		want: [8]bool{false, false, false, false, false, true, false, false},
	},
	{
		name: "clock/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, false, true, false, false},
	},
	{
		name: "locker/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, false, true, false, false},
	},
	{
		name: "clock/locker/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, true, false, false},
	},
	{
		name: "authority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, false, true, false, false},
	},
	{
		name: "clock/authority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, true, false, false},
	},
	{
		name: "locker/authority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, true, false, false},
	},
	{
		name: "clock/locker/authority/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, false, true, false, false},
	},
	{
		name: "directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, false, true, false, false},
	},
	{
		name: "clock/directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, true, false, false},
	},
	{
		name: "locker/directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, true, false, false},
	},
	{
		name: "clock/locker/directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, false, true, false, false},
	},
	{
		name: "authority/directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, true, false, false},
	},
	{
		name: "clock/authority/directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, false, true, false, false},
	},
	{
		name: "locker/authority/directory/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, false, true, false, false},
	},
	{
		name: "clock/locker/authority/directory/bundle",
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
		want: [8]bool{true, true, true, true, false, true, false, false},
	},
	{
		name: "epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, true, true, false, false},
	},
	{
		name: "clock/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, true, false, false},
	},
	{
		name: "locker/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, true, false, false},
	},
	{
		name: "clock/locker/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, true, true, false, false},
	},
	{
		name: "authority/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, true, false, false},
	},
	{
		name: "clock/authority/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, true, true, false, false},
	},
	{
		name: "locker/authority/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, true, true, false, false},
	},
	{
		name: "clock/locker/authority/epoch/bundle",
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
		want: [8]bool{true, true, true, false, true, true, false, false},
	},
	{
		name: "directory/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, true, false, false},
	},
	{
		name: "clock/directory/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, true, true, false, false},
	},
	{
		name: "locker/directory/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, true, true, false, false},
	},
	{
		name: "clock/locker/directory/epoch/bundle",
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
		want: [8]bool{true, true, false, true, true, true, false, false},
	},
	{
		name: "authority/directory/epoch/bundle",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, true, true, false, false},
	},
	{
		name: "clock/authority/directory/epoch/bundle",
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
		want: [8]bool{true, false, true, true, true, true, false, false},
	},
	{
		name: "locker/authority/directory/epoch/bundle",
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
		want: [8]bool{false, true, true, true, true, true, false, false},
	},
	{
		name: "clock/locker/authority/directory/epoch/bundle",
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
		want: [8]bool{true, true, true, true, true, true, false, false},
	},
	{
		name: "dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc}
		},
		want: [8]bool{false, false, false, false, false, false, true, false},
	},
	{
		name: "clock/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, false, false, true, false},
	},
	{
		name: "locker/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, false, false, true, false},
	},
	{
		name: "clock/locker/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, false, true, false},
	},
	{
		name: "authority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, false, false, true, false},
	},
	{
		name: "clock/authority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, false, true, false},
	},
	{
		name: "locker/authority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, false, true, false},
	},
	{
		name: "clock/locker/authority/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, false, false, true, false},
	},
	{
		name: "directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, false, false, true, false},
	},
	{
		name: "clock/directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, false, true, false},
	},
	{
		name: "locker/directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, false, true, false},
	},
	{
		name: "clock/locker/directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, false, false, true, false},
	},
	{
		name: "authority/directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, false, true, false},
	},
	{
		name: "clock/authority/directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, false, false, true, false},
	},
	{
		name: "locker/authority/directory/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, false, false, true, false},
	},
	{
		name: "clock/locker/authority/directory/dab",
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
		want: [8]bool{true, true, true, true, false, false, true, false},
	},
	{
		name: "epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, true, false, true, false},
	},
	{
		name: "clock/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, false, true, false},
	},
	{
		name: "locker/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, false, true, false},
	},
	{
		name: "clock/locker/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, true, false, true, false},
	},
	{
		name: "authority/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, false, true, false},
	},
	{
		name: "clock/authority/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, true, false, true, false},
	},
	{
		name: "locker/authority/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, true, false, true, false},
	},
	{
		name: "clock/locker/authority/epoch/dab",
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
		want: [8]bool{true, true, true, false, true, false, true, false},
	},
	{
		name: "directory/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, false, true, false},
	},
	{
		name: "clock/directory/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, true, false, true, false},
	},
	{
		name: "locker/directory/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, true, false, true, false},
	},
	{
		name: "clock/locker/directory/epoch/dab",
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
		want: [8]bool{true, true, false, true, true, false, true, false},
	},
	{
		name: "authority/directory/epoch/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, true, false, true, false},
	},
	{
		name: "clock/authority/directory/epoch/dab",
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
		want: [8]bool{true, false, true, true, true, false, true, false},
	},
	{
		name: "locker/authority/directory/epoch/dab",
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
		want: [8]bool{false, true, true, true, true, false, true, false},
	},
	{
		name: "clock/locker/authority/directory/epoch/dab",
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
		want: [8]bool{true, true, true, true, true, false, true, false},
	},
	{
		name: "bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, false, true, true, false},
	},
	{
		name: "clock/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, false, true, true, false},
	},
	{
		name: "locker/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, false, true, true, false},
	},
	{
		name: "clock/locker/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, true, true, false},
	},
	{
		name: "authority/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, false, true, true, false},
	},
	{
		name: "clock/authority/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, true, true, false},
	},
	{
		name: "locker/authority/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, true, true, false},
	},
	{
		name: "clock/locker/authority/bundle/dab",
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
		want: [8]bool{true, true, true, false, false, true, true, false},
	},
	{
		name: "directory/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, false, true, true, false},
	},
	{
		name: "clock/directory/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, true, true, false},
	},
	{
		name: "locker/directory/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, true, true, false},
	},
	{
		name: "clock/locker/directory/bundle/dab",
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
		want: [8]bool{true, true, false, true, false, true, true, false},
	},
	{
		name: "authority/directory/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, true, true, false},
	},
	{
		name: "clock/authority/directory/bundle/dab",
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
		want: [8]bool{true, false, true, true, false, true, true, false},
	},
	{
		name: "locker/authority/directory/bundle/dab",
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
		want: [8]bool{false, true, true, true, false, true, true, false},
	},
	{
		name: "clock/locker/authority/directory/bundle/dab",
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
		want: [8]bool{true, true, true, true, false, true, true, false},
	},
	{
		name: "epoch/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, true, true, true, false},
	},
	{
		name: "clock/epoch/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, true, true, false},
	},
	{
		name: "locker/epoch/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, true, true, false},
	},
	{
		name: "clock/locker/epoch/bundle/dab",
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
		want: [8]bool{true, true, false, false, true, true, true, false},
	},
	{
		name: "authority/epoch/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, true, true, false},
	},
	{
		name: "clock/authority/epoch/bundle/dab",
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
		want: [8]bool{true, false, true, false, true, true, true, false},
	},
	{
		name: "locker/authority/epoch/bundle/dab",
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
		want: [8]bool{false, true, true, false, true, true, true, false},
	},
	{
		name: "clock/locker/authority/epoch/bundle/dab",
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
		want: [8]bool{true, true, true, false, true, true, true, false},
	},
	{
		name: "directory/epoch/bundle/dab",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, true, true, false},
	},
	{
		name: "clock/directory/epoch/bundle/dab",
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
		want: [8]bool{true, false, false, true, true, true, true, false},
	},
	{
		name: "locker/directory/epoch/bundle/dab",
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
		want: [8]bool{false, true, false, true, true, true, true, false},
	},
	{
		name: "clock/locker/directory/epoch/bundle/dab",
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
		want: [8]bool{true, true, false, true, true, true, true, false},
	},
	{
		name: "authority/directory/epoch/bundle/dab",
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
		want: [8]bool{false, false, true, true, true, true, true, false},
	},
	{
		name: "clock/authority/directory/epoch/bundle/dab",
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
		want: [8]bool{true, false, true, true, true, true, true, false},
	},
	{
		name: "locker/authority/directory/epoch/bundle/dab",
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
		want: [8]bool{false, true, true, true, true, true, true, false},
	},
	{
		name: "clock/locker/authority/directory/epoch/bundle/dab",
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
		want: [8]bool{true, true, true, true, true, true, true, false},
	},
	{
		name: "factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.BoundedReaderFactory
			}{sc, sc}
		},
		want: [8]bool{false, false, false, false, false, false, false, true},
	},
	{
		name: "clock/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.BoundedReaderFactory
			}{sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, false, false, false, true},
	},
	{
		name: "locker/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.BoundedReaderFactory
			}{sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, false, false, false, true},
	},
	{
		name: "clock/locker/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, false, false, true},
	},
	{
		name: "authority/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, false, false, false, true},
	},
	{
		name: "clock/authority/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, false, false, true},
	},
	{
		name: "locker/authority/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, false, false, true},
	},
	{
		name: "clock/locker/authority/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, false, false, false, true},
	},
	{
		name: "directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, false, false, false, true},
	},
	{
		name: "clock/directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, false, false, true},
	},
	{
		name: "locker/directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, false, false, true},
	},
	{
		name: "clock/locker/directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, false, false, false, true},
	},
	{
		name: "authority/directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, false, false, true},
	},
	{
		name: "clock/authority/directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, false, false, false, true},
	},
	{
		name: "locker/authority/directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, false, false, false, true},
	},
	{
		name: "clock/locker/authority/directory/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, false, false, false, true},
	},
	{
		name: "epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, true, false, false, true},
	},
	{
		name: "clock/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, false, false, true},
	},
	{
		name: "locker/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, false, false, true},
	},
	{
		name: "clock/locker/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, true, false, false, true},
	},
	{
		name: "authority/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, false, false, true},
	},
	{
		name: "clock/authority/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, true, false, false, true},
	},
	{
		name: "locker/authority/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, true, false, false, true},
	},
	{
		name: "clock/locker/authority/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, true, false, false, true},
	},
	{
		name: "directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, false, false, true},
	},
	{
		name: "clock/directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, true, false, false, true},
	},
	{
		name: "locker/directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, true, false, false, true},
	},
	{
		name: "clock/locker/directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, true, false, false, true},
	},
	{
		name: "authority/directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, true, false, false, true},
	},
	{
		name: "clock/authority/directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, true, false, false, true},
	},
	{
		name: "locker/authority/directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, true, false, false, true},
	},
	{
		name: "clock/locker/authority/directory/epoch/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, true, false, false, true},
	},
	{
		name: "bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, false, true, false, true},
	},
	{
		name: "clock/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, false, true, false, true},
	},
	{
		name: "locker/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, false, true, false, true},
	},
	{
		name: "clock/locker/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, true, false, true},
	},
	{
		name: "authority/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, false, true, false, true},
	},
	{
		name: "clock/authority/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, true, false, true},
	},
	{
		name: "locker/authority/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, true, false, true},
	},
	{
		name: "clock/locker/authority/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, false, true, false, true},
	},
	{
		name: "directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, false, true, false, true},
	},
	{
		name: "clock/directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, true, false, true},
	},
	{
		name: "locker/directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, true, false, true},
	},
	{
		name: "clock/locker/directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, false, true, false, true},
	},
	{
		name: "authority/directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, true, false, true},
	},
	{
		name: "clock/authority/directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, false, true, false, true},
	},
	{
		name: "locker/authority/directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, false, true, false, true},
	},
	{
		name: "clock/locker/authority/directory/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, false, true, false, true},
	},
	{
		name: "epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, true, true, false, true},
	},
	{
		name: "clock/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, true, false, true},
	},
	{
		name: "locker/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, true, false, true},
	},
	{
		name: "clock/locker/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, true, true, false, true},
	},
	{
		name: "authority/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, true, false, true},
	},
	{
		name: "clock/authority/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, true, true, false, true},
	},
	{
		name: "locker/authority/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, true, true, false, true},
	},
	{
		name: "clock/locker/authority/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, true, true, false, true},
	},
	{
		name: "directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, true, false, true},
	},
	{
		name: "clock/directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, true, true, false, true},
	},
	{
		name: "locker/directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, true, true, false, true},
	},
	{
		name: "clock/locker/directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, true, true, false, true},
	},
	{
		name: "authority/directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, true, true, false, true},
	},
	{
		name: "clock/authority/directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, true, true, false, true},
	},
	{
		name: "locker/authority/directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, true, true, false, true},
	},
	{
		name: "clock/locker/authority/directory/epoch/bundle/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, true, true, false, true},
	},
	{
		name: "dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, false, false, true, true},
	},
	{
		name: "clock/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, false, false, true, true},
	},
	{
		name: "locker/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, false, false, true, true},
	},
	{
		name: "clock/locker/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, false, true, true},
	},
	{
		name: "authority/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, false, false, true, true},
	},
	{
		name: "clock/authority/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, false, true, true},
	},
	{
		name: "locker/authority/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, false, true, true},
	},
	{
		name: "clock/locker/authority/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, false, false, true, true},
	},
	{
		name: "directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, false, false, true, true},
	},
	{
		name: "clock/directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, false, true, true},
	},
	{
		name: "locker/directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, false, true, true},
	},
	{
		name: "clock/locker/directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, false, false, true, true},
	},
	{
		name: "authority/directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, false, true, true},
	},
	{
		name: "clock/authority/directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, false, false, true, true},
	},
	{
		name: "locker/authority/directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, false, false, true, true},
	},
	{
		name: "clock/locker/authority/directory/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, false, false, true, true},
	},
	{
		name: "epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, true, false, true, true},
	},
	{
		name: "clock/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, false, true, true},
	},
	{
		name: "locker/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, false, true, true},
	},
	{
		name: "clock/locker/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, true, false, true, true},
	},
	{
		name: "authority/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, false, true, true},
	},
	{
		name: "clock/authority/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, true, false, true, true},
	},
	{
		name: "locker/authority/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, true, false, true, true},
	},
	{
		name: "clock/locker/authority/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, true, false, true, true},
	},
	{
		name: "directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, false, true, true},
	},
	{
		name: "clock/directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, true, false, true, true},
	},
	{
		name: "locker/directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, true, false, true, true},
	},
	{
		name: "clock/locker/directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, true, false, true, true},
	},
	{
		name: "authority/directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, true, false, true, true},
	},
	{
		name: "clock/authority/directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, true, false, true, true},
	},
	{
		name: "locker/authority/directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, true, false, true, true},
	},
	{
		name: "clock/locker/authority/directory/epoch/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, true, false, true, true},
	},
	{
		name: "bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, false, true, true, true},
	},
	{
		name: "clock/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, false, true, true, true},
	},
	{
		name: "locker/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, false, true, true, true},
	},
	{
		name: "clock/locker/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, false, true, true, true},
	},
	{
		name: "authority/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, false, true, true, true},
	},
	{
		name: "clock/authority/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, false, true, true, true},
	},
	{
		name: "locker/authority/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, false, true, true, true},
	},
	{
		name: "clock/locker/authority/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, false, true, true, true},
	},
	{
		name: "directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, false, true, true, true},
	},
	{
		name: "clock/directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, false, true, true, true},
	},
	{
		name: "locker/directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, false, true, true, true},
	},
	{
		name: "clock/locker/directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, false, true, true, true},
	},
	{
		name: "authority/directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, false, true, true, true},
	},
	{
		name: "clock/authority/directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, false, true, true, true},
	},
	{
		name: "locker/authority/directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, false, true, true, true},
	},
	{
		name: "clock/locker/authority/directory/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, false, true, true, true},
	},
	{
		name: "epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, false, true, true, true, true},
	},
	{
		name: "clock/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, false, true, true, true, true},
	},
	{
		name: "locker/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, false, true, true, true, true},
	},
	{
		name: "clock/locker/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, false, true, true, true, true},
	},
	{
		name: "authority/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, false, true, true, true, true},
	},
	{
		name: "clock/authority/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, false, true, true, true, true},
	},
	{
		name: "locker/authority/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, false, true, true, true, true},
	},
	{
		name: "clock/locker/authority/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, false, true, true, true, true},
	},
	{
		name: "directory/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, false, true, true, true, true, true},
	},
	{
		name: "clock/directory/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, false, true, true, true, true, true},
	},
	{
		name: "locker/directory/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, false, true, true, true, true, true},
	},
	{
		name: "clock/locker/directory/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.TransactionLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, false, true, true, true, true, true},
	},
	{
		name: "authority/directory/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, false, true, true, true, true, true, true},
	},
	{
		name: "clock/authority/directory/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionClock
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, false, true, true, true, true, true, true},
	},
	{
		name: "locker/authority/directory/epoch/bundle/dab/factory",
		raw: func(sc *tenantScope) store.Scope {
			return &struct {
				store.Scope
				store.TransactionLocker
				store.AuthoritySnapshotLocker
				store.DirectorySnapshotReader
				store.AuthorizationEpochStore
				store.AuthoritySnapshotBundleLocker
				store.DirectoryAuthoritySnapshotLocker
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{false, true, true, true, true, true, true, true},
	},
	{
		name: "clock/locker/authority/directory/epoch/bundle/dab/factory",
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
				store.BoundedReaderFactory
			}{sc, sc, sc, sc, sc, sc, sc, sc, sc}
		},
		want: [8]bool{true, true, true, true, true, true, true, true},
	},
}

// TestBoundedReaderConfinementOptionalityMatrix requires every confined
// capability set to equal the raw one exactly, keeps same-boundary
// re-confinement idempotent, and proves the confined factory installs the
// boundary: its reader denies policy reads before any I/O.
func TestBoundedReaderConfinementOptionalityMatrix(t *testing.T) {
	if len(boundedPortCases) != 256 {
		t.Fatalf("matrix has %d cases, want 256", len(boundedPortCases))
	}
	ctx := context.Background()
	s, _, _, tenants := f2aFreshTarget(t, store.EngineSQLite)
	if err := s.Mutate(ctx, tenants[0], func(raw store.Scope) error {
		sc := raw.(*tenantScope)
		ws, err := raw.DefaultWorkspace(ctx)
		if err != nil {
			return err
		}
		for _, c := range boundedPortCases {
			t.Run(c.name, func(t *testing.T) {
				confined, err := store.ConfineWorkspace(ctx, c.raw(sc), ws.ID)
				if err != nil {
					t.Fatalf("confine: %v", err)
				}
				assertBoundedPortCapabilities(t, confined, c.want)
				twice, err := store.ConfineWorkspace(ctx, confined, ws.ID)
				if err != nil {
					t.Fatalf("re-confine: %v", err)
				}
				if twice != confined {
					t.Error("same-workspace re-confinement was not idempotent")
				}
				assertBoundedPortCapabilities(t, twice, c.want)
				if got, err := store.ConfineWorkspace(ctx, confined, model.NewID()); got != nil ||
					!errors.Is(err, store.ErrWorkspaceConfinement) {
					t.Errorf("cross-workspace confinement: scope=%v err=%v", got, err)
				}
				if factory, ok := confined.(store.BoundedReaderFactory); ok {
					if factory == store.BoundedReaderFactory(sc) {
						t.Fatal("confinement returned the raw factory")
					}
					reader, err := factory.NewBoundedReader(store.BoundedReadOptions{Limits: boundedTestLimits()})
					if err != nil {
						t.Fatalf("confined factory: %v", err)
					}
					if _, err := reader.GetPolicySnapshot(ctx, model.NewID()); !errors.Is(err, store.ErrWorkspaceLineageRequired) {
						t.Errorf("confined reader read policies: %v", err)
					}
				}
			})
		}
		return nil
	}); err != nil {
		t.Fatalf("matrix transaction: %v", err)
	}
}

func assertBoundedPortCapabilities(t *testing.T, confined store.Scope, want [8]bool) {
	t.Helper()
	_, clock := confined.(store.TransactionClock)
	_, locker := confined.(store.TransactionLocker)
	_, authority := confined.(store.AuthoritySnapshotLocker)
	_, directory := confined.(store.DirectorySnapshotReader)
	_, epoch := confined.(store.AuthorizationEpochStore)
	_, bundle := confined.(store.AuthoritySnapshotBundleLocker)
	_, dab := confined.(store.DirectoryAuthoritySnapshotLocker)
	_, factory := confined.(store.BoundedReaderFactory)
	names := [8]string{"TransactionClock", "TransactionLocker", "AuthoritySnapshotLocker",
		"DirectorySnapshotReader", "AuthorizationEpochStore", "AuthoritySnapshotBundleLocker",
		"DirectoryAuthoritySnapshotLocker", "BoundedReaderFactory"}
	for i, have := range [8]bool{clock, locker, authority, directory, epoch, bundle, dab, factory} {
		if have != want[i] {
			verb := "manufactured"
			if want[i] {
				verb = "lost"
			}
			t.Errorf("confinement %s %s", verb, names[i])
		}
	}
}
