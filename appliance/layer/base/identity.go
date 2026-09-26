// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ProductDataDir is the product package's data directory (packaging/systemd/olivares.service).
const ProductDataDir = "/var/lib/olivares"

// productIdentityFiles are the files the product creates in its data directory for one
// installation: the evidence set of cmd/olivares/boot.go installationExistsAt, which
// includes the three signing keys, the TLS key, the setup token and the store that holds
// users and tenant data. None may exist in a template; every one is unique to an instance.
var productIdentityFiles = []string{
	"olivares.db", "audit-signing.key", "catalog-signing.key", "policy-signing.key",
	"tls.key", "secret-store.key", "eventing-secret.key", "sso-secret.key",
	"setup.token", "license.key", "install-id",
}

// ProductIdentities lists the product identity files present under root.
func ProductIdentities(root string) ([]string, error) {
	var found []string
	for _, name := range productIdentityFiles {
		_, err := os.Lstat(filepath.Join(root, ProductDataDir, name))
		switch {
		case err == nil:
			found = append(found, name)
		case !errors.Is(err, os.ErrNotExist):
			return nil, err
		}
	}
	return found, nil
}

// TemplateFindings lists every instance identity present under root, an image root or a
// running system. A clean template has none. An instance that ran first boot keeps its
// record, so it is never a template; preparing a template from an instance is a separate,
// explicit lifecycle action that this package does not perform.
func TemplateFindings(root string) ([]string, error) {
	found, err := ProductIdentities(root)
	if err != nil {
		return nil, err
	}
	keys, err := filepath.Glob(filepath.Join(root, "etc/ssh/ssh_host_*_key"))
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		found = append(found, "SSH host key "+filepath.Base(key))
	}
	id, err := os.ReadFile(filepath.Join(root, "etc/machine-id"))
	switch {
	case err == nil:
		// Empty and "uninitialized" are the two template forms machine-id(5) defines.
		if v := strings.TrimSpace(string(id)); v != "" && v != "uninitialized" {
			found = append(found, "machine-id")
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	switch _, err := os.Lstat(filepath.Join(root, StateDir, recordFile)); {
	case err == nil:
		found = append(found, "first-boot record")
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return found, nil
}
