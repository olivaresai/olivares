// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExecutableReadOnlyContract(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "appliance-answers")
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelBuild()
	build := exec.CommandContext(buildContext, "go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build command: %v\n%s", err, output)
	}
	valid, err := os.ReadFile("../../answers/testdata/cloud-init.json")
	if err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(dir, "host")
	if err := os.MkdirAll(filepath.Join(host, "var/lib/olivares"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"answers.json": valid, "etc-machine-id": []byte("existing-instance"), "var/lib/olivares/store": []byte("existing-user-data")} {
		if err := os.WriteFile(filepath.Join(host, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	adapters := filepath.Join(dir, "adapters")
	if err := os.Mkdir(adapters, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"systemctl", "cloud-init", "hostnamectl", "timedatectl", "nmcli", "ip", "olivares"} {
		if err := os.WriteFile(filepath.Join(adapters, name), []byte("#!/bin/sh\nprintf invoked > \"$HOME/adapter-invoked\"\nexit 97\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshot(t, host)
	cases := []struct {
		name   string
		args   []string
		input  string
		code   int
		stdout string
	}{
		{"validate file", []string{"validate", "--input", filepath.Join(host, "answers.json")}, "", 0, "valid appliance answers"},
		{"plan stdin", []string{"plan", "--input", "-"}, string(valid), 0, `"executable": false`},
		{"hostname numeric refused", []string{"plan", "--input", "-"}, strings.Replace(string(valid), `"hostname": "olivares.example.test"`, `"hostname": "1234"`, 1), 1, ""},
		{"hostname address refused", []string{"plan", "--input", "-"}, strings.Replace(string(valid), `"hostname": "olivares.example.test"`, `"hostname": "10.0.0.1"`, 1), 1, ""},
		{"hostname 65 refused", []string{"plan", "--input", "-"}, strings.Replace(string(valid), `"hostname": "olivares.example.test"`, `"hostname": "`+strings.Repeat("a", 63)+`.b"`, 1), 1, ""},
		{"hostname 64 complete", []string{"plan", "--input", "-"}, strings.Replace(string(valid), `"hostname": "olivares.example.test"`, `"hostname": "`+strings.Repeat("A", 62)+`.B"`, 1), 0, `"hostname": "` + strings.Repeat("a", 62) + `.b"`},
		{"mapped time zone refused", []string{"plan", "--input", "-"}, strings.Replace(string(valid), `"time.example.test"`, `"::ffff:192.0.2.1%eth0"`, 1), 1, ""},
		{"mapped time unzoned", []string{"plan", "--input", "-"}, strings.Replace(string(valid), `"time.example.test"`, `"::ffff:192.0.2.1"`, 1), 0, `"192.0.2.1"`},
		{"mapped console zone refused", []string{"plan", "--input", "-"}, strings.Replace(string(valid), "https://olivares.example.test", "https://[::ffff:192.0.2.1%25eth0]", 1), 1, ""},
		{"mapped console unzoned", []string{"plan", "--input", "-"}, strings.Replace(string(valid), "https://olivares.example.test", "https://[::ffff:192.0.2.1]", 1), 0, `"public_console_url": "https://192.0.2.1"`},
		{"invalid document", []string{"plan", "--input", "-"}, strings.Replace(string(valid), "appliance-answers/v1", "DO-NOT-PRINT", 1), 1, ""},
		{"missing file", []string{"plan", "--input", filepath.Join(host, "DO-NOT-PRINT")}, "", 2, ""},
		{"ambiguous files", []string{"plan", "--input", "-", "--input", "DO-NOT-PRINT"}, string(valid), 2, ""},
		{"no source", []string{"plan"}, "", 2, ""},
		{"no apply", []string{"apply", "--input", "-"}, string(valid), 2, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			commandContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(commandContext, binary, tc.args...)
			cmd.Dir = host
			cmd.Env = []string{"HOME=" + host, "PATH=" + adapters, "TMPDIR=" + host, "OLIVARES_DATA_DIR=" + filepath.Join(host, "var/lib/olivares")}
			cmd.Stdin = strings.NewReader(tc.input)
			var out, diagnostic bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &diagnostic
			err := cmd.Run()
			code := 0
			if err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, diagnostic.String())
			}
			if tc.code == 0 {
				if diagnostic.Len() != 0 || !strings.Contains(out.String(), tc.stdout) {
					t.Fatalf("success channels: stdout=%s stderr=%s", out.String(), diagnostic.String())
				}
			} else if out.Len() != 0 || diagnostic.Len() == 0 {
				t.Fatalf("failure channels: stdout=%s stderr=%s", out.String(), diagnostic.String())
			}
			if strings.Contains(out.String()+diagnostic.String(), "DO-NOT-PRINT") {
				t.Fatal("input value escaped")
			}
		})
	}
	if after := snapshot(t, host); !reflect.DeepEqual(before, after) {
		t.Fatalf("validate/plan changed host or data: before=%v after=%v", before, after)
	}
}

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String() + " " + info.ModTime().UTC().String()
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			value += " " + hex.EncodeToString(sum[:])
		}
		result[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
