// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

// The Appliance Console's selection. The console runs unprivileged and the carriers are
// root-only, so it reads the operator's exposure answers from one file first boot publishes:
// a view of the validated answers, never a second configuration store. The base package's
// postinstall creates the directory root-owned and 0755; the first-boot units may write it
// (ReadWritePaths=) but cannot create it.
const (
	PortalSelectionDir  = "/etc/olivares-portal"
	PortalSelectionFile = PortalSelectionDir + "/selection.json"

	portalSelectionSchema = "olivares-portal-selection/v1"
	// The console reads at most this much of the file.
	maxPortalSelectionBytes = 4096
)

// portalSelection is the published document: the answers' own paths, in a fixed order, and
// only the fields the answers declare.
type portalSelection struct {
	SchemaVersion string               `json:"schema_version"`
	Portal        *portalSelectionPart `json:"portal,omitempty"`
	Host          *portalSelectionHost `json:"host,omitempty"`
}

type portalSelectionPart struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Listen  *string `json:"listen,omitempty"`
}

type portalSelectionHost struct {
	ManagementInterfaces []string `json:"management_interfaces"`
}

// renderPortalSelection returns the selection document of a with a terminal newline, and
// false when a declares none of portal.enabled, portal.listen and host.management_interfaces.
// The answers module has validated and normalized the values (the interfaces sorted).
func renderPortalSelection(a Answers) ([]byte, bool, error) {
	document := portalSelection{SchemaVersion: portalSelectionSchema}
	if a.PortalEnabled != nil || a.PortalListen != nil {
		document.Portal = &portalSelectionPart{Enabled: a.PortalEnabled, Listen: a.PortalListen}
	}
	if len(a.ManagementInterfaces) > 0 {
		document.Host = &portalSelectionHost{ManagementInterfaces: a.ManagementInterfaces}
	}
	if document.Portal == nil && document.Host == nil {
		return nil, false, nil
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, true, err
	}
	return append(data, '\n'), true, nil
}

// publishPortalSelection writes the selection a declares, root-owned and 0644 so the
// console's account can read it, or removes one left from earlier answers when a declares
// none. The directory must exist, be a directory owned by this account and be writable by
// nobody else: first boot never creates it.
func (s ProductConfig) publishPortalSelection(a Answers) error {
	content, declared, err := renderPortalSelection(a)
	if err != nil {
		return Refuse("the portal selection could not be encoded")
	}
	dir := s.Host.path(PortalSelectionDir)
	if !declared {
		err := os.Remove(filepath.Join(dir, filepath.Base(PortalSelectionFile)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Refuse("a portal selection the answers no longer declare could not be removed")
		}
		return nil
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 || !ownedByThisAccount(info) {
		return Refuse("the portal selection directory " + PortalSelectionDir + " is missing or writable by others; " +
			"olivares-appliance-base creates it")
	}
	if err := writeAtomic(dir, filepath.Base(PortalSelectionFile), content, 0o644); err != nil {
		return Refuse("the portal selection could not be written")
	}
	return nil
}

// observePortalSelection measures the published selection against a: the SHA-256 prefix of
// the file when a declares a selection and the file says exactly that, "" when a declares
// none and none is published, and a refusal otherwise. The file must be a regular file owned
// by this account, mode 0644, not a symbolic link.
func (s ProductConfig) observePortalSelection(a Answers) (string, error) {
	want, declared, err := renderPortalSelection(a)
	if err != nil {
		return "", Refuse("the portal selection could not be encoded")
	}
	path := s.Host.path(PortalSelectionFile)
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist) && declared:
		return "", Refuse("the portal selection first boot publishes is missing")
	case errors.Is(err, os.ErrNotExist):
		return "", nil
	case err != nil:
		return "", Refuse("the portal selection cannot be inspected")
	case !declared:
		return "", Refuse("a portal selection exists that the answers do not declare")
	case info.Mode().Perm() != 0o644:
		return "", Refuse("the portal selection is not mode 0644")
	}
	got, present, err := carriers.ReadProtected(path, maxPortalSelectionBytes)
	if err != nil || !present {
		return "", Refuse("the portal selection cannot be read safely")
	}
	if !bytes.Equal(got, want) {
		return "", Refuse("the portal selection differs from the answers")
	}
	sum := sha256.Sum256(got)
	return fmt.Sprintf("%x", sum[:8]), nil
}

// ownedByThisAccount reports whether info's owner is the effective user: root on an installed
// appliance, where first boot runs.
func ownedByThisAccount(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
