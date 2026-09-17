// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitDataDirSystemdUsesOnlyExecStart(t *testing.T) {
	// Every body is a unit systemd would actually act on: directives live in
	// [Service], because that is the only section whose ExecStart= it runs.
	const program = "/usr/bin/olivares"
	cases := []struct {
		name, body, want, wantErr string
	}{
		{"bare", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares --listen=127.0.0.1:8443\n", "/srv/olivares", ""},
		{"trailing argument", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n", "/srv/olivares", ""},
		{"value quoted with a space", "[Service]\n" + `ExecStart=/usr/bin/olivares serve --data-dir="/srv/olivares data" --listen=127.0.0.1:8443` + "\n", "/srv/olivares data", ""},
		{"argument quoted with a space", "[Service]\n" + `ExecStart=/usr/bin/olivares serve "--data-dir=/srv/olivares data" --listen=127.0.0.1:8443` + "\n", "/srv/olivares data", ""},
		{"single quotes", "[Service]\n" + `ExecStart=/usr/bin/olivares serve '--data-dir=/srv/olivares data'` + "\n", "/srv/olivares data", ""},
		{"package unit with continuation lines", "[Service]\nExecStart=/usr/bin/olivares serve \\\n  --data-dir=/var/lib/olivares \\\n  --listen=127.0.0.1:8443 \\\n  $OLIVARES_EXTRA_ARGS\n", "/var/lib/olivares", ""},
		{"whitespace around the equals sign", "[Service]\nExecStart = /usr/bin/olivares serve --data-dir=/srv/olivares\n", "/srv/olivares", ""},
		{"section headers with surrounding space", "  [Service]  \nExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n", "/srv/olivares", ""},
		{"later sections do not carry ExecStart back", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n[Install]\nWantedBy=multi-user.target\n", "/srv/olivares", ""},
		{"comment decoy is ignored", "[Service]\n# --data-dir=/srv/decoy is what we would like\nExecStart=/usr/bin/olivares serve --data-dir=/var/lib/olivares\n", "/var/lib/olivares", ""},
		{"semicolon comment decoy is ignored", "[Service]\n; ExecStart=/usr/bin/olivares serve --data-dir=/srv/decoy\nExecStart=/usr/bin/olivares serve --data-dir=/var/lib/olivares\n", "/var/lib/olivares", ""},
		{"Environment decoy is ignored", "[Service]\nEnvironment=FOO=--data-dir=/srv/decoy\nExecStart=/usr/bin/olivares serve --data-dir=/srv/real\n", "/srv/real", ""},
		{"ExecStartPre decoy is ignored", "[Service]\nExecStartPre=/usr/bin/olivares check --data-dir=/srv/decoy\nExecStart=/usr/bin/olivares serve --data-dir=/srv/real\nExecStartPost=/bin/true --data-dir=/srv/decoy2\n", "/srv/real", ""},
		{"ReadWritePaths decoy is ignored", "[Service]\nReadWritePaths=/srv/decoy\nExecStart=/usr/bin/olivares serve --data-dir=/srv/real\n", "/srv/real", ""},
		{"empty ExecStart resets earlier commands", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/old\nExecStart=\nExecStart=/usr/bin/olivares serve --data-dir=/srv/new\n", "/srv/new", ""},
		{"repeated identical ExecStart is accepted", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/a\nExecStart=/usr/bin/olivares serve --data-dir=/srv/a\n", "/srv/a", ""},
		{"conflicting ExecStart lines are ambiguous", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/a\nExecStart=/usr/bin/olivares serve --data-dir=/srv/b\n", "", "ambiguous"},
		{"two data dirs in one command are ambiguous", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/a --data-dir=/srv/b\n", "", "ambiguous"},
		{"no ExecStart", "[Service]\nEnvironment=HOME=/srv/olivares\n", "", "names no --data-dir"},
		{"only a comment", "[Service]\n# ExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n", "", "names no --data-dir"},
		{"space separated form is refused", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir /srv/olivares\n", "", "space-separated"},
		{"unbalanced quote", "[Service]\n" + `ExecStart=/usr/bin/olivares serve --data-dir="/srv/olivares` + "\n", "", "unbalanced"},
		{"escape sequence", "[Service]\n" + `ExecStart=/usr/bin/olivares serve --data-dir=/srv/oli\ vares` + "\n", "", "escape"},
		{"empty value", "[Service]\nExecStart=/usr/bin/olivares serve --data-dir=\n", "", "empty"},
		// The two bodies the independent review reproduced: a directive systemd
		// never runs, and one that runs another program.
		{"ExecStart outside [Service] never witnesses", "[Unit]\nExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n[Service]\nExecStart=/usr/bin/olivares serve\n", "", "outside [Service]"},
		{"ExecStart before any section never witnesses", "ExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n", "", "outside [Service]"},
		{"another program is not this estate's witness", "[Service]\nExecStart=/bin/echo --data-dir=/srv/olivares\n", "", "executes"},
		{"another olivares build is not this estate's witness", "[Service]\nExecStart=/opt/olivares/bin/olivares serve --data-dir=/srv/olivares\n", "", "executes"},
		{"a systemd prefix on the program is not accepted", "[Service]\nExecStart=-/usr/bin/olivares serve --data-dir=/srv/olivares\n", "", "executes"},
		{"a malformed section header does not open [Service]", "[Service extra\nExecStart=/usr/bin/olivares serve --data-dir=/srv/olivares\n", "", "outside [Service]"},
		{"only the engine's own directive counts", "[Service]\nExecStart=/bin/echo --data-dir=/srv/decoy\nExecStart=/usr/bin/olivares serve --data-dir=/srv/real\n", "/srv/real", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnitDataDir("systemd", tc.body, program)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %q, err %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, err %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestUnitDataDirOpenRCUsesOnlyCommandArgs(t *testing.T) {
	// OpenRC runs "$command $command_args", so every body carries the command=
	// that says which program the arguments belong to.
	const program = "/usr/local/bin/olivares"
	const command = "command=\"/usr/local/bin/olivares\"\n"
	cases := []struct {
		name, body, want, wantErr string
	}{
		{"rendered adapter with the extra-args expansion", command + "command_args=\"serve --data-dir=/srv/olivares --listen=127.0.0.1:8443 ${OLIVARES_EXTRA_ARGS:-}\"\n", "/srv/olivares", ""},
		{"command_args_base is the serve line, command_args copies it", command + "command_args_base=\"serve --data-dir=/srv/olivares --listen=127.0.0.1:8443\"\ncommand_args=\"$command_args_base\"\n", "/srv/olivares", ""},
		{"command_args_base disagrees with a literal command_args", command + "command_args_base=\"serve --data-dir=/srv/a\"\ncommand_args=\"serve --data-dir=/srv/b\"\n", "", "ambiguous"},
		{"embedded expansion is ambiguous", command + "command_args=\"serve --data-dir=/srv/$X/olivares\"\n", "", "embedded shell expansion"},
		{"command substitution is refused", command + "command_args=\"serve --data-dir=/srv/olivares `id`\"\n", "", "command substitution"},
		{"plain value", command + "command_args=\"serve --data-dir=/srv/olivares --listen=127.0.0.1:8443\"\n", "/srv/olivares", ""},
		{"comment and other variable decoys", "# command_args=\"serve --data-dir=/srv/decoy\"\n" + command + "output_log=\"/srv/decoy/olivares.log\"\ncommand_args='serve --data-dir=/srv/real'\n", "/srv/real", ""},
		{"conflicting assignments are ambiguous", command + "command_args=\"serve --data-dir=/srv/a\"\ncommand_args=\"serve --data-dir=/srv/b\"\n", "", "ambiguous"},
		{"sibling prefix does not match", command + "command_args=\"serve --data-dir=/srv/olivares2\"\n", "/srv/olivares2", ""},
		{"unbalanced quote", command + "command_args=\"serve --data-dir=/srv/olivares\n", "", "unbalanced"},
		{"no assignment", command + "name=\"Olivares AI\"\n", "", "names no --data-dir"},
		{"arguments without a command= do not witness", "command_args=\"serve --data-dir=/srv/olivares\"\n", "", "no command="},
		{"another program's command= does not witness", "command=\"/bin/echo\"\ncommand_args=\"serve --data-dir=/srv/olivares\"\n", "", "not /usr/local/bin/olivares"},
		{"conflicting command= assignments are ambiguous", command + "command=\"/usr/bin/olivares\"\ncommand_args=\"serve --data-dir=/srv/olivares\"\n", "", "ambiguous"},
		// The four bodies the independent review of 2026-09-05 reproduced (B1), through
		// the parser and the CLI: command_args_base= is not a directive openrc-run
		// consumes, so one that no command_args= copies in is a mention about this
		// estate, not the command line that starts it. It never supplies the witness.
		{"unreferenced base with no command_args at all", command + "command_args_base=\"serve --data-dir=/srv/stale\"\n", "", "names no --data-dir"},
		{"unreferenced base beside a literal command_args without --data-dir", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"serve --listen=127.0.0.1:8443\"\n", "", "names no --data-dir"},
		{"unreferenced base beside command_args reset to empty", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"\"\n", "", "names no --data-dir"},
		{"unreferenced base beside command_args expanding another variable", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"$other_args\"\n", "", "names no --data-dir"},
		// A copy that a later assignment replaces is a stale witness: the value
		// OpenRC runs is whichever assignment is in force, and this parser does not
		// interpret which, so every assignment has to prove the same directory.
		{"base copied then command_args reset to empty", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"$command_args_base\"\ncommand_args=\"\"\n", "", "no --data-dir in another"},
		{"base copied then command_args replaced by another variable", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"$command_args_base\"\ncommand_args=\"$other_args\"\n", "", "no --data-dir in another"},
		{"base copied then command_args replaced by a literal without --data-dir", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"$command_args_base\"\ncommand_args=\"serve --listen=127.0.0.1:8443\"\n", "", "no --data-dir in another"},
		{"a copy before the base is assigned proves nothing", command + "command_args=\"$command_args_base\"\ncommand_args_base=\"serve --data-dir=/srv/a\"\n", "", "names no --data-dir"},
		// The shell expands nothing inside single quotes: '$command_args_base' is
		// literal text handed to the engine, never the copy the shipped units make.
		{"single-quoted $command_args_base is literal text, not a copy", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args='$command_args_base'\n", "", "single quotes"},
		{"single-quoted expansion inside a literal command line is refused", command + "command_args='serve --data-dir=/srv/a $command_args_base'\n", "", "single quotes"},
		{"a modified base expansion is runtime input, not the copy", command + "command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"${command_args_base:-}\"\n", "", "names no --data-dir"},
		// What the shipped units actually render stays a witness: the copy at top
		// level, braced or bare, with the extra-args expansion appended in start_pre.
		{"braced copy of the base", command + "command_args_base=\"serve --data-dir=/srv/olivares\"\ncommand_args=\"${command_args_base}\"\n", "/srv/olivares", ""},
		{"base copied with the extra-args expansion appended", command + "command_args_base=\"serve --data-dir=/srv/olivares\"\ncommand_args=\"$command_args_base $olivares_extra_args\"\n", "/srv/olivares", ""},
		{"base copied in every assignment the shipped units make", command + "command_args_base=\"serve --data-dir=/srv/olivares\"\ncommand_args=\"$command_args_base\"\nstart_pre() {\n\tcommand_args=\"$command_args_base\"\n\tolivares_extra_args=\n\tcommand_args=\"$command_args_base $olivares_extra_args\"\n}\n", "/srv/olivares", ""},
		{"base copied then a second --data-dir appended", command + "command_args_base=\"serve --data-dir=/srv/a\"\ncommand_args=\"$command_args_base --data-dir=/srv/b\"\n", "", "ambiguous"},
		{"two bases naming different directories are ambiguous", command + "command_args_base=\"serve --data-dir=/srv/a\"\ncommand_args=\"$command_args_base\"\ncommand_args_base=\"serve --data-dir=/srv/b\"\ncommand_args=\"$command_args_base\"\n", "", "ambiguous"},
		{"a similarly named key is not the base", command + "command_args_base_old=\"serve --data-dir=/srv/stale\"\ncommand_args=\"serve --data-dir=/srv/live\"\n", "/srv/live", ""},
		{"a commented base is inert", command + "#command_args_base=\"serve --data-dir=/srv/stale\"\ncommand_args=\"serve --data-dir=/srv/live\"\n", "/srv/live", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnitDataDir("openrc", tc.body, program)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %q, err %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, err %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestUnitDataDirLaunchdUsesOnlyProgramArguments(t *testing.T) {
	plist := func(program, extra string) string {
		return "<?xml version=\"1.0\"?>\n<plist version=\"1.0\">\n<dict>\n  <key>Label</key>\n  <string>dev.olivares.olivares</string>\n" +
			extra + "  <key>ProgramArguments</key>\n  <array>\n    <string>" + program + "</string>\n  </array>\n" +
			"  <key>StandardOutPath</key>\n  <string>/srv/decoy/launchd-run.sh</string>\n</dict>\n</plist>\n"
	}
	cases := []struct {
		name, body, program, want, wantErr string
	}{
		{"wrapper inside the data directory", plist("/Library/Application Support/Olivares/launchd-run.sh", ""), "/Library/Application Support/Olivares/launchd-run.sh", "/Library/Application Support/Olivares", ""},
		{"custom data directory", plist("/srv/olivares/launchd-run.sh", ""), "/srv/olivares/launchd-run.sh", "/srv/olivares", ""},
		{"comment decoy is ignored", plist("/srv/real/launchd-run.sh", "  <!-- <key>ProgramArguments</key><array><string>/srv/decoy/launchd-run.sh</string></array> -->\n"), "/srv/real/launchd-run.sh", "/srv/real", ""},
		{"program is not the wrapper", plist("/usr/local/bin/olivares", ""), "/srv/real/launchd-run.sh", "", "not the Olivares launchd wrapper"},
		{"another estate's wrapper does not witness this one", plist("/srv/other/launchd-run.sh", ""), "/srv/real/launchd-run.sh", "", "not /srv/real/launchd-run.sh"},
		{"two ProgramArguments keys are ambiguous", plist("/srv/a/launchd-run.sh", "  <key>ProgramArguments</key>\n  <array>\n    <string>/srv/b/launchd-run.sh</string>\n  </array>\n"), "/srv/a/launchd-run.sh", "", "exactly one ProgramArguments"},
		{"no ProgramArguments", "<plist><dict><key>Label</key><string>x</string></dict></plist>", "/srv/a/launchd-run.sh", "", "exactly one ProgramArguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnitDataDir("launchd", tc.body, tc.program)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %q, err %v; want error containing %q", got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, err %v; want %q", got, err, tc.want)
			}
		})
	}
}

// renderTemplate applies the marker substitution scripts/install-service.sh
// performs, so the parser is exercised against the real shipped templates.
func renderTemplate(t *testing.T, name string, values map[string]string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packaging", "service", name))
	if err != nil {
		t.Fatal(err)
	}
	out := string(body)
	for marker, value := range values {
		out = strings.ReplaceAll(out, marker, value)
	}
	if strings.Contains(out, "@DATA_DIR@") || strings.Contains(out, "@BINARY@") {
		t.Fatalf("unresolved marker in %s", name)
	}
	return out
}

func TestUnitDataDirAgainstShippedTemplates(t *testing.T) {
	systemd := renderTemplate(t, "systemd.service", map[string]string{
		"@USER_LINE@": "User=olivares", "@GROUP_LINE@": "Group=olivares", "@PROTECT_HOME@": "true",
		"@WANTED_BY@": "multi-user.target", "@BINARY@": "/usr/local/bin/olivares",
		"@DATA_DIR@": `"/srv/olivares data"`, "@CONFIG@": "/etc/olivares/olivares.env",
	})
	if got, err := UnitDataDir("systemd", systemd, "/usr/local/bin/olivares"); err != nil || got != "/srv/olivares data" {
		t.Fatalf("shipped systemd template: got %q, err %v", got, err)
	}
	openrc := renderTemplate(t, "openrc.sh", map[string]string{
		"@BINARY@": "/usr/local/bin/olivares", "@DATA_DIR@": "/srv/olivares", "@CONFIG@": "/etc/olivares/olivares.env",
	})
	if got, err := UnitDataDir("openrc", openrc, "/usr/local/bin/olivares"); err != nil || got != "/srv/olivares" {
		t.Fatalf("shipped openrc template: got %q, err %v", got, err)
	}
	packaged, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packaging", "openrc", "olivares.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := UnitDataDir("openrc", string(packaged), "/usr/bin/olivares"); err != nil || got != "/var/lib/olivares" {
		t.Fatalf("packaged apk openrc unit: got %q, err %v", got, err)
	}
	launchd := renderTemplate(t, "launchd.xml", map[string]string{
		"@USER_KEY@": "<key>UserName</key><string>_olivares</string>",
		"@PROGRAM@":  "/srv/olivares/launchd-run.sh", "@LOG_PATH@": "/srv/olivares/olivares.log",
	})
	if got, err := UnitDataDir("launchd", launchd, "/srv/olivares/launchd-run.sh"); err != nil || got != "/srv/olivares" {
		t.Fatalf("shipped launchd template: got %q, err %v", got, err)
	}
}
