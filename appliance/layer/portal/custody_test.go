// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every custody test judges a host without SELinux unless it says otherwise: the host that runs
// the tests may enforce a policy of its own, whose labels are not the appliance's.
func init() { selinuxEnforce = filepath.Join(os.TempDir(), "no-selinuxfs-here", "enforce") }

// selinuxEnforceAt points the custody check at path as selinuxfs's enforce switch for the rest of
// the test.
func selinuxEnforceAt(t *testing.T, path string) {
	t.Helper()
	production := selinuxEnforce
	selinuxEnforce = path
	t.Cleanup(func() { selinuxEnforce = production })
}

// custodyDirectories writes an operator's TLS directory, its key with keyMode, and the
// copies the service manager delivers to the service, the key at 0400.
func custodyDirectories(t *testing.T, keyMode os.FileMode) (source, delivered string) {
	t.Helper()
	certPEM, keyPEM, _ := newTLSPair(t)
	source, delivered = t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(source, CertificateFile), certPEM, 0o644)
	writeFile(t, filepath.Join(source, KeyFile), keyPEM, keyMode)
	writeFile(t, filepath.Join(delivered, CertificateFile), certPEM, 0o400)
	writeFile(t, filepath.Join(delivered, KeyFile), keyPEM, 0o400)
	return source, delivered
}

// environment names the operator's TLS directory and the credentials directory as the
// service unit does.
func environment(source, delivered string) map[string]string {
	return map[string]string{"OLIVARES_PORTAL_TLS_DIRECTORY": source, "CREDENTIALS_DIRECTORY": delivered}
}

// anotherUser returns a uid that is neither root nor this process's user.
func anotherUser() uint32 {
	if uint32(os.Getuid()) != 65534 {
		return 65534
	}
	return 65533
}

// ownedByAnotherUser makes the owner source report anotherUser for the file at path, for
// the rest of the test.
func ownedByAnotherUser(t *testing.T, path string) {
	t.Helper()
	target, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	production := fileOwner
	fileOwner = func(info os.FileInfo) (uint32, bool) {
		if os.SameFile(info, target) {
			return anotherUser(), true
		}
		return production(info)
	}
	t.Cleanup(func() { fileOwner = production })
}

func chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestPortalCustody_MeasuresTheOperatorsFilesNotTheDeliveredCopy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T) map[string]string
		refusal string // the reason the console reports, or "" when custody holds
	}{
		{"delivered copy shows the service manager's ACL mask", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			chmod(t, filepath.Join(delivered, KeyFile), 0o440)
			return environment(source, delivered)
		}, ""},
		{"operator's key read-only for its owner", func(t *testing.T) map[string]string {
			return environment(custodyDirectories(t, 0o400))
		}, ""},
		{"operator's key readable by others, delivered copy private", func(t *testing.T) map[string]string {
			return environment(custodyDirectories(t, 0o644))
		}, "key is not mode 0600 or 0400"},
		{"operator's key readable by its group", func(t *testing.T) map[string]string {
			return environment(custodyDirectories(t, 0o640))
		}, "key is not mode 0600 or 0400"},
		{"operator's key executable", func(t *testing.T) map[string]string {
			return environment(custodyDirectories(t, 0o700))
		}, "key is not mode 0600 or 0400"},
		{"operator's key is a symbolic link", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			elsewhere := t.TempDir()
			writeTLSPair(t, elsewhere, 0o600)
			symlink(t, filepath.Join(elsewhere, KeyFile), filepath.Join(source, KeyFile))
			return environment(source, delivered)
		}, "key is not a regular file"},
		{"operator's key absent", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			if err := os.Remove(filepath.Join(source, KeyFile)); err != nil {
				t.Fatal(err)
			}
			return environment(source, delivered)
		}, "key is absent or cannot be measured"},
		{"operator's certificate writable by its group", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			chmod(t, filepath.Join(source, CertificateFile), 0o664)
			return environment(source, delivered)
		}, "certificate is writable by group or others"},
		{"operator's directory writable by its group", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			chmod(t, source, 0o775)
			return environment(source, delivered)
		}, "TLS source directory is writable by group or others"},
		{"operator's directory is a symbolic link", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			link := filepath.Join(t.TempDir(), "tls")
			symlink(t, source, link)
			return environment(link, delivered)
		}, "TLS source directory is not a directory"},
		{"no operator directory configured", func(t *testing.T) map[string]string {
			_, delivered := custodyDirectories(t, 0o600)
			return environment("", delivered)
		}, "no TLS source directory"},
		{"delivered key is a symbolic link", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			symlink(t, filepath.Join(source, KeyFile), filepath.Join(delivered, KeyFile))
			return environment(source, delivered)
		}, "delivered key is not a regular file"},
		{"delivered key does not match the certificate", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			_, otherKey, _ := newTLSPair(t)
			if err := os.Remove(filepath.Join(delivered, KeyFile)); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(delivered, KeyFile), otherKey, 0o400)
			return environment(source, delivered)
		}, "certificate and key do not form a pair"},
		{"delivered pair is not the operator's", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			certPEM, keyPEM, _ := newTLSPair(t)
			for name, data := range map[string][]byte{CertificateFile: certPEM, KeyFile: keyPEM} {
				path := filepath.Join(delivered, name)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				writeFile(t, path, data, 0o400)
			}
			return environment(source, delivered)
		}, "delivered certificate is not the operator's certificate"},
		{"operator's pair replaced after delivery", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			certPEM, keyPEM, _ := newTLSPair(t)
			writeFile(t, filepath.Join(source, CertificateFile), certPEM, 0o644)
			writeFile(t, filepath.Join(source, KeyFile), keyPEM, 0o600)
			return environment(source, delivered)
		}, "delivered certificate is not the operator's certificate"},
		{"operator's key owned by another user", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			ownedByAnotherUser(t, filepath.Join(source, KeyFile))
			return environment(source, delivered)
		}, "key has an owner other than root or this service"},
		{"operator's directory owned by another user", func(t *testing.T) map[string]string {
			source, delivered := custodyDirectories(t, 0o600)
			ownedByAnotherUser(t, source)
			return environment(source, delivered)
		}, "TLS source directory has an owner other than root or this service"},
		{"operator's key owned by another user, changed as root", func(t *testing.T) map[string]string {
			if os.Getuid() != 0 {
				t.Skip("changing a file's owner needs root")
			}
			source, delivered := custodyDirectories(t, 0o600)
			if err := os.Chown(filepath.Join(source, KeyFile), int(anotherUser()), int(anotherUser())); err != nil {
				t.Fatal(err)
			}
			return environment(source, delivered)
		}, "key has an owner other than root or this service"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.prepare(t)
			var diagnostics bytes.Buffer
			if code := Run(func(key string) string { return env[key] }, os.Getpid(), &diagnostics); code != 1 {
				t.Fatalf("Run returned %d; without passed sockets it returns 1", code)
			}
			got := diagnostics.String()
			for _, secret := range []string{env["OLIVARES_PORTAL_TLS_DIRECTORY"], env["CREDENTIALS_DIRECTORY"], "PRIVATE KEY"} {
				if secret != "" && strings.Contains(got, secret) {
					t.Fatalf("diagnostics carry a path or key material:\n%s", got)
				}
			}
			if tc.refusal == "" {
				if strings.Contains(got, "disabled") || !strings.Contains(got, errNoSockets.Error()) {
					t.Fatalf("custody should hold, so the console goes on to its sockets:\n%s", got)
				}
				return
			}
			if !strings.Contains(got, "disabled: TLS custody is unverified: "+tc.refusal) {
				t.Fatalf("want the refusal %q:\n%s", tc.refusal, got)
			}
		})
	}
}

// Under SELinux, enforcing or permissive, the operator's key must carry the key's own type: a key
// copied into the directory takes the directory's type until it is relabeled, and the console's
// domain may read that type. The label is read with lgetxattr, never by opening the key. A host
// without SELinux judges the key as before.
func TestPortalCustody_RefusesAKeyNotLabeledThePortalKeyTypeUnderSELinux(t *testing.T) {
	const wrongType = "key is not labeled " + PortalKeyType
	for _, tc := range []struct {
		name, enforce, label string
		readErr              error
		verified             bool
		reason               string
	}{
		{"enforcing, the key's own type", "1", "system_u:object_r:olivares_portal_key_t:s0", nil, true, ""},
		{"enforcing, the key's own type at a range", "1", "system_u:object_r:olivares_portal_key_t:s0-s0:c0.c1023", nil, true, ""},
		{"enforcing, the directory's type", "1", "system_u:object_r:olivares_portal_etc_t:s0", nil, false, wrongType},
		{"permissive, the directory's type", "0", "system_u:object_r:olivares_portal_etc_t:s0", nil, false, wrongType},
		{"enforcing, a label that is only a type name", "1", PortalKeyType, nil, false, wrongType},
		{"enforcing, no label at all", "1", "", errors.New("no data available"), false, "key's SELinux label cannot be read"},
		{"no SELinux on this host", "", "system_u:object_r:olivares_portal_etc_t:s0", nil, true, ""},
		// A switch that cannot be measured (here its parent is a file) is not an absent one: the
		// check never switches itself off because a stat was refused.
		{"a switch that cannot be measured", "unmeasurable", "system_u:object_r:olivares_portal_etc_t:s0", nil, false, wrongType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, delivered := custodyDirectories(t, 0o600)
			enforce := filepath.Join(t.TempDir(), "enforce")
			switch tc.enforce {
			case "":
			case "unmeasurable":
				notADirectory := filepath.Join(t.TempDir(), "selinux")
				writeFile(t, notADirectory, []byte("a file, not selinuxfs\n"), 0o644)
				enforce = filepath.Join(notADirectory, "enforce")
			default:
				writeFile(t, enforce, []byte(tc.enforce+"\n"), 0o644)
			}
			selinuxEnforceAt(t, enforce)
			var asked []string
			production := keyLabel
			keyLabel = func(path string) (string, error) {
				asked = append(asked, path)
				return tc.label, tc.readErr
			}
			t.Cleanup(func() { keyLabel = production })

			custody, pair := CheckTLSCustody(source, delivered)
			if custody.Verified != tc.verified || custody.Reason != tc.reason || (pair != nil) != tc.verified {
				t.Fatalf("custody %+v (pair %t), want verified %t with reason %q", custody, pair != nil, tc.verified, tc.reason)
			}
			var want []string
			if tc.enforce != "" {
				want = []string{filepath.Join(source, KeyFile)}
			}
			if !slices.Equal(asked, want) {
				t.Fatalf("the key's label was read for %q, want %q", asked, want)
			}
		})
	}
}
