// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestInvariant_NoSecretsInLogs is a repository-wide static guard on the
// rule that credential material must never reach a log sink. It complements the
// notify-path rule (deliver.go never logs the raw connector error) and the runtime
// redaction helpers with a regression backstop: it fails if any structured-log call
// pairs a KNOWN sensitive key with an un-redacted value, e.g.
//
//	log.Error("restore failed", "passphrase", req.Passphrase)   // <-- would fail here
//	slog.Info("wired", "signing_key", key)                      // <-- would fail here
//
// It is deliberately precise (low false-positive): it only inspects calls to logger
// sinks, only matches a string-literal argument whose NORMALIZED value is exactly a
// sensitive key name (so format strings like "passphrase: %s" never match), and
// treats the following value as safe when it is a literal or a redaction/hash/mask
// helper call. The sensitive-key list mirrors the high-confidence names from
// core/secret.IsCredentialBearingConfigKey.
func TestInvariant_NoSecretsInLogs(t *testing.T) {
	root := repoRoot(t)

	// High-confidence secret-VALUE names. Deliberately excludes the wrapping-key
	// REFERENCE labels "kek"/"dek"/"wrapkey": under CMEK custody the key-/data-
	// encryption-key MATERIAL lives in an external KMS and is never in-process, so
	// those slog labels carry a non-secret provider+key-id reference (audit
	// provenance), not key bytes — see cmd/olivares/auditkey.go. The real secret
	// material (private signing keys, passphrases, API keys, auth values) stays covered.
	sensitive := map[string]bool{
		"passphrase": true, "password": true, "privatekey": true, "signingkey": true,
		"secretkey": true, "authvalue": true, "apikey": true, "accesskey": true,
		"secretaccesskey": true, "clientsecret": true, "sessionkey": true,
		"encryptionkey": true, "masterkey": true, "licensekey": true, "otasigningkey": true,
	}
	logSinks := map[string]bool{
		"info": true, "infof": true, "infow": true, "warn": true, "warnf": true, "warnw": true,
		"error": true, "errorf": true, "errorw": true, "debug": true, "debugf": true, "debugw": true,
		"print": true, "printf": true, "println": true, "fatal": true, "fatalf": true,
		"panic": true, "panicf": true, "log": true, "logf": true, "logattrs": true,
	}

	var violations []string
	dirs := []string{"core", "modules", "cmd", "connectors", "sdk", "operator"}
	for _, d := range dirs {
		base := filepath.Join(root, d)
		_ = filepath.WalkDir(base, func(path string, de os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				if name := de.Name(); name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			if strings.Contains(string(src), "// Code generated") {
				return nil // generated files
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, src, 0)
			if perr != nil {
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !logSinks[strings.ToLower(sel.Sel.Name)] {
					return true
				}
				for i := 0; i+1 < len(call.Args); i++ {
					lit, ok := call.Args[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					key, err := strconv.Unquote(lit.Value)
					if err != nil {
						continue
					}
					if !sensitive[normalizeKey(key)] {
						continue
					}
					if !isRedactedValue(call.Args[i+1]) {
						pos := fset.Position(call.Args[i+1].Pos())
						rel, _ := filepath.Rel(root, pos.Filename)
						violations = append(violations,
							rel+":"+strconv.Itoa(pos.Line)+" logs sensitive key "+strconv.Quote(key)+" with an un-redacted value")
					}
				}
				return true
			})
			return nil
		})
	}

	for _, v := range violations {
		t.Errorf("no-secrets-in-logs: %s", v)
	}
}

func normalizeKey(s string) string {
	s = strings.ToLower(s)
	return strings.NewReplacer("_", "", "-", "", ".", "").Replace(s)
}

// isRedactedValue reports whether a logged value is safe: a literal, or a call to a
// redaction/masking/hashing/length helper (redact/scrub/mask/sanitize/fingerprint/
// hash/sha/len), which never emits the raw secret.
func isRedactedValue(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.CallExpr:
		name := ""
		switch fn := v.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		n := strings.ToLower(name)
		for _, safe := range []string{"redact", "scrub", "mask", "sanitize", "fingerprint", "hash", "sha", "len"} {
			if strings.Contains(n, safe) {
				return true
			}
		}
	}
	return false
}

// repoRoot walks up from the test's working directory to the workspace root
// (the directory holding go.work).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.work not found walking up from test dir")
		}
		dir = parent
	}
}
