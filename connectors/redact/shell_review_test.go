// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redact

import "testing"

func TestReviewableShellCommandRefusesOpaqueMasking(t *testing.T) {
	commands := []string{
		"olvs_abcdefgh",
		"printf visible | olvs_abcdefgh",
		"eval olvs_abcdefgh",
		"exec olvs_abcdefgh",
		"password=olvs_abcdefgh; $password",
		">/dev/null olvs_abcdefgh",
		`e""val olvs_abcdefgh`,
		"source olvs_abcdefgh",
		". olvs_abcdefgh",
		"time -p olvs_abcdefgh",
		"coproc olvs_abcdefgh",
		"function olvs_abcdefgh() { printf visible; }",
		"trap 'olvs_abcdefgh' EXIT",
		"alias next=olvs_abcdefgh",
		`bind -x '"x":olvs_abcdefgh'`,
		"enable -f olvs_abcdefgh builtin_name",
		"fc -s olvs_abcdefgh",
		"history -s olvs_abcdefgh",
		"mapfile -C olvs_abcdefgh lines",
		"readarray -C olvs_abcdefgh lines",
		"complete -F olvs_abcdefgh tool",
		"compgen -C olvs_abcdefgh",
		"jobs -x olvs_abcdefgh",
		"hash -p olvs_abcdefgh tool",
		"let x=olvs_abcdefgh",
		"declare -i x=olvs_abcdefgh",
		"typeset -i x=olvs_abcdefgh",
		"local -i x=olvs_abcdefgh",
		"(( x = olvs_abcdefgh ))",
		"echo password=$(printf${IFS}hidden_action)",
		"echo password=`id`",
		"echo password=${VARIABLE}",
		"echo password=$((1+2))",
		"echo password=<(id)",
		"echo password=literal>target",
		"echo password=literal|id",
		"echo password=literal*.txt",
		"echo password=literal{a,b}",
		`echo password=literal\value`,
		"echo password=literal[ab]",
		"echo password=literal[REDACTED]",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			display := Clean(command)
			if display == command {
				t.Fatal("fixture must exercise actual credential masking")
			}
			if _, err := ReviewableShellCommand(command, display); err == nil {
				t.Fatal("masked operation is reviewable")
			}
		})
	}
}

func TestReviewableShellCommandKeepsLiteralCredentialsPrivate(t *testing.T) {
	for _, scenario := range []struct{ command, display string }{
		{"PASSWORD=synthetic-literal printf visible", "PASSWORD=[REDACTED] printf visible"},
		{`PASSWORD="synthetic-literal" printf visible`, `PASSWORD="[REDACTED]" printf visible`},
		{"PASSWORD=synthetic-literal printf visible; printf visible", "PASSWORD=[REDACTED] printf visible; printf visible"},
		{"PASSWORD=synthetic-literal printf visible && printf visible", "PASSWORD=[REDACTED] printf visible && printf visible"},
		{"PASSWORD=synthetic-literal printf visible | cat", "PASSWORD=[REDACTED] printf visible | cat"},
		{"curl --password=synthetic-literal", "curl --password=[REDACTED]"},
		{"curl --token olvs_abcdefgh", "curl --token [REDACTED:olivares-token]"},
		{"curl --secret=synthetic-literal", "curl --secret=[REDACTED]"},
		{"curl --api-key=synthetic-literal", "curl --api-key=[REDACTED]"},
		{"curl --auth=synthetic-literal", "curl --auth=[REDACTED]"},
		{"curl --bearer olvs_abcdefgh", "curl --bearer [REDACTED:olivares-token]"},
		{"curl https://user:synthetic-literal@host/path", "curl https://[REDACTED]@host/path"},
		{`curl -H "Authorization: Bearer olvs_abcdefgh"`, `curl -H "Authorization: Bearer [REDACTED:olivares-token]"`},
		{`curl -H "Proxy-Authorization: Bearer olvs_abcdefgh"`, `curl -H "Proxy-Authorization: Bearer [REDACTED:olivares-token]"`},
		{`curl -H "X-Api-Key: olvs_abcdefgh"`, `curl -H "X-Api-Key: [REDACTED]"`},
		{`curl -H "Cookie: session=olvs_abcdefgh"`, `curl -H "Cookie: session=[REDACTED:olivares-token]"`},

		{"eval visible", "eval visible"},
	} {
		t.Run(scenario.command, func(t *testing.T) {
			shown, err := ReviewableShellCommand(scenario.command, Clean(scenario.command))
			if err != nil || shown != scenario.display {
				t.Fatal("literal secrecy or unchanged input was lost")
			}
		})
	}
}

func TestReviewableShellCommandRefusesInterpreterCode(t *testing.T) {
	for family, command := range map[string]string{
		"Python":                       `python -c "import olvs_abcdefgh"`,
		"PythonVersionPath":            `/usr/bin/python3.12 -c "import olvs_abcdefgh"`,
		"Node":                         `node -e "require('olvs_abcdefgh')"`,
		"Deno":                         "deno run olvs_abcdefgh",
		"Bun":                          "bun run olvs_abcdefgh",
		"Perl":                         `perl -e "require olvs_abcdefgh"`,
		"Ruby":                         `ruby -e "require 'olvs_abcdefgh'"`,
		"PHP":                          `php -r "include 'olvs_abcdefgh';"`,
		"Lua":                          `lua -e "require 'olvs_abcdefgh'"`,
		"AppleScript":                  `osascript -e "run script olvs_abcdefgh"`,
		"PowerShell":                   "pwsh --command olvs_abcdefgh",
		"WindowsPowerShell":            "powershell -File olvs_abcdefgh",
		"Awk":                          `awk "BEGIN {olvs_abcdefgh}"`,
		"Gawk":                         "gawk -f olvs_abcdefgh",
		"Mawk":                         "mawk -f olvs_abcdefgh",
		"Sed":                          "sed -f olvs_abcdefgh",
		"Jq":                           "jq -f olvs_abcdefgh",
		"GenericCommandFlag":           "custom-interpreter -c olvs_abcdefgh",
		"GenericEvalFlag":              "custom-interpreter -eolvs_abcdefgh",
		"GenericLongEvalFlag":          "custom-interpreter --eval=olvs_abcdefgh",
		"GenericUpperEvalFlag":         "custom-interpreter -E olvs_abcdefgh",
		"GenericRunFlag":               "custom-interpreter -r olvs_abcdefgh",
		"GenericLongCommandFlag":       "custom-interpreter --command=olvs_abcdefgh",
		"GenericPartialQuotedFlag":     `custom-interpreter -""c olvs_abcdefgh`,
		"GenericPartialQuotedLongFlag": `custom-interpreter --e''val=olvs_abcdefgh`,
		"GenericEmptyQuotePrefix":      `custom-interpreter ""-c olvs_abcdefgh`,
	} {
		t.Run(family, func(t *testing.T) {
			display := Clean(command)
			if display == command {
				t.Fatal("fixture must mask executable code")
			}
			if _, err := ReviewableShellCommand(command, display); err == nil {
				t.Fatal("masked interpreter code reached human review")
			}
		})
	}
}

func TestReviewableShellCommandRefusesUnprovenMaskPositions(t *testing.T) {
	for _, command := range shellReviewUnprovenCommands() {
		t.Run(command, func(t *testing.T) {
			display := Clean(command)
			if display == command {
				t.Fatal("fixture did not exercise masking")
			}
			if _, err := ReviewableShellCommand(command, display); err == nil {
				t.Fatal("masking outside credential DATA was accepted")
			}
		})
	}
}

func shellReviewUnprovenCommands() []string {
	return []string{
		"xargs olvs_abcdefgh", "nice olvs_abcdefgh", "nohup olvs_abcdefgh", "timeout 1 olvs_abcdefgh", "setsid olvs_abcdefgh",
		"sudo olvs_abcdefgh", "doas olvs_abcdefgh", "stdbuf -oL olvs_abcdefgh", "chroot /tmp olvs_abcdefgh", "watch olvs_abcdefgh", "flock /tmp/lock olvs_abcdefgh",
		"ionice olvs_abcdefgh", "nsenter olvs_abcdefgh", "unshare olvs_abcdefgh", "strace olvs_abcdefgh", "ltrace olvs_abcdefgh",
		"unknown-wrapper olvs_abcdefgh", "printf 'olvs_abcdefgh'", "echo password=synthetic-literal", "PATH=olvs_abcdefgh printf visible",
		"curl --script=olvs_abcdefgh", "curl https://olvs_abcdefgh/path", "curl https://user:synthetic-literal@host/olvs_abcdefgh",
		`curl -H "User-Agent: olvs_abcdefgh"`, `curl -H "olvs_abcdefgh: data"`, "TOKEN=synthetic-literal printf olvs_abcdefgh",
		`P\ASSWORD=olvs_abcdefgh printf visible`,
	}
}

func TestReviewableShellCommandBindsNamedSecretMarkersToCredentialData(t *testing.T) {
	for _, scenario := range []struct {
		command, display string
		allow            bool
	}{
		{"PASSWORD=synthetic-vault-value cmd", "PASSWORD=[secret env/test] cmd", true},
		{`PASSWORD="synthetic-vault-value" cmd`, `PASSWORD="[secret env/test]" cmd`, true},
		{"printf synthetic-vault-value", "printf [secret env/test]", false},
		{"xargs synthetic-vault-value", "xargs [secret env/test]", false},
		{"PASSWORD=synthetic-vault-value cmd [secret env/test]", "PASSWORD=[secret env/test] cmd [secret env/test]", false},
	} {
		t.Run(scenario.command, func(t *testing.T) {
			shown, err := ReviewableShellCommand(scenario.command, scenario.display)
			if scenario.allow && (err != nil || shown != scenario.display) || !scenario.allow && err == nil {
				t.Fatal("named placeholder changed the credential DATA boundary")
			}
		})
	}
}

func TestReviewableShellCommandRequiresProvenConsumerAndOptionState(t *testing.T) {
	for _, command := range []string{
		`nice 'Authorization: olvs_abcdefgh'`, "nice https://user:synthetic-literal@host", "custom-wrapper --token olvs_abcdefgh",
		"curl -- --token olvs_abcdefgh", "curl --password --token olvs_abcdefgh", "curl --TOKEN olvs_abcdefgh", "curl -I --token olvs_abcdefgh",
	} {
		t.Run(command, func(t *testing.T) {
			display := Clean(command)
			if display == command {
				t.Fatal("fixture must mask a value")
			}
			if _, err := ReviewableShellCommand(command, display); err == nil {
				t.Fatal("unproven consumer or option context was accepted")
			}
		})
	}
}

func TestReviewableShellCommandQuotedControlWordsArePrograms(t *testing.T) {
	for _, word := range []string{"!", "if", "then", "else", "elif", "while", "until", "do"} {
		for _, quote := range []string{"'", `"`} {
			command := quote + word + quote + " PASSWORD=olvs_abcdefgh printf visible"
			t.Run(command, func(t *testing.T) {
				if _, err := ReviewableShellCommand(command, Clean(command)); err == nil {
					t.Fatal("quoted control word hid an executable operand")
				}
			})
		}
	}
	command := "if PASSWORD=synthetic-literal printf visible; then printf visible; fi"
	if shown, err := ReviewableShellCommand(command, Clean(command)); err != nil || shown != "if PASSWORD=[REDACTED] printf visible; then printf visible; fi" {
		t.Fatal("unquoted conditional lost its leading credential DATA")
	}
}
