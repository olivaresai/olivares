// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package opgate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxRecordBytes = 64 * 1024

var ErrRecordNotRegular = errors.New("opgate: restore control record is not a regular file")

func regularRecordPath(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	if !info.Mode().IsRegular() {
		return nil, ErrRecordNotRegular
	}
	return info, nil
}

// Absence is established only by the first Lstat. Disappearance or replacement
// after observation is a refusal. O_NONBLOCK also protects the open-time FIFO race.
// These local checks require a stable controlled namespace, as the lease does.
func readRecordFile(path string) ([]byte, bool, error) {
	before, err := regularRecordPath(path)
	if err != nil {
		return nil, true, err
	}
	if before == nil {
		return nil, false, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openNoFollow|openNonBlock, 0)
	if err != nil {
		return nil, true, err
	}
	defer f.Close() //nolint:errcheck // read-only descriptor
	opened, err := f.Stat()
	if err != nil {
		return nil, true, err
	}
	if !opened.Mode().IsRegular() {
		return nil, true, ErrRecordNotRegular
	}
	if !os.SameFile(before, opened) {
		return nil, true, errors.New("opgate: record identity changed during open")
	}
	if opened.Size() > maxRecordBytes {
		return nil, true, errors.New("opgate: record exceeds the size limit")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return nil, true, err
	}
	if len(raw) > maxRecordBytes {
		return nil, true, errors.New("opgate: record exceeds the size limit")
	}
	after, err := regularRecordPath(path)
	if err != nil {
		return nil, true, err
	}
	final, err := f.Stat()
	if err != nil {
		return nil, true, err
	}
	if after == nil || !os.SameFile(opened, after) || final.Size() != opened.Size() || !final.ModTime().Equal(opened.ModTime()) {
		return nil, true, errors.New("opgate: record identity or contents changed during read")
	}
	return raw, true, nil
}

// Decode the entire token stream first: encoding/json's struct decoder alone
// accepts duplicate members and case-insensitive aliases of field names.
func closedJSON(raw []byte) error {
	if len(raw) > maxRecordBytes || !utf8.Valid(raw) {
		return errors.New("opgate: invalid or oversized JSON record")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 16 {
			return errors.New("opgate: JSON nesting exceeds record format")
		}
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("opgate: invalid object member")
				}
				if seen[name] {
					return fmt.Errorf("opgate: duplicate JSON member %q", name)
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("opgate: invalid JSON delimiter")
		}
		_, err = dec.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("opgate: record must contain one JSON document and EOF")
	}
	return nil
}
func closedObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, errors.New("opgate: record format requires an object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, name := range required {
		allowed[name] = true
		v, ok := obj[name]
		if !ok {
			return nil, fmt.Errorf("opgate: required member %q is absent", name)
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) && name != "keyset" {
			return nil, fmt.Errorf("opgate: member %q cannot be null", name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
		if v, ok := obj[name]; ok && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, fmt.Errorf("opgate: member %q cannot be null", name)
		}
	}
	for name := range obj {
		if !allowed[name] {
			return nil, fmt.Errorf("opgate: unknown member %q", name)
		}
	}
	return obj, nil
}
func decodeRecord(raw []byte) (Record, error) {
	var rec Record
	if err := closedJSON(raw); err != nil {
		return rec, err
	}
	obj, err := closedObject(raw, []string{"format", "revision", "op_id", "state", "enrolled", "plan_sha256", "destination", "keyset", "observed_at"}, []string{"journal_path"})
	if err != nil {
		return rec, err
	}
	if _, err := closedObject(obj["destination"], []string{"engine", "canonical_path", "sqlite_file", "database", "schema", "system_identifier"}, nil); err != nil {
		return rec, err
	}
	if !bytes.Equal(bytes.TrimSpace(obj["keyset"]), []byte("null")) {
		keyset, err := closedObject(obj["keyset"], []string{"format", "sha256", "keys"}, nil)
		if err != nil {
			return rec, err
		}
		var keys []json.RawMessage
		if err := json.Unmarshal(keyset["keys"], &keys); err != nil {
			return rec, err
		}
		for _, key := range keys {
			if _, err := closedObject(key, []string{"purpose", "source", "public_sha256"}, nil); err != nil {
				return rec, err
			}
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return rec, err
	}
	if rec.State != StateComplete && !bytes.Equal(bytes.TrimSpace(obj["keyset"]), []byte("null")) {
		return rec, errors.New("opgate: noncomplete keyset must be explicitly null")
	}
	return rec, nil
}

// PostgresDestination compares fields independently, so dotted identifiers cannot
// alias one another. It is public metadata, not a connection or a destination lease.
type PostgresDestination struct {
	Database         string
	Schema           string
	SystemIdentifier string
}

func (d PostgresDestination) Validate() error {
	if d.Database == "" || d.Schema == "" || strings.ContainsRune(d.Database, 0) || strings.ContainsRune(d.Schema, 0) {
		return errors.New("opgate: incomplete PostgreSQL destination")
	}
	n, err := strconv.ParseUint(d.SystemIdentifier, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != d.SystemIdentifier {
		return errors.New("opgate: invalid PostgreSQL system identifier")
	}
	return nil
}
func (d Destination) Postgres() PostgresDestination {
	return PostgresDestination{d.Database, d.Schema, d.SystemIdentifier}
}
func (d Destination) validate() error {
	if d.CanonicalPath == "" || !filepath.IsAbs(d.CanonicalPath) || strings.ContainsRune(d.CanonicalPath, 0) {
		return errors.New("opgate: record names no absolute destination path")
	}
	switch d.Engine {
	case "sqlite":
		if !filepath.IsAbs(d.SQLiteFile) || strings.ContainsRune(d.SQLiteFile, 0) {
			return errors.New("opgate: SQLite record requires its frozen absolute file target")
		}
		if d.Database != "" || d.Schema != "" || d.SystemIdentifier != "" {
			return errors.New("opgate: SQLite destination carries PostgreSQL fields")
		}
	case "postgres":
		if d.SQLiteFile != "" {
			return errors.New("opgate: PostgreSQL destination carries a SQLite target")
		}
		return d.Postgres().Validate()
	default:
		return errors.New("opgate: unsupported destination engine")
	}
	return nil
}
