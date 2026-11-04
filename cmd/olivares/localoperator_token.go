// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// THE ACCOUNT THAT OWNS THE DATA DIRECTORY IS THE LOCAL OPERATOR.
//
// That account already holds the store, the signing keys and the TLS key, so a
// separate `olivares login` protects nothing from it and blocks the first hour
// (2026-11-01, in his Docker container: "Not signed in" for every command).
// The engine therefore leaves it one credential in the data directory, readable
// only by that account (0600): an ordinary superadmin API token (that account holds
// the store and the keys, so nothing less is true), then the first organization's
// tenant id on a second line, the tenant the CLI sends by default, as after
// `olivares login`. It is not a new kind of credential: it authenticates through the
// same bearer path as every other API token and is revoked like any token. Its name
// says whose it is, "local-operator <install id>: <user> (uid N) on <host>", so the
// ledger's token:<id> resolves to the data directory, account and host in
// `olivares tokens`.
//
// The file holds this data directory's only live one: each replacement revokes the
// earlier tokens of the same install id (another node of a shared store keeps its
// own), so deleting the file (or revoking the token) and restarting cuts any copy of
// it in a backup or snapshot.
//
// Another account cannot read the file, and a CLI on another host never sees it:
// both sign in as before. The CLI sends that bearer to the engine the data directory
// recorded, so it trusts the directory only as far as the engine does (readAccountFile):
// a directory this account owns that no other account can write, holding regular files
// it owns. Where a directory is shared or the file is readable, the engine leaves no
// credential (or replaces the one it left) and the CLI signs in as before.

// localOperatorTokenFile is the credential the engine leaves in its data
// directory for the account that owns it.
const localOperatorTokenFile = "operator.token"

// What readAccountFile refuses in a file's permission bits: the credential must be
// private to the account (nobody else reads it), and the console state and the
// certificate, which choose where the credential goes, must not be writable by anyone else.
const (
	accountFilePrivate     os.FileMode = 0o077
	accountFileNotWritable os.FileMode = 0o022
)

var (
	// errUnsafeAccountDir: the data directory is not one to trust.
	errUnsafeAccountDir = errors.New("the data directory is not owned by the expected account, or accounts other than its owner can write it")
	// errUnsafeAccountFile: a file in it is not the one the engine wrote there.
	errUnsafeAccountFile = errors.New("not a regular file of the data directory's owner with safe permissions")
)

// anyDirOwner asks readDataDirFile to trust whichever account owns the directory.
const anyDirOwner = -1

// readDataDirFile is the one safe read of the files in a data directory that decide
// where a credential goes. It reads name from dir only when dir is a directory that
// no account but its owner can write (else another account renames or replaces files
// in it) and name is a regular file, not a symlink, of that same owner, none of whose
// permission bits are in deny. With uid other than anyDirOwner that owner must be uid.
// A missing file stays fs.ErrNotExist; anything unsafe is errUnsafeAccountDir or
// errUnsafeAccountFile.
func readDataDirFile(dir, name string, deny os.FileMode, uid int) ([]byte, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	owner, known := infoOwner(info)
	if !info.IsDir() || info.Mode().Perm()&accountFileNotWritable != 0 || !known || (uid != anyDirOwner && owner != uid) {
		return nil, fmt.Errorf("%w: %s", errUnsafeAccountDir, dir)
	}
	path := filepath.Join(dir, name)
	info, err = os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fileUID, known := infoOwner(info); !info.Mode().IsRegular() || info.Mode().Perm()&deny != 0 || !known || fileUID != owner {
		return nil, fmt.Errorf("%w: %s", errUnsafeAccountFile, path)
	}
	return os.ReadFile(path)
}

// readAccountFile is readDataDirFile for the local operator's files, the engine that
// keeps its credential and the CLI that sends it: the directory and the files must be
// this account's own.
func readAccountFile(dir, name string, deny os.FileMode) ([]byte, error) {
	return readDataDirFile(dir, name, deny, os.Geteuid())
}

// localOperatorTokenName starts the name of every local-operator token.
const localOperatorTokenName = "local-operator"

// ensureLocalOperatorToken leaves the local operator's credential in dataDir when
// an organization exists, and keeps the one already there when it still works. It
// does nothing before first-boot setup: no organization, nothing to be an owner of.
//
// ponytail: the token never expires and the CLI's default tenant is the install's
// oldest organization (--tenant reaches any other); give it an expiry and a renewal
// if a deployment needs a credential that ages.
func ensureLocalOperatorToken(ctx context.Context, authr *auth.Authenticator, st store.Store, dataDir string) error {
	tenant, found, err := oldestOrganization(ctx, st)
	if err != nil {
		return fmt.Errorf("local operator credential: find the organization: %w", err)
	}
	if !found {
		return nil
	}
	path := filepath.Join(dataDir, localOperatorTokenFile)
	raw, err := readAccountFile(dataDir, localOperatorTokenFile, accountFilePrivate)
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errUnsafeAccountFile) {
		// An unsafe directory (the CLI would refuse it) or a read that failed: leave
		// everything as it is. An unsafe or missing file is replaced below.
		return fmt.Errorf("local operator credential: read %s: %w", path, err)
	}
	actor, err := auth.NewSystemOperator("serve/local-operator",
		"the engine leaves the data directory owner's CLI credential")
	if err != nil {
		return fmt.Errorf("local operator credential: %w", err)
	}
	prefix := localOperatorPrefix(dataDir)
	if have, haveTenant := splitLocalOperatorFile(string(raw)); have != "" {
		p, err := authr.Authenticate(ctx, have)
		if err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
			// A store that could not answer says nothing about the token: keep it.
			return fmt.Errorf("local operator credential: check the one in %s: %w", path, err)
		}
		if err == nil && p.Superadmin && haveTenant == tenant.String() {
			// Kept; any predecessor an earlier start did not finish revoking goes now.
			return revokeEarlierLocalOperatorTokens(ctx, authr, st, actor, prefix, p.CredID)
		}
	}
	token, issued, err := authr.IssueToken(ctx, actor, auth.TokenSpec{
		Name: prefix + localOperatorOwner(), Superadmin: true,
	})
	if err != nil {
		return fmt.Errorf("local operator credential: issue the token: %w", err)
	}
	if err := writeAtomicMode(path, []byte(token+"\n"+tenant.String()+"\n"), 0o600); err != nil {
		if rerr := authr.RevokeToken(ctx, actor, issued.ID); rerr != nil {
			err = errors.Join(err, fmt.Errorf("revoke the unwritten token %s: %w", issued.ID, rerr))
		}
		return fmt.Errorf("local operator credential: write %s: %w", path, err)
	}
	return revokeEarlierLocalOperatorTokens(ctx, authr, st, actor, prefix, issued.ID)
}

// localOperatorPrefix starts the name of every local-operator token of dataDir.
func localOperatorPrefix(dataDir string) string {
	return localOperatorTokenName + " " + resolveInstallID(dataDir) + ": "
}

// localOperatorOwner is the account that owns the data directory (the engine runs
// as it) and the host.
func localOperatorOwner() string {
	name := strconv.Itoa(os.Geteuid())
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username + " (uid " + name + ")"
	} else {
		name = "uid " + name
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		name += " on " + host
	}
	return name
}

// revokeEarlierLocalOperatorTokens revokes every live engine-minted superadmin token
// (no owning user) whose name starts with prefix, except keep.
func revokeEarlierLocalOperatorTokens(ctx context.Context, authr *auth.Authenticator, st store.Store, actor auth.Principal, prefix string, keep model.ID) error {
	var earlier []model.ID
	err := st.AuthView(ctx, func(as store.AuthScope) error {
		tokens, _, err := as.Tokens().List(ctx, model.Query{
			Filters: []model.Filter{
				{Column: "is_superadmin", Op: model.OpEq, Value: true},
				{Column: "revoked", Op: model.OpEq, Value: false},
			},
			Limit: 1000,
		})
		for _, t := range tokens {
			if t.ID != keep && t.UserID.IsZero() && strings.HasPrefix(t.Name, prefix) {
				earlier = append(earlier, t.ID)
			}
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("local operator credential: list the earlier ones: %w", err)
	}
	for _, id := range earlier {
		if err := authr.RevokeToken(ctx, actor, id); err != nil {
			return fmt.Errorf("local operator credential: revoke the earlier token %s: %w", id, err)
		}
	}
	return nil
}

// oldestOrganization is the organization first-boot setup created: the oldest served
// business tenant. It reads the tenants every install can enumerate (servedWorkTenants),
// so it also answers on the default PostgreSQL install, where ListOrgs is refused for
// want of an admin pool.
func oldestOrganization(ctx context.Context, st store.Store) (model.TenantID, bool, error) {
	tenants, err := servedWorkTenants(ctx, st)
	if err != nil {
		return "", false, err
	}
	var (
		oldest model.Org
		found  bool
	)
	for _, tenant := range tenants {
		var org model.Org
		err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			org, err = sc.Org(ctx)
			return err
		})
		if err != nil {
			return "", false, fmt.Errorf("read the org of tenant %s: %w", tenant, err)
		}
		if !found || org.CreatedAt.Time().Before(oldest.CreatedAt.Time()) {
			oldest, found = org, true
		}
	}
	return oldest.TenantID, found, nil
}

// splitLocalOperatorFile reads the credential file: the token, then the tenant id.
func splitLocalOperatorFile(raw string) (token, tenant string) {
	lines := strings.Fields(raw)
	if len(lines) != 2 {
		return "", ""
	}
	return lines[0], lines[1]
}
