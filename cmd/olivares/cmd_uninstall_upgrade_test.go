// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestUpgradeCarriesOwnershipRecordsInEveryValidJSONPresentation is the permanent form
// of the reproduction the independent review of 2026-09-05 ran by hand (R4).
//
// WHAT IT DRIVES, AND WHY EACH PIECE IS REAL. The manifest is a JSON document, and JSON
// says nothing about line breaks, indentation or the order of properties: the consumer
// accepts every presentation of the same object. The producer that has to carry those
// records across an engine upgrade — scripts/install-service.sh — used to read them with
// line-oriented expressions, so a semantically identical record written compactly, or
// with its properties reordered, carried NOTHING and a SUCCESSFUL upgrade silently
// dropped the recorded workspace and the AgentOps roles.
//
// So this test refuses to mirror the algorithm under test. It renders the REAL release
// entrypoint from scripts/render-release-installer.sh, packs the REAL adapter and
// templates of this tree into a tar.gz the entrypoint downloads and unpacks, and runs
// that entrypoint. Only the boundaries a hermetic test may not cross are ports: curl
// (transport), cosign (the cryptographic verifier — a passing port certifies NO
// signature) and systemctl (the service manager). The consumer side is the real CLI,
// executed in process by runCLI, before and after every upgrade.
func TestUpgradeCarriesOwnershipRecordsInEveryValidJSONPresentation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the systemd user adapter this exercises is linux-only; GOOS=%s", runtime.GOOS)
	}
	repo := repoRoot(t)
	for _, tool := range []string{"bash", "/bin/sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("this battery needs %s to run the real entrypoint: %v", tool, err)
		}
	}
	base := t.TempDir()
	home := filepath.Join(base, "operator-home")
	data := filepath.Join(home, "estate/data")
	workspace := filepath.Join(home, "projects/outside")
	fixture := filepath.Join(base, "fixture")
	fakebin := filepath.Join(base, "fakebin")
	mkdirs(t, home, filepath.Dir(data), fixture, fakebin)

	stageReleaseFixture(t, repo, fixture)
	writePort(t, filepath.Join(fakebin, "curl"), `#!/bin/sh
set -eu
url=""; dest=""
while [ "$#" -gt 0 ]; do
  case "$1" in -o) dest="$2"; shift 2 ;; http://*|https://*) url="$1"; shift ;; *) shift ;; esac
done
[ -n "$url" ] && [ -n "$dest" ]
cp "$FIXTURE/${url##*/}" "$dest"
`)
	writePort(t, filepath.Join(fakebin, "cosign"), "#!/bin/sh\nexit 0\n")
	writePort(t, filepath.Join(fakebin, "systemctl"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$COMMAND_LOG\"\nexit 0\n")
	commandLog := filepath.Join(base, "service-requests.log")

	rendered := filepath.Join(base, "olivares-install-26.9.0.sh")
	runTool(t, base, nil, "bash", filepath.Join(repo, "scripts/render-release-installer.sh"), "26.9.0", rendered)

	installerEnv := []string{
		"HOME=" + home,
		"FIXTURE=" + fixture,
		"COMMAND_LOG=" + commandLog,
		"OLIVARES_OS=linux",
		"OLIVARES_ARCH=amd64",
		"OLIVARES_GITHUB_URL=https://fixture.invalid",
		"PATH=" + fakebin + ":/usr/bin:/bin",
	}
	install := func() string {
		t.Helper()
		return runTool(t, base, installerEnv, "/bin/sh", rendered,
			"--version", "v26.9.0", "--bindir", filepath.Join(home, ".local/bin"),
			"--user", "--init", "systemd", "--data-dir", data)
	}
	install()

	manifestPath := filepath.Join(data, "install-manifest.json")
	first := readManifest(t, manifestPath)
	if first["mode"] != "user" || first["layout"] != "custom" {
		t.Fatalf("the first install did not record a custom user estate: %v", first)
	}

	// What install-agentops.sh adds on top, and the files those records describe.
	unit := filepath.Join(home, ".config/systemd/user/olivares.service")
	dropin := unit + ".d/agentops.conf"
	runtimeEnv := filepath.Join(home, ".config/olivares/agentops.env")
	mkdirs(t, workspace, filepath.Dir(dropin))
	writeFile(t, filepath.Join(workspace, "keep.txt"), "operator data", 0o600)
	writeFile(t, dropin, "[Service]\nReadWritePaths="+workspace+"\n", 0o644)
	writeFile(t, runtimeEnv, "OLIVARES_AGENTOPS=true\n", 0o640)
	annotated := withAgentOpsRecords(first, workspace, dropin, runtimeEnv)

	// The CLI's own view of the estate, taken once from the presentation the producer
	// writes. Every later presentation has to reach exactly this, or the upgrade lost
	// meaning on the way through.
	for _, tc := range []struct {
		name   string
		render func(map[string]any) []byte
	}{
		{"producer one-object-per-row form", producerForm},
		{"compact, no whitespace at all", compactForm},
		{"properties reordered and objects split over lines", reorderedMultilineForm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.render(annotated)
			if !json.Valid(body) {
				t.Fatalf("the fixture presentation is not valid JSON:\n%s", body)
			}
			var same map[string]any
			if err := json.Unmarshal(body, &same); err != nil {
				t.Fatal(err)
			}
			if !equalJSON(t, same, annotated) {
				t.Fatalf("the presentation changed the DOCUMENT, so it would prove nothing:\n%s", body)
			}
			writeFile(t, manifestPath, string(body), 0o600)
			writeFile(t, dropin, "[Service]\nReadWritePaths="+workspace+"\n", 0o644)
			writeFile(t, runtimeEnv, "OLIVARES_AGENTOPS=true\n", 0o640)

			// The consumer accepts it before the upgrade: this is a valid record, not a
			// broken one the producer may discard.
			t.Setenv("HOME", home)
			t.Setenv("PATH", fakebin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("COMMAND_LOG", commandLog)
			if out, err := runCLI(t, "uninstall", "--plan", "--data-dir", data); err != nil {
				t.Fatalf("the CLI rejected a valid v2 manifest BEFORE the upgrade: %v\n%s", err, out)
			}

			install()

			after := readManifest(t, manifestPath)
			if got := after["workspace_dir"]; got != workspace {
				t.Errorf("the upgrade lost the recorded workspace: workspace_dir = %v, want %q", got, workspace)
			}
			roles := roleIndex(t, after)
			for role, want := range map[string]struct {
				path, mode string
				managed    bool
			}{
				"dropin":      {dropin, "0644", true},
				"runtime-env": {runtimeEnv, "0640", true},
			} {
				entry, ok := roles[role]
				if !ok {
					t.Errorf("the upgrade lost the %s entry; roles carried: %v", role, sortedJSONKeys(roles))
					continue
				}
				if entry["path"] != want.path || entry["mode"] != want.mode || entry["managed"] != want.managed {
					t.Errorf("the %s entry changed meaning: %v", role, entry)
				}
			}
			// The adapter's own fields must not be collateral damage of reading the
			// previous record: this is where a loop variable called `mode` once
			// overwrote the install mode with a file mode, and the estate came out
			// recording `"mode": ""` for the engine to refuse afterwards.
			for _, field := range []string{"schema", "mode", "init", "layout", "data_dir", "config", "manifest"} {
				if after[field] != first[field] {
					t.Errorf("the upgrade changed %q from %v to %v", field, first[field], after[field])
				}
			}
			if out, err := runCLI(t, "uninstall", "--plan", "--data-dir", data); err != nil {
				t.Fatalf("the CLI rejected the manifest the upgrade produced: %v\n%s", err, out)
			} else if !strings.Contains(out, "keep         workspace") || !strings.Contains(out, "dropin") {
				t.Errorf("the plan after the upgrade does not disclose the carried records:\n%s", out)
			}
		})
	}

	// The lifecycle the carried records exist for, on that same estate: preserve, plan
	// again, purge — with the explicitly selected external workspace surviving all of it.
	t.Setenv("HOME", home)
	t.Setenv("PATH", fakebin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COMMAND_LOG", commandLog)
	if out, err := runCLI(t, "uninstall", "--preserve", "--data-dir", data); err != nil {
		t.Fatalf("preserve: %v\n%s", err, out)
	}
	if _, err := os.Lstat(dropin); !errors.Is(err, os.ErrNotExist) {
		t.Error("preserve left the managed drop-in")
	}
	if out, err := runCLI(t, "uninstall", "--plan", "--data-dir", data); err != nil {
		t.Fatalf("plan after preserve: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "uninstall", "--purge", "--yes", "--data-dir", data); err != nil {
		t.Fatalf("purge after preserve: %v\n%s", err, out)
	}
	for _, gone := range []string{data, runtimeEnv} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("purge left %s", gone)
		}
	}
	if got, err := os.ReadFile(filepath.Join(workspace, "keep.txt")); err != nil || string(got) != "operator data" {
		t.Fatalf("the external workspace did not survive the lifecycle: %q, %v", got, err)
	}
}

// --- fixture construction -------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts/install-service.sh")); err != nil {
		t.Fatalf("repository root not where this test expects it: %v", err)
	}
	return root
}

// stageReleaseFixture builds the archive the rendered entrypoint downloads: THIS tree's
// adapter and service templates, plus a stand-in engine binary, in GoReleaser's member
// layout, with the checksum file the entrypoint verifies and inert signature material.
func stageReleaseFixture(t *testing.T, repo, fixture string) {
	t.Helper()
	members := map[string][]byte{
		"olivares": []byte("#!/bin/sh\ncase \"${OLIVARES_NOT_A_REAL_KEY+x}\" in x) exit 1 ;; esac\nexit 0\n"),
	}
	members["scripts/install-service.sh"] = read(t, filepath.Join(repo, "scripts/install-service.sh"))
	entries, err := os.ReadDir(filepath.Join(repo, "packaging/service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			members["packaging/service/"+e.Name()] = read(t, filepath.Join(repo, "packaging/service", e.Name()))
		}
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mode := int64(0o644)
		if name == "olivares" || strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(members[name])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(members[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := "olivares_26.9.0_linux_amd64.tar.gz"
	writeFile(t, filepath.Join(fixture, archive), buf.String(), 0o644)
	sum := sha256.Sum256(buf.Bytes())
	writeFile(t, filepath.Join(fixture, "checksums.txt"), hex.EncodeToString(sum[:])+"  "+archive+"\n", 0o644)
	writeFile(t, filepath.Join(fixture, "checksums.txt.sig"), "inert verifier fixture\n", 0o644)
	writeFile(t, filepath.Join(fixture, "checksums.txt.pem"), "inert verifier fixture\n", 0o644)
}

// --- presentations of one and the same document ----------------------------------

// producerForm is the presentation the two producers actually write: two-space indent,
// ": " after every key, and each file entry on one row with its properties in the order
// path, role, mode, managed. It is the CONTROL of this test — the presentation the
// reviewed baseline did carry — so it is reproduced literally rather than through Go's
// map encoder, whose alphabetical key order is a different presentation again.
func producerForm(doc map[string]any) []byte {
	var b strings.Builder
	b.WriteString("{\n")
	rows := []string{}
	for _, key := range manifestKeyOrder(doc) {
		if key == "files" {
			entries := doc["files"].([]any)
			lines := make([]string, 0, len(entries))
			for _, entry := range entries {
				lines = append(lines, "    "+producerEntry(entry.(map[string]any)))
			}
			rows = append(rows, "  \"files\": [\n"+strings.Join(lines, ",\n")+"\n  ]")
			continue
		}
		rows = append(rows, "  "+string(mustMarshal(key))+": "+string(mustMarshal(doc[key])))
	}
	b.WriteString(strings.Join(rows, ",\n"))
	b.WriteString("\n}\n")
	return []byte(b.String())
}

func producerEntry(entry map[string]any) string {
	parts := make([]string, 0, len(entry))
	for _, key := range []string{"path", "role", "mode", "managed"} {
		if value, ok := entry[key]; ok {
			parts = append(parts, string(mustMarshal(key))+": "+string(mustMarshal(value)))
		}
	}
	for _, key := range sortedJSONKeys(entry) {
		switch key {
		case "path", "role", "mode", "managed":
		default:
			parts = append(parts, string(mustMarshal(key))+": "+string(mustMarshal(entry[key])))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func compactForm(doc map[string]any) []byte { return append(mustMarshal(doc), '\n') }

// reorderedMultilineForm writes the same object with its top-level properties in the
// reverse of the producer's order and every file entry split over lines with its own
// properties reordered too. Nothing about the DOCUMENT changes; only its presentation.
func reorderedMultilineForm(doc map[string]any) []byte {
	keys := manifestKeyOrder(doc)
	var b strings.Builder
	b.WriteString("{")
	for i := len(keys) - 1; i >= 0; i-- {
		key := keys[i]
		if i != len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n\t" + string(mustMarshal(key)) + " :")
		if key != "files" {
			b.WriteString(" " + string(mustMarshal(doc[key])))
			continue
		}
		b.WriteString(" [")
		for j, entry := range doc["files"].([]any) {
			if j > 0 {
				b.WriteString(",")
			}
			fields := entry.(map[string]any)
			names := sortedJSONKeys(fields)
			b.WriteString("\n\t\t{")
			for k := len(names) - 1; k >= 0; k-- {
				if k != len(names)-1 {
					b.WriteString(",")
				}
				b.WriteString("\n\t\t\t" + string(mustMarshal(names[k])) + ": " + string(mustMarshal(fields[names[k]])))
			}
			b.WriteString("\n\t\t}")
		}
		b.WriteString("\n\t]")
	}
	b.WriteString("\n}\n")
	return []byte(b.String())
}

// manifestKeyOrder is the order the adapter itself prints, with any other key appended
// so a document is never silently truncated by this helper.
func manifestKeyOrder(doc map[string]any) []string {
	known := []string{"schema", "mode", "init", "layout", "data_dir", "config", "workspace_dir", "files", "account", "manifest"}
	order := make([]string, 0, len(doc))
	seen := map[string]bool{}
	for _, key := range known {
		if _, ok := doc[key]; ok {
			order = append(order, key)
			seen[key] = true
		}
	}
	for _, key := range sortedJSONKeys(doc) {
		if !seen[key] {
			order = append(order, key)
		}
	}
	return order
}

func withAgentOpsRecords(doc map[string]any, workspace, dropin, runtimeEnv string) map[string]any {
	out := map[string]any{}
	for k, v := range doc {
		out[k] = v
	}
	out["workspace_dir"] = workspace
	files := append([]any{}, doc["files"].([]any)...)
	files = append(files,
		map[string]any{"path": dropin, "role": "dropin", "mode": "0644", "managed": true},
		map[string]any{"path": runtimeEnv, "role": "runtime-env", "mode": "0640", "managed": true})
	out["files"] = files
	return out
}

// --- small helpers ---------------------------------------------------------------

func mustMarshal(v any) []byte {
	body, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return body
}

func sortedJSONKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalJSON(t *testing.T, a, b any) bool {
	t.Helper()
	return bytes.Equal(canonical(t, a), canonical(t, b))
}

// canonical re-encodes through Go's map encoder, which sorts keys, so two documents
// compare equal exactly when they mean the same thing.
func canonical(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var round any
	if err := json.Unmarshal(body, &round); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(round)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readManifest(t *testing.T, path string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(read(t, path), &doc); err != nil {
		t.Fatalf("manifest at %s is not valid JSON: %v", path, err)
	}
	return doc
}

func roleIndex(t *testing.T, doc map[string]any) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, entry := range doc["files"].([]any) {
		fields, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("a files entry is not an object: %v", entry)
		}
		role, _ := fields["role"].(string)
		if _, dup := out[role]; dup {
			t.Fatalf("role %q is recorded twice", role)
		}
		out[role] = fields
	}
	return out
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writePort(t *testing.T, path, body string) {
	t.Helper()
	writeFile(t, path, body, 0o755)
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
}

func runTool(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}
