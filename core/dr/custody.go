// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Sealer file names and overrides are shared by boot, backup and restore.
const (
	SecretStoreKeyFile    = "secret-store.key"
	SecretStoreKeyEnv     = "OLIVARES_SECRET_STORE_KEY"
	TOTPSeedKeyFile       = "totp-seed.key"
	TOTPSeedKeyEnv        = "OLIVARES_TOTP_SEED_KEY"
	SSOSecretKeyFile      = "sso-secret.key"
	SSOSecretKeyEnv       = "OLIVARES_SSO_SECRET_KEY"
	EventingSecretKeyFile = "eventing-secret.key"
	EventingSecretKeyEnv  = "OLIVARES_EVENTING_SECRET_KEY"
)

// SealerKeyFiles identifies the data-directory sealers and their environment overrides.
var SealerKeyFiles = []struct{ Name, Env string }{
	{SecretStoreKeyFile, SecretStoreKeyEnv},
	{TOTPSeedKeyFile, TOTPSeedKeyEnv},
	{SSOSecretKeyFile, SSOSecretKeyEnv},
	{EventingSecretKeyFile, EventingSecretKeyEnv},
}

func decodeSealerKey(path string, b []byte) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		clear(key)
		return nil, fmt.Errorf("%q is not a valid sealer key file", path)
	}
	return key, nil
}

// SealerKeyInEffect is the key serve seals and opens with for a sealer: its
// variable when set, otherwise the file in dir; from names which. It mints
// nothing, so a nil key with no error means there is none yet.
func SealerKeyInEffect(dir, name, env string, getenv func(string) string) (key []byte, from string, err error) {
	if v := strings.TrimSpace(getenv(env)); v != "" {
		key, err = decodeSealerKey(env, []byte(v))
		return key, env, err
	}
	from = filepath.Join(dir, name)
	b, err := os.ReadFile(from)
	if os.IsNotExist(err) {
		return nil, from, nil
	}
	if err == nil {
		key, err = decodeSealerKey(from, b)
	}
	clear(b)
	return key, from, err
}

// SealerKeyProbe is HMAC-SHA-256 under a sealer key over the key's file name: it
// matches under that key only and carries no key material.
func SealerKeyProbe(key []byte, name string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("olivares.dr.sealer.probe|" + name))
	return hex.EncodeToString(mac.Sum(nil))
}

// SameInstallation reports whether dataDir, before the restore writes to it,
// holds the bundle's audit signing keys. It is then the source's own data dir and
// the sealer key files it keeps are the source's. An older bundle has no sealer
// probe, so this is the only evidence it offers. ponytail: a sealer key file
// replaced on the source after that backup is not detected (the source cannot
// open those values either); a bundle with probes has no such gap.
func SameInstallation(dataDir string, m *Manifest) bool {
	matched := false
	for _, kr := range m.Keys {
		if kr.Role != RoleAudit || kr.PubSHA256 == "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dataDir, kr.Name))
		if err != nil {
			return false
		}
		fp, err := PubFingerprintFromSigningKey(b)
		clear(b)
		if err != nil || fp != kr.PubSHA256 {
			return false
		}
		matched = true
	}
	return matched
}

// RestoreCustodyNote is the closing line of a verified restore. It says key
// custody is intact only when the key serve will use for every sealer matches the
// probe the source recorded under its own key, or, for an older bundle without
// probes, when serve reads the file kept in the source's own data dir (same, from
// SameInstallation). Otherwise
// what the source sealed may not open, and the line names each key, where serve
// reads it and why. It never contains "key custody intact" then, so a script that
// looks for that phrase is not misled.
func RestoreCustodyNote(verdict string, m *Manifest, dataDir string, same bool, getenv func(string) string) string {
	probes := map[string]string{}
	for _, p := range m.SealerProbes {
		probes[p.Name] = p.Probe
	}
	var bad []string
	for _, k := range SealerKeyFiles {
		key, from, err := SealerKeyInEffect(dataDir, k.Name, k.Env, getenv)
		probe, recorded := probes[k.Name]
		var why string
		var readErr *fs.PathError
		switch {
		case errors.As(err, &readErr):
			why = from + " cannot be read: " + readErr.Err.Error()
		case err != nil:
			why = from + " is not a valid key"
		case recorded && probe == "":
			why = "dr backup had no valid key for it"
		case recorded && key == nil:
			why = from + " is missing"
		case recorded:
			if !hmac.Equal([]byte(SealerKeyProbe(key, k.Name)), []byte(probe)) {
				why = from + " is not the source's key"
			}
		case len(m.SealerProbes) > 0:
			why = "the backup recorded no probe for it"
		case same && key != nil && from != k.Env:
			// An older bundle over the source's own data dir, which kept its file.
		default:
			why = "this older bundle cannot confirm " + from
		}
		clear(key)
		if why != "" {
			bad = append(bad, fmt.Sprintf("%s (%s): %s", k.Name, k.Env, why))
		}
	}
	if len(bad) == 0 {
		return verdict + ": ledger continuity and key custody intact"
	}
	return fmt.Sprintf("%s: ledger continuity and signing keys intact, but sealer keys NOT restored: %s. "+
		"Provider keys, runtime secrets, TOTP enrolments, SSO and eventing secrets sealed on the source open "+
		"only under the source's keys. Put the source's key files in %s (mode 0600) with the variables unset, "+
		"or set the variables to the source's keys, before you start serve",
		verdict, strings.Join(bad, "; "), dataDir)
}
