// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// secretCalls records what the reader asked the sealed store for.
type secretCalls struct {
	scopes []model.TenantID
	names  []string
}

func (s *secretCalls) Resolve(_ context.Context, scope model.TenantID, name string) ([]byte, error) {
	s.scopes, s.names = append(s.scopes, scope), append(s.names, name)
	return []byte("value-of-" + name), nil
}

// The connections broker may open the connection/ entries of the deployment's secret
// store and nothing else: a secret under another namespace (the MCP servers', the git
// hosts', a personal vault item) is not reachable through this port, so a connection can
// never be made to read it.
func TestConnectionSecretReaderOpensOnlyItsNamespace(t *testing.T) {
	store := &secretCalls{}
	read := connectionSecretReader(store)

	got, err := read(t.Context(), "connection/prod-db")
	if err != nil || string(got) != "value-of-connection/prod-db" {
		t.Fatalf("read(connection/prod-db) = %q, %v", got, err)
	}
	if len(store.scopes) != 1 || store.scopes[0] != auth.GlobalSecretScope {
		t.Fatalf("the store was asked in scope %v, want the deployment scope", store.scopes)
	}

	for _, name := range []string{"", "mcp/crm", "git-host/github", "provider/x", "connection", "connection/", "Connection/x",
		"connection/../mcp/crm", "connection//x", "connection/./x", "connection/x/", "connection/x ", " connection/x",
		"connection/x y", "connection/\x00", auth.PersonalVaultPrefix + "login"} {
		before := len(store.names)
		if _, err := read(t.Context(), name); !errors.Is(err, auth.ErrSecretNotFound) {
			t.Errorf("read(%q) = %v, want the secret not found", name, err)
		}
		if len(store.names) != before {
			t.Errorf("read(%q) reached the sealed store", name)
		}
	}
}

// failingSecrets answers every Resolve with err.
type failingSecrets struct{ err error }

func (f failingSecrets) Resolve(context.Context, model.TenantID, string) ([]byte, error) {
	return nil, f.err
}

// What the sealed store answers reaches the broker as it is: an outage is not turned into
// "not found", and a missing entry is not turned into a value. With no store at all the
// door answers not found instead of panicking.
func TestConnectionSecretReaderPassesStoreAnswersThrough(t *testing.T) {
	outage := errors.New("the sealed store is unavailable")
	if _, err := connectionSecretReader(failingSecrets{outage})(t.Context(), "connection/prod-db"); !errors.Is(err, outage) {
		t.Errorf("a store failure came back as %v, want it unchanged", err)
	}
	if got, err := connectionSecretReader(failingSecrets{auth.ErrSecretNotFound})(t.Context(), "connection/prod-db"); !errors.Is(err, auth.ErrSecretNotFound) || got != nil {
		t.Errorf("a missing entry came back as %q, %v, want the secret not found", got, err)
	}
	if _, err := connectionSecretReader(nil)(t.Context(), "connection/prod-db"); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Errorf("with no store the door answered %v, want the secret not found", err)
	}
}
