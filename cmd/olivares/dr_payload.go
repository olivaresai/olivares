// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	coreengine "github.com/olivaresai/olivares/core/engine"
)

const drTOCMaxBytes = 8 << 20

// drPayloadProof binds classification to a staged file's identity and bytes.
type drPayloadProof struct {
	path      string
	info      os.FileInfo
	sum       string
	size      int64
	inventory bool
}

func (p *drPayloadProof) unchanged() error {
	if p == nil {
		return nil
	} // A physical PITR companion has no logical payload.
	info, err := os.Lstat(p.path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(p.info, info) {
		return fmt.Errorf("store payload changed after validation; restore refused")
	}
	sum, size, err := dr.FileSHA256(p.path)
	after, statErr := os.Lstat(p.path)
	if err != nil || statErr != nil || !os.SameFile(info, after) ||
		size != p.size || sum != p.sum || !info.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("store payload changed after validation; restore refused")
	}
	return nil
}

func classifyDRPayload(ctx context.Context, engine, method, path, pgRestore string) (*drPayloadProof, error) {
	if engine == "postgres" && method == dr.MethodPITR {
		return nil, nil
	}
	if (engine != "sqlite" || method != dr.MethodVacuumInto) &&
		(engine != "postgres" || method != dr.MethodPgDump) {
		return nil, fmt.Errorf("unsupported store payload method for the selected engine")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("store payload is not a readable regular snapshot file")
	}
	sum, size, err := dr.FileSHA256(path)
	if err != nil {
		return nil, fmt.Errorf("store payload digest is unreadable")
	}
	proof := &drPayloadProof{path: path, info: info, sum: sum, size: size}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("store payload is unreadable")
	}
	var header [16]byte
	n, readErr := f.Read(header[:])
	closeErr := f.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("store payload header is unreadable")
	}
	if engine == "sqlite" {
		if n != len(header) || string(header[:]) != "SQLite format 3\x00" {
			return nil, fmt.Errorf("unsupported SQLite snapshot format")
		}
		err = classifySQLitePayload(ctx, path)
	} else {
		if n < 5 || string(header[:5]) != "PGDMP" {
			return nil, fmt.Errorf("unsupported PostgreSQL archive: expected a custom-format archive")
		}
		err = classifyPGArchive(ctx, pgRestore, path, proof)
	}
	if err != nil {
		return nil, err
	}
	if err := proof.unchanged(); err != nil {
		return nil, err
	}
	return proof, nil
}

func classifySQLitePayload(ctx context.Context, path string) (err error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return fmt.Errorf("SQLite snapshot metadata is unreadable")
	}
	defer func() {
		if closeErr := db.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("SQLite snapshot metadata is unreadable")
		}
	}()
	for _, object := range coreengine.DRCoordinationObjects() {
		if string(object.Kind) != "TABLE" {
			continue
		}
		var count int
		if e := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name = ? COLLATE NOCASE", object.Name).Scan(&count); e != nil {
			return fmt.Errorf("SQLite snapshot metadata is unreadable")
		}
		if count != 0 {
			return fmt.Errorf("store payload contains operational restore coordination; backup or restore refused")
		}
	}
	return nil
}

type drTOCBuffer struct{ buffer bytes.Buffer }

func (b *drTOCBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > drTOCMaxBytes {
		return 0, fmt.Errorf("PostgreSQL archive metadata exceeds the supported bound")
	}
	return b.buffer.Write(p)
}

func classifyPGArchive(ctx context.Context, bin, path string, proof *drPayloadProof) error {
	if bin == "" {
		bin = "pg_restore"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--list", path) // #nosec G204 -- operator-selected native client; no shell or DSN
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out drTOCBuffer
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("PostgreSQL archive metadata is unreadable or exceeds the supported bound; backup or restore refused")
	}
	return classifyPGTOC(&out.buffer, proof)
}

// Longer TOC descriptors precede their prefixes. Unquoted ambiguous identities refuse.
var drPGTOCKinds = []string{
	"PUBLICATION TABLES IN SCHEMA", "TEXT SEARCH CONFIGURATION", "TEXT SEARCH DICTIONARY",
	"TEXT SEARCH TEMPLATE", "TEXT SEARCH PARSER", "MATERIALIZED VIEW DATA", "SEQUENCE OWNED BY",
	"FOREIGN DATA WRAPPER", "DATABASE PROPERTIES", "PROCEDURAL LANGUAGE", "PUBLICATION TABLE",
	"CHECK CONSTRAINT", "FK CONSTRAINT", "SECURITY LABEL", "DEFAULT ACL", "EVENT TRIGGER",
	"MATERIALIZED VIEW", "OPERATOR CLASS", "OPERATOR FAMILY", "TABLE ATTACH", "TABLE DATA",
	"INDEX ATTACH", "SEQUENCE SET", "ACCESS METHOD", "USER MAPPING", "ROW SECURITY", "FOREIGN TABLE",
	"AGGREGATE", "COLLATION", "CONSTRAINT", "CONVERSION", "ENCODING", "EXTENSION", "FUNCTION",
	"PROCEDURE", "PUBLICATION", "SEARCHPATH", "SHELL TYPE", "STATISTICS", "STDSTRINGS",
	"SUBSCRIPTION", "TRANSFORM", "pg_largeobject", "DATABASE", "COMMENT", "DEFAULT", "DOMAIN",
	"OPERATOR", "POLICY", "SCHEMA", "SEQUENCE", "SERVER", "TRIGGER", "BLOBS", "BLOB",
	"TABLE", "INDEX", "RULE", "TYPE", "VIEW", "CAST", "ACL",
}

var drPGDumpVersion = regexp.MustCompile(`^(15|16|17|18)\.[0-9]+( \([^()\r\n]+\))?$`)

func classifyPGTOC(r io.Reader, proof *drPayloadProof) error {
	unsupported := func() error {
		return fmt.Errorf("unsupported or ambiguous PostgreSQL archive metadata; use a PostgreSQL 15, 16, 17, or 18 custom archive with unambiguous table and constraint identities")
	}
	limited := &io.LimitedReader{R: r, N: drTOCMaxBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	ids := map[uint64]bool{}
	inventory := coreengine.DRCoordinationObjects()
	format, client := false, false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ";") {
			field := strings.TrimSpace(strings.TrimPrefix(line, ";"))
			if strings.HasPrefix(field, "Format:") {
				if format || strings.TrimSpace(strings.TrimPrefix(field, "Format:")) != "CUSTOM" {
					return unsupported()
				}
				format = true
			}
			if strings.HasPrefix(field, "Dumped by pg_dump version:") {
				if client || !drPGDumpVersion.MatchString(strings.TrimSpace(strings.TrimPrefix(field, "Dumped by pg_dump version:"))) {
					return unsupported()
				}
				client = true
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 4)
		if len(fields) != 4 {
			return unsupported()
		}
		idText, ok := strings.CutSuffix(fields[0], ";")
		id, err := strconv.ParseUint(idText, 10, 31)
		if !ok || err != nil || id == 0 || ids[id] {
			return unsupported()
		}
		ids[id] = true
		for _, oid := range fields[1:3] {
			if _, err := strconv.ParseUint(oid, 10, 32); err != nil {
				return unsupported()
			}
		}
		body := fields[3]
		kind, rest := "", ""
		for _, candidate := range drPGTOCKinds {
			if after, ok := strings.CutPrefix(body, candidate+" "); ok {
				kind, rest = candidate, after
				break
			}
		}
		if kind == "" {
			return unsupported()
		}
		if len(strings.Fields(rest)) < 2 {
			return unsupported()
		}
		if kind == "FUNCTION" && proof != nil {
			identity := strings.Fields(strings.ReplaceAll(rest, "\"", ""))
			if len(identity) >= 2 && identity[0] == "public" && strings.HasPrefix(identity[1], "olivares_directory_inventory_v1(") {
				proof.inventory = true
			}
		}
		if kind != "TABLE" && kind != "TABLE DATA" && kind != "CONSTRAINT" && kind != "CHECK CONSTRAINT" && kind != "FK CONSTRAINT" {
			continue
		}
		identity := strings.Split(rest, " ")
		want := 3
		parent := ""
		if strings.Contains(kind, "CONSTRAINT") {
			want = 4
		}
		if len(identity) != want || strings.ContainsAny(rest, "\"\r\n\t") || identity[0] == "-" {
			return unsupported()
		}
		for _, value := range identity {
			if value == "" {
				return unsupported()
			}
		}
		name := identity[1]
		if want == 4 {
			parent, name = name, identity[2]
			kind = "CONSTRAINT"
		}
		for _, object := range inventory {
			if string(object.Kind) == kind && object.Schema == identity[0] && object.Name == name && object.Parent == parent {
				return fmt.Errorf("store payload contains operational restore coordination; backup or restore refused")
			}
		}
	}
	if scanner.Err() != nil || limited.N == 0 || !format || !client || len(ids) == 0 {
		return unsupported()
	}
	return nil
}

func drRestoreClientForDump(bin string) string {
	path, err := exec.LookPath(bin)
	if err != nil {
		return "pg_restore"
	}
	return filepath.Join(filepath.Dir(path), "pg_restore")
}
