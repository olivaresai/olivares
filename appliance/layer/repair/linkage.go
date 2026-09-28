// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package repair

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
	"time"
	"unicode/utf8"
)

// LinkageSchema is the linkage record's closed schema.
const LinkageSchema = "olivares.ai/network-recovery-power-link/v1"

// LinkageDirectory is the linkage records' protected directory: root's, mode 0700, on persistent
// host state outside the root snapshots.
const LinkageDirectory = "/var/lib/olivares-power/recovery"

// MaxLinkageBytes bounds a linkage record.
const MaxLinkageBytes = 4096

// ActionReboot is the one action a recovery reboot links: the existing power verb, reboot.
const ActionReboot = "reboot"

// PendingNetwork names the one network window a recovery reboot is confirmed for, in the guard's own
// types: its operation id, the boot it was opened in, its window generation, and the SHA-256 of its
// status facts as the operator saw them (StatusDigest).
type PendingNetwork struct {
	OperationID      string `json:"operation_id"`
	BootID           string `json:"boot_id"`
	WindowGeneration string `json:"window_generation"`
	StatusDigest     string `json:"status_digest"`
}

// Linkage is the durable record of an authorized recovery reboot and the pending network window it
// was confirmed for, written before the power helper is asked. It proves the recorded intent only:
// never that the reboot happened or that recovery succeeded. It holds no credential, token or
// reusable authority: the sign-in is named by its non-secret audit reference.
type Linkage struct {
	Schema              string         `json:"schema"`
	PowerOperationID    string         `json:"power_operation_id"`
	Action              string         `json:"action"`
	ConfirmedPlanDigest string         `json:"confirmed_plan_digest"`
	SignInSessionRef    string         `json:"sign_in_session_ref"`
	CreatedAtUTC        string         `json:"created_at_utc"`
	PendingNetwork      PendingNetwork `json:"pending_network"`
}

// Errors of the linkage store. Each refuses the recovery reboot; none authorizes a replay.
var (
	// ErrLinkageInvalid refuses a record outside the closed schema.
	ErrLinkageInvalid = errors.New("the linkage record is outside its closed schema")
	// ErrLinkageExists refuses a power operation id that already has a record, complete or not.
	ErrLinkageExists = errors.New("a linkage record exists for this power operation id: reconcile it by its id, never replay it")
	// ErrLinkageCustody refuses a directory or a record whose custody does not hold.
	ErrLinkageCustody = errors.New("the linkage directory or record is not in its protected custody")
)

// Validate refuses a record whose values are outside the closed schema's types: 32-hex operation
// ids and window generation as the guard and the operation engine write them, the boot id as the
// kernel states it, lowercase SHA-256 digests, a 128-bit session reference and an RFC 3339 UTC time.
func (l Linkage) Validate() error {
	createdAt, err := time.Parse(time.RFC3339, l.CreatedAtUTC)
	switch {
	case l.Schema != LinkageSchema,
		!lowerHex(l.PowerOperationID, 32),
		l.Action != ActionReboot,
		!lowerHex(l.ConfirmedPlanDigest, 64),
		!lowerHex(l.SignInSessionRef, 32),
		err != nil || createdAt.Location() != time.UTC || createdAt.Format(time.RFC3339) != l.CreatedAtUTC,
		l.PendingNetwork.Validate() != nil:
		return ErrLinkageInvalid
	}
	return nil
}

// Validate refuses a pending window outside the guard's types.
func (p PendingNetwork) Validate() error {
	if !lowerHex(p.OperationID, 32) || !bootIDShape(p.BootID) || !lowerHex(p.WindowGeneration, 32) || !lowerHex(p.StatusDigest, 64) {
		return ErrLinkageInvalid
	}
	return nil
}

// LinkageName is the record's file name: the SHA-256 of the canonical power operation id's bytes,
// in lowercase hexadecimal, and .json. It is never text a caller supplied.
func LinkageName(powerOperationID string) (string, error) {
	if !lowerHex(powerOperationID, 32) {
		return "", ErrLinkageInvalid
	}
	sum := sha256.Sum256([]byte(powerOperationID))
	return hex.EncodeToString(sum[:]) + ".json", nil
}

// encodeLinkage is the record's bytes: one JSON object in the schema's field order.
func encodeLinkage(l Linkage) ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(l)
	if err != nil || len(data) > MaxLinkageBytes {
		return nil, ErrLinkageInvalid
	}
	return data, nil
}

// linkageFields and pendingFields are the closed schema's exact keys.
var (
	linkageFields = []string{"schema", "power_operation_id", "action", "confirmed_plan_digest", "sign_in_session_ref",
		"created_at_utc", "pending_network"}
	pendingFields = []string{"operation_id", "boot_id", "window_generation", "status_digest"}
)

// DecodeLinkage reads one record: valid UTF-8 of at most 4096 bytes holding exactly one JSON object
// with every field of the closed schema, each once, none null and no other, at every level.
func DecodeLinkage(data []byte) (Linkage, error) {
	if len(data) > MaxLinkageBytes || !utf8.Valid(data) {
		return Linkage{}, ErrLinkageInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := exactObject(decoder, linkageFields, map[string][]string{"pending_network": pendingFields}); err != nil {
		return Linkage{}, ErrLinkageInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Linkage{}, ErrLinkageInvalid
	}
	var l Linkage
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&l); err != nil {
		return Linkage{}, ErrLinkageInvalid
	}
	return l, l.Validate()
}

// exactObject reads one object whose keys are exactly fields, each once; a key named in nested is
// itself an exact object of those fields, and every other value is a string.
func exactObject(d *json.Decoder, fields []string, nested map[string][]string) error {
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrLinkageInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		keyToken, err := d.Token()
		if err != nil {
			return ErrLinkageInvalid
		}
		key, ok := keyToken.(string)
		if !ok || seen[key] || !contains(fields, key) {
			return ErrLinkageInvalid
		}
		seen[key] = true
		if inner, ok := nested[key]; ok {
			if err := exactObject(d, inner, nil); err != nil {
				return err
			}
			continue
		}
		value, err := d.Token()
		if _, isString := value.(string); err != nil || !isString {
			return ErrLinkageInvalid
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') || len(seen) != len(fields) {
		return ErrLinkageInvalid
	}
	return nil
}

// LinkageStore keeps the linkage records in Dir, which must be a directory, never a link, owned by
// Owner (root on an installed appliance) and mode 0700.
type LinkageStore struct {
	Dir   string
	Owner uint32
}

// Linker commits a linkage record durably.
type Linker interface {
	Commit(Linkage) error
}

// Commit writes l relative to the opened protected directory: created exclusively with no link
// followed, final mode 0600, checked as one regular file of the store's owner with one link and the
// written size, synced, and the directory synced. A record already under the name, complete, partial
// or not a file at all, refuses with ErrLinkageExists; nothing is ever replaced or removed to clear
// it. A failure after creation leaves what was written for named reconciliation.
func (s LinkageStore) Commit(l Linkage) error {
	data, err := encodeLinkage(l)
	if err != nil {
		return err
	}
	name, err := LinkageName(l.PowerOperationID)
	if err != nil {
		return err
	}
	root, err := s.open()
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrLinkageExists
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	chmodErr := f.Chmod(0o600)
	syncErr := f.Sync()
	info, statErr := f.Stat()
	closeErr := f.Close()
	if err := errors.Join(writeErr, chmodErr, syncErr, statErr, closeErr); err != nil {
		return err
	}
	if uid, links, ok := fileFacts(info); !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		uid != s.Owner || links != 1 || info.Size() != int64(len(data)) {
		return ErrLinkageCustody
	}
	d, err := root.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Read reads the record of powerOperationID: a regular file of the store's owner, mode 0600, one
// link, at most 4096 bytes, never a link, the same file when opened as when measured, and a record
// of the closed schema for that very id.
func (s LinkageStore) Read(powerOperationID string) (Linkage, error) {
	name, err := LinkageName(powerOperationID)
	if err != nil {
		return Linkage{}, err
	}
	root, err := s.open()
	if err != nil {
		return Linkage{}, err
	}
	defer root.Close()
	measured, err := root.Lstat(name)
	if err != nil {
		return Linkage{}, err
	}
	if uid, links, ok := fileFacts(measured); !ok || !measured.Mode().IsRegular() || measured.Mode().Perm() != 0o600 ||
		uid != s.Owner || links != 1 || measured.Size() > MaxLinkageBytes {
		return Linkage{}, ErrLinkageCustody
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Linkage{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(measured, opened) {
		return Linkage{}, ErrLinkageCustody
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxLinkageBytes+1))
	if err != nil {
		return Linkage{}, err
	}
	l, err := DecodeLinkage(data)
	if err != nil || l.PowerOperationID != powerOperationID {
		return Linkage{}, ErrLinkageInvalid
	}
	return l, nil
}

// open opens the store's directory after measuring its custody: a directory, not a link, of the
// store's owner, mode 0700.
func (s LinkageStore) open() (*os.Root, error) {
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return nil, err
	}
	if uid, _, ok := fileFacts(info); !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || uid != s.Owner {
		return nil, ErrLinkageCustody
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, ErrLinkageCustody
	}
	return root, nil
}

// lowerHex reports whether s is exactly n lowercase hexadecimal digits.
func lowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// bootIDShape reports whether s is a boot id as the kernel states it: a lowercase UUID, 8-4-4-4-12.
func bootIDShape(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			if !lowerHex(s[i:i+1], 1) {
				return false
			}
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
