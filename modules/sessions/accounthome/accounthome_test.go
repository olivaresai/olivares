// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accounthome

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// The naming rule of an account's home: where the root of every home is on a
// node, what the directories under it are called, which of them a reader is
// shown, and which components are refused before any of them reaches a
// filesystem call.
//
// It is pure, so it runs without a database, without a filesystem and without
// the race detector.

func TestAccountHome_RootPerEnvironmentAndModes(t *testing.T) {
	t.Parallel()

	// The root per environment. A node states its data directory; a deployment
	// that keeps account homes somewhere else states that root instead, and it
	// wins, because the layer that configured it knows what the data directory
	// does not.
	t.Run("root per environment", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name       string
			dataDir    string
			configured string
			want       string
			wantErr    error
		}{
			{name: "a data directory carries the root", dataDir: "/srv/olivares", want: "/srv/olivares/accounts"},
			{name: "a trailing separator is cleaned away", dataDir: "/srv/olivares/", want: "/srv/olivares/accounts"},
			{name: "an uncleaned data directory is cleaned", dataDir: "/srv/./olivares//data", want: "/srv/olivares/data/accounts"},
			{
				name:    "a configured root wins over the data directory",
				dataDir: "/srv/olivares", configured: "/mnt/homes/accounts", want: "/mnt/homes/accounts",
			},
			{name: "a configured root needs no data directory", configured: "/mnt/homes/accounts", want: "/mnt/homes/accounts"},
			{name: "no data directory and no configured root is refused", wantErr: ErrRoot},
			{name: "a relative data directory is refused", dataDir: "olivares/data", wantErr: ErrRoot},
			{name: "a relative configured root is refused", dataDir: "/srv/olivares", configured: "accounts", wantErr: ErrRoot},
			{name: "a data directory with a control character is refused", dataDir: "/srv/oliv\x00ares", wantErr: ErrRoot},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, err := Root(tc.dataDir, tc.configured)
				if tc.wantErr != nil {
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("Root(%q, %q) = %q, %v; want an error wrapping %v", tc.dataDir, tc.configured, got, err, tc.wantErr)
					}
					if got != "" {
						t.Fatalf("Root(%q, %q) refused and still returned %q", tc.dataDir, tc.configured, got)
					}
					return
				}
				if err != nil || got != tc.want {
					t.Fatalf("Root(%q, %q) = %q, %v; want %q", tc.dataDir, tc.configured, got, err, tc.want)
				}
			})
		}
	})

	// The custody tuple, and the arithmetic over it. A name is a LABEL and is
	// deliberately absent from every path here: renaming an account must move no
	// directory and reassign no custody, and two tenants or two environments must
	// never meet in one directory because they chose the same name.
	t.Run("path arithmetic over the custody tuple", func(t *testing.T) {
		t.Parallel()
		c := Custody{Tenant: "tnt-a", Environment: "env-test-node-a", AccountRef: "ppf_01J8ABCDEF"}
		rel, err := c.Relative()
		if err != nil || rel != "tnt-a/env-test-node-a/ppf_01J8ABCDEF" {
			t.Fatalf("Relative() = %q, %v; want the tenant, the environment and the account reference", rel, err)
		}
		parent, err := c.ParentRelative()
		if err != nil || parent != "tnt-a/env-test-node-a" {
			t.Fatalf("ParentRelative() = %q, %v; want tnt-a/env-test-node-a", parent, err)
		}
		config, err := c.ConfigRelative()
		if err != nil || config != rel+"/"+ConfigDir {
			t.Fatalf("ConfigRelative() = %q, %v; want %s/%s", config, err, rel, ConfigDir)
		}
		user, err := c.UserRelative()
		if err != nil || user != rel+"/"+UserDir {
			t.Fatalf("UserRelative() = %q, %v; want %s/%s", user, err, rel, UserDir)
		}
		if ConfigDir == UserDir {
			t.Fatalf("the configuration home and the user home are one directory (%q): a driver's state and its HOME are not the same place", ConfigDir)
		}
		// Two accounts of one tenant and environment never share a directory, and
		// neither do two tenants that chose the same account reference shape.
		other := Custody{Tenant: "tnt-b", Environment: c.Environment, AccountRef: c.AccountRef}
		otherRel, err := other.Relative()
		if err != nil || otherRel == rel {
			t.Fatalf("two tenants resolved to %q and %q (err %v); a tenant must be part of the custody", rel, otherRel, err)
		}

		// Staging is a directory of the operation, never of the account: it is
		// named by an identity that is never reused, and it lives beside the
		// published tree rather than inside it.
		stage, err := Staging("01J8STAGE00000000000000000")
		if err != nil || stage != StagingDir+"/01J8STAGE00000000000000000" {
			t.Fatalf("Staging() = %q, %v; want %s/<operation>", stage, err, StagingDir)
		}
		if !strings.HasPrefix(StagingDir, ".") {
			t.Fatalf("the staging directory %q does not start with a dot, so a tenant identity could collide with it", StagingDir)
		}
	})

	// The two home modes, and what each one shows a reader. A managed home is the
	// product's own directory under the accounts root, so it has a relative path
	// and that path is the only form anybody outside the ledger sees. An adopted
	// home is the operator's own directory somewhere else entirely, so there is no
	// relative path to show and the field stays empty rather than being guessed.
	t.Run("modes decide what a reader is shown", func(t *testing.T) {
		t.Parallel()
		c := Custody{Tenant: "tnt-a", Environment: "env-test-node-a", AccountRef: "ppf_01J8ABCDEF"}
		managed, err := RelativeFor(ModeManaged, c)
		if err != nil || managed != "tnt-a/env-test-node-a/ppf_01J8ABCDEF" {
			t.Fatalf("RelativeFor(%q) = %q, %v; want the custody tuple", ModeManaged, managed, err)
		}
		adopted, err := RelativeFor(ModeAdopted, c)
		if err != nil || adopted != "" {
			t.Fatalf("RelativeFor(%q) = %q, %v; want no path at all", ModeAdopted, adopted, err)
		}
		for _, mode := range []string{"", "Managed", "dedicated", "unknown"} {
			if got, err := RelativeFor(mode, c); !errors.Is(err, ErrMode) || got != "" {
				t.Fatalf("RelativeFor(%q) = %q, %v; want an error wrapping ErrMode", mode, got, err)
			}
		}
		if ModeManaged == ModeAdopted {
			t.Fatal("the two home modes are one string, so a reader cannot tell who made the home")
		}
	})

	// The file modes the design states, stated here as facts and not as hopes:
	// both homes are private to their owner, and the directory ABOVE them is
	// traversable but not listable, so one account cannot enumerate another.
	t.Run("file modes", func(t *testing.T) {
		t.Parallel()
		if HomeMode != fs.FileMode(0o700) {
			t.Fatalf("HomeMode = %#o, want 0700", HomeMode)
		}
		if StagingMode != fs.FileMode(0o700) {
			t.Fatalf("StagingMode = %#o, want 0700: a half-built home is no more readable than a finished one", StagingMode)
		}
		if ParentMode != fs.FileMode(0o711) {
			t.Fatalf("ParentMode = %#o, want 0711", ParentMode)
		}
		if HomeMode&0o077 != 0 {
			t.Fatalf("HomeMode %#o grants a group or another user access to an account's home", HomeMode)
		}
		if ParentMode&0o044 != 0 {
			t.Fatalf("ParentMode %#o is listable, so one account can enumerate the others", ParentMode)
		}
		if ParentMode&0o011 != 0o011 {
			t.Fatalf("ParentMode %#o is not traversable, so no home under it can be reached", ParentMode)
		}
	})

	// A component that could leave the tree it is joined into is refused HERE,
	// before any of it reaches a filesystem call, so the confinement does not
	// depend on the caller remembering to check. An execution-environment
	// reference is the dangerous one: the profile plane accepts every printable
	// character in it except ':' and '|', so '/' and '..' get that far.
	t.Run("a traversal component is refused", func(t *testing.T) {
		t.Parallel()
		good := Custody{Tenant: "tnt-a", Environment: "env-test-node-a", AccountRef: "ppf_01J8ABCDEF"}
		cases := []struct {
			name    string
			custody Custody
		}{
			{"an empty tenant", Custody{Tenant: "", Environment: good.Environment, AccountRef: good.AccountRef}},
			{"an empty environment", Custody{Tenant: good.Tenant, Environment: "", AccountRef: good.AccountRef}},
			{"an empty account reference", Custody{Tenant: good.Tenant, Environment: good.Environment, AccountRef: ""}},
			{"a parent reference as the environment", Custody{Tenant: good.Tenant, Environment: "..", AccountRef: good.AccountRef}},
			{"a parent reference as the tenant", Custody{Tenant: "..", Environment: good.Environment, AccountRef: good.AccountRef}},
			{"a parent reference as the account", Custody{Tenant: good.Tenant, Environment: good.Environment, AccountRef: ".."}},
			{"this directory as a component", Custody{Tenant: good.Tenant, Environment: ".", AccountRef: good.AccountRef}},
			{"a separator inside the environment", Custody{Tenant: good.Tenant, Environment: "env-a/../../etc", AccountRef: good.AccountRef}},
			{"a separator inside the tenant", Custody{Tenant: "tnt-a/tnt-b", Environment: good.Environment, AccountRef: good.AccountRef}},
			{"a backslash inside the environment", Custody{Tenant: good.Tenant, Environment: `env-a\..\..`, AccountRef: good.AccountRef}},
			{"a leading dot hides the home beside the staging tree", Custody{Tenant: ".staging", Environment: good.Environment, AccountRef: good.AccountRef}},
			{"a leading dot in the environment", Custody{Tenant: good.Tenant, Environment: ".archive", AccountRef: good.AccountRef}},
			{"a control character", Custody{Tenant: good.Tenant, Environment: "env\x00a", AccountRef: good.AccountRef}},
			{"a newline", Custody{Tenant: good.Tenant, Environment: "env\na", AccountRef: good.AccountRef}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				for _, call := range []struct {
					what string
					fn   func() (string, error)
				}{
					{"Relative", tc.custody.Relative},
					{"ParentRelative", tc.custody.ParentRelative},
					{"ConfigRelative", tc.custody.ConfigRelative},
					{"UserRelative", tc.custody.UserRelative},
					{"RelativeFor", func() (string, error) { return RelativeFor(ModeManaged, tc.custody) }},
				} {
					got, err := call.fn()
					if !errors.Is(err, ErrComponent) {
						t.Fatalf("%s() = %q, %v; want an error wrapping ErrComponent", call.what, got, err)
					}
					if got != "" {
						t.Fatalf("%s() refused and still returned the path %q", call.what, got)
					}
				}
			})
		}
		// The same rule guards the staging name, which is the one component this
		// package is handed rather than deriving.
		for _, operation := range []string{"", ".", "..", "a/b", "a\x00b", ".hidden"} {
			if got, err := Staging(operation); !errors.Is(err, ErrComponent) || got != "" {
				t.Fatalf("Staging(%q) = %q, %v; want an error wrapping ErrComponent", operation, got, err)
			}
		}
	})
}
