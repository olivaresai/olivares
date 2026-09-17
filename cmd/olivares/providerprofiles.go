// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// providerprofiles.go is the B1 composition seam for the provider-profile plane:
// the node's PERSISTENT local execution-environment identity, and the narrow port
// through which the sessions module validates a source→profile binding against
// the durable roster and this node's reconciler. The module never receives the
// roster store, the auth scope or a Config map; it receives an answer.

const (
	// envExecutionEnvironmentID lets an operator whose deployment already manages
	// node identities supply the execution-environment reference explicitly. It is
	// REQUIRED on a topology that offers only shared state (no node-local data
	// directory), and an explicit but malformed value refuses boot rather than
	// degrading in silence.
	envExecutionEnvironmentID = "OLIVARES_EXECUTION_ENVIRONMENT_ID"
	// executionEnvironmentFile is the node-local state file holding the generated
	// identity. It lives in the data directory — private to this node, beside the
	// signing keys — never in the shared database and never on a unit shared
	// between nodes (those would make two nodes one environment).
	executionEnvironmentFile   = "execution-environment-id"
	executionEnvironmentPrefix = "xenv_"
	maxExecutionEnvironmentRef = 256
)

// resolveExecutionEnvironmentRef returns this node's execution-environment
// reference: the explicit override when set, else the identity persisted in the
// data directory, generated ONCE with an atomic exclusive create and 0600.
//
// It returns "" — the profiled plane deny-closed, the legacy path untouched —
// when there is no node-local state to persist in or the file cannot be read or
// created; it returns an error only for an explicit override that is malformed,
// because an operator who wrote a value expects it to be applied.
func resolveExecutionEnvironmentRef(dataDir string, localStateAvailable bool, getenv func(string) string, log *slog.Logger) (string, error) {
	if override := strings.TrimSpace(getenv(envExecutionEnvironmentID)); override != "" {
		if !validExecutionEnvironmentRef(override) {
			return "", fmt.Errorf("%s is set but not usable: it must be 1..%d printable bytes without whitespace, ':' or '|'", envExecutionEnvironmentID, maxExecutionEnvironmentRef)
		}
		return override, nil
	}
	if !localStateAvailable || strings.TrimSpace(dataDir) == "" {
		if log != nil {
			log.Warn("execution environment: no node-local state directory to persist an identity in; profiled session launches are deny-closed until " + envExecutionEnvironmentID + " names this node explicitly")
		}
		return "", nil
	}
	path := filepath.Join(dataDir, executionEnvironmentFile)
	ref, err := readExecutionEnvironmentFile(path)
	switch {
	case err == nil:
		return ref, nil
	case errors.Is(err, fs.ErrNotExist):
		created, cerr := createExecutionEnvironmentFile(path)
		if cerr == nil {
			if log != nil {
				log.Info("execution environment: generated this node's identity", "path", path)
			}
			return created, nil
		}
		if errors.Is(cerr, fs.ErrExist) {
			// Lost a race with a sibling process of this node: read the winner.
			if ref, rerr := readExecutionEnvironmentFile(path); rerr == nil {
				return ref, nil
			}
		}
		err = cerr
	}
	if log != nil {
		log.Warn("execution environment: identity unavailable; profiled session launches are deny-closed (the legacy launch path is unaffected)", "path", path, "err", err)
	}
	return "", nil
}

// validExecutionEnvironmentRef mirrors the module's shape rule for an environment
// reference so a value accepted here is one the module accepts.
func validExecutionEnvironmentRef(s string) bool {
	if s == "" || len(s) > maxExecutionEnvironmentRef || s != strings.TrimSpace(s) {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r == 0x7f || r == ':' || r == '|' {
			return false
		}
	}
	return true
}

func readExecutionEnvironmentFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(string(raw))
	if !validExecutionEnvironmentRef(ref) {
		// A present but unusable file is an operator's to inspect: it is never
		// overwritten, because that would silently mint a NEW environment for a node
		// whose old identity the profiles and runs still name.
		return "", fmt.Errorf("%s holds an unusable identity; repair or remove it deliberately", path)
	}
	return ref, nil
}

// createExecutionEnvironmentFile writes the identity to a private temporary file
// and LINKS it into place: the link is atomic and fails with fs.ErrExist when a
// sibling won, so two processes of one node can never mint two identities and a
// partial write never becomes the file another process reads.
func createExecutionEnvironmentFile(path string) (string, error) {
	ref := executionEnvironmentPrefix + model.NewID().String()
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+executionEnvironmentFile+".*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // best-effort cleanup of the staging file
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(ref + "\n"); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Link(tmpName, path); err != nil {
		return "", err
	}
	return ref, nil
}

// providerSourceResolver implements sessions.ProviderSourceResolver over the
// durable roster, this node's reconciler and the deployment authorizer. The
// order of checks is the order of authority: may the actor administer the roster
// at all (the same deployment-wide permission the console's source CRUD requires),
// does the row exist under this persistent id, and has THIS node applied it — in
// which case the revision reported is the one the reconciler wired, which a failed
// rotation never advances.
type providerSourceResolver struct {
	store *auth.SourceStore
	sr    *sourceReconciler
	authz *auth.Authorizer
	env   string
}

var _ sessions.ProviderSourceResolver = (*providerSourceResolver)(nil)

func (r *providerSourceResolver) ResolveAppliedSource(ctx context.Context, p auth.Principal, _ model.TenantID, sourceID model.ID) (sessions.SourceRevision, error) {
	if r == nil || r.store == nil || r.sr == nil || r.authz == nil || r.env == "" {
		return sessions.SourceRevision{}, sessions.ErrNoSourceResolver
	}
	// The global roster is deployment-wide configuration, administered by the
	// system admin exactly as /v1/console/sources requires (authzSystem).
	dec := r.authz.Authorize(ctx, auth.Request{
		Principal: p, Permission: auth.PermSystemAdmin, Tenant: model.SystemTenantID,
		Resource: auth.ResourceFor(auth.PermSystemAdmin),
	})
	if !dec.Allow {
		return sessions.SourceRevision{}, sessions.ErrSourceForbidden
	}
	def, ok, err := r.store.GetByID(ctx, sourceID)
	if err != nil {
		return sessions.SourceRevision{}, err
	}
	if !ok || def.Scope != auth.GlobalSourceScope {
		return sessions.SourceRevision{}, sessions.ErrSourceNotFound
	}
	appliedID, revision, applied := r.sr.appliedRevision(def.Name)
	if !applied || appliedID != def.ID {
		// Not wired on this node (never applied, refused at Open, or the name now
		// belongs to a recreated row): nothing here can be bound.
		return sessions.SourceRevision{}, sessions.ErrSourceNotFound
	}
	return sessions.SourceRevision{
		ID: def.ID, Version: revision, Name: def.Name, Tenant: def.Tenant, Kind: def.Kind,
		EnvironmentRef: r.env,
	}, nil
}
