// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// nextCommandAnnotation carries "when this command succeeds, the operator's next
// command is X" into the help output.
//
// A rule adopted from the reference CLIs: every first-hour command's help
// ends with the next command in the path. MEASURED 2026-09-18: `--help` ended
// with examples of the command you had just run and said nothing about what
// follows it, so the order of the first hour lived only in the docs-site guide.
// An operator in the terminal had no way to learn it from the terminal.
//
// It is an ANNOTATION and a template section rather than a line appended to each
// Long string, because a line in prose cannot be enumerated: the gate below walks
// the real cobra tree and asks every first-hour command whether it has one.
const nextCommandAnnotation = "olivares.next-command"

// firstHourNextCommands is the path the walk actually took, in its order. The
// keys are command PATHS without the binary name, so a command that moves under
// a different parent stops matching and the gate says so.
//
// `doctor` points at the agent path because that is what doctor itself reports
// as the next first-hour step when no coding agent is wired.
var firstHourNextCommands = map[string]string{
	"quickstart":           "olivares first-boot",
	"first-boot":           "olivares doctor",
	"doctor":               "olivares agent tool detect",
	"agent tool detect":    "olivares agent tool install --driver <name> --yes",
	"agent tool install":   "olivares agent deploy <driver>",
	"agent deploy":         "olivares agent session create --provider-profile <ref>",
	"provider add":         "olivares provider test <provider-ref>",
	"provider test":        "olivares agent deploy <driver> --provider <provider-ref>",
	"agent session create": "olivares agent session attach <run-ref>",
}

// topLevelNextCommands extends the same rule to EVERY remaining top-level verb.
//
// The first-hour table above covers nine commands on one path. It left the other
// sixty-odd top-level verbs ending their help with examples of themselves, which
// is the defect the first-hour table was written for, still true everywhere the
// first hour does not reach.
//
// The rule for a container verb is the READ verb an operator runs to see what is
// there — `ls`, `status`, `summary`, `export` — because that is the command that
// answers "did anything happen", and a governance family whose help points at its
// write verb teaches an operator to change something before they have looked.
//
// EVERY VALUE HERE IS RUNNABLE AS PRINTED, which is a stronger rule than the one
// this table started with and it is stronger for a measured reason. Resolving is
// not enough: on 2026-09-18 all 66 were run offline and four of them resolved
// and still refused — `claude-agents sessions events` and
// `claude-policy distribution` want one argument, `ddil verify` wants two flags,
// and `work list` wants one of items|decisions|leases. A resolution test cannot
// see that, because the path resolves. So a line that needs an argument prints a
// <placeholder> for it, the way the first-hour table already did, and
// TestEveryNextCommandIsRunnableAsPrinted puts every line through cobra's own
// admission checks — flags parsed, required flags set, positional arguments
// accepted — which is what the binary does before it starts work.
//
// AND COBRA'S ADMISSION IS NOT THE WHOLE RULE, measured on 2026-09-19:
// `finops` pointed at `olivares finops cost`, a CONTAINER whose only
// child is the write verb `ingest`. Cobra admits a container — it resolves, it
// takes no arguments, it has no required flags — and run as printed it prints help
// and exits 0. An operator reads that as success and nothing happened. So the test
// asks two more things of the resolved command, and both are this table's own rule
// written down: it must have a Run or a RunE, and its verb must not be on the
// closed write list in firsthour_next_test.go.
var topLevelNextCommands = map[string]string{
	"accessmap":     "olivares accessmap graph",
	"adoption":      "olivares adoption summary",
	"agent":         "olivares agent session ls",
	"audit":         "olivares audit verify",
	"auth":          "olivares auth status",
	"capabilities":  "olivares capabilities servers ls",
	"catalog":       "olivares catalog entries ls",
	"claude-agents": "olivares claude-agents sessions events <session-id>",
	"claude-policy": "olivares claude-policy distribution <surface>",
	"codex":         "olivares codex managed-config",
	"collector":     "olivares status",
	"compliance":    "olivares compliance holds ls",
	"config":        "olivares config validate",
	"connector":     "olivares doctor",
	"consoleviews":  "olivares consoleviews ls",
	"db":            "olivares db check",
	"ddil":          "olivares ddil verify --bundle <bundle> --pubkey <pubkey>",
	"deploy":        "olivares deploy definitions ls",
	"dr":            "olivares dr ls",
	"evals":         "olivares evals gate",
	"eventing":      "olivares eventing subscriptions ls",
	"findings":      "olivares findings export",
	// `finops cost` is the INGEST group, and `cost` alone is a container that
	// prints help and exits 0, measured on 2026-09-19 — which is what "runnable as
	// printed" has to mean for an operator and not only
	// for the parser. `spend summary` is the read verb this group's own help leads
	// with, and it is the aggregate an operator opens finops for.
	"finops":          "olivares finops spend summary",
	"governance":      "olivares governance killswitch ls",
	"grok":            "olivares grok managed-config",
	"health":          "olivares health checks ls",
	"hookpep":         "olivares hookpep explain",
	"identity":        "olivares identity sso",
	"inference-proxy": "olivares inference-proxy config get",
	"inventory":       "olivares inventory summary",
	"keys":            "olivares keys status",
	"knowledge":       "olivares knowledge documents get <document-id>",
	"license":         "olivares license status",
	"mcp":             "olivares mcp pins ls",
	"members":         "olivares members ls",
	"migrate":         "olivares migrate status",
	"models":          "olivares models ls",
	"notify":          "olivares notify destinations",
	"observability":   "olivares observability ingestion-health",
	"orchestration":   "olivares orchestration graph",
	"posture":         "olivares posture export",
	"provider":        "olivares provider ls",
	"readyz":          "olivares doctor",
	"recording":       "olivares recording sessions ls",
	"redteam":         "olivares redteam catalog",
	"reporting":       "olivares reporting reports ls",
	"sandbox":         "olivares sandbox runs ls",
	"secrets":         "olivares secrets ls",
	"security":        "olivares security check",
	"serve":           "olivares status",
	"setup":           "olivares doctor",
	"sources":         "olivares sources ls",
	"sourcescope":     "olivares sourcescope assignments ls",
	"status":          "olivares doctor",
	"superadmin":      "olivares superadmin status",
	"support":         "olivares support bundle",
	"tenants":         "olivares tenants ls",
	"tokens":          "olivares tokens ls",
	"upgrade":         "olivares version",
	"users":           "olivares users ls",
	"version":         "olivares status",
	"voice":           "olivares voice sessions ls",
	"work":            "olivares work list items",
}

// nextCommandExempt is the closed list of top-level verbs that name NO next
// command, each with the reason. It is a map and not a silence because "this verb
// has nothing after it" and "nobody got to this verb" look identical in an empty
// table, and only one of them is a decision.
var nextCommandExempt = map[string]string{
	"help":       "cobra's own verb: what follows is the command you asked about, which it just printed",
	"completion": "its output is evaluated by the shell, not run as a command",
	"uninstall":  "on success this binary is gone; a next command would name something that is no longer installed",
	"openapi":    "its output is a document for a code generator, and the next step happens in another tool",
	"first-boot": "already in the first-hour table above",
	"quickstart": "already in the first-hour table above",
	"doctor":     "already in the first-hour table above",
	// The hook clients are invoked BY the coding agent, never by a person at a
	// prompt. There is no operator to give a next command to.
	"claude-hook": "invoked by Claude Code as a hook, not by a person",
	"codex-hook":  "invoked by Codex as a hook, not by a person",
	"grok-hook":   "invoked by Grok Build as a hook, not by a person",
}

// applyFirstHourNextCommands stamps the annotation onto the commands named above.
// It returns the paths it did NOT find, so a rename is a test failure rather than
// a help section that silently stops appearing.
func applyFirstHourNextCommands(root *cobra.Command) []string {
	found := map[string]bool{}
	walkCommands(root, func(cmd *cobra.Command) {
		path := commandPathWithoutBinary(cmd)
		next, ok := firstHourNextCommands[path]
		if !ok {
			// The first-hour path wins where the two tables name the same verb:
			// it is the ordered walk, and the top-level table is the fallback.
			next, ok = topLevelNextCommands[path]
		}
		if !ok {
			return
		}
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[nextCommandAnnotation] = next
		found[path] = true
	})
	var missing []string
	for path := range firstHourNextCommands {
		if !found[path] {
			missing = append(missing, path)
		}
	}
	for path := range topLevelNextCommands {
		if !found[path] {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	return missing
}

// commandPathWithoutBinary is "agent tool detect" for `olivares agent tool
// detect`. CommandPath includes the binary name, which changes with the file on
// disk and must not decide whether a help section appears.
func commandPathWithoutBinary(cmd *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()))
}

// nextCommandFor is the template function. It returns "" for every command that
// has no next step, and the template prints nothing for an empty string.
func nextCommandFor(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	return strings.TrimSpace(cmd.Annotations[nextCommandAnnotation])
}

// helpTemplateWithNext is cobra's default help template with one section added.
//
// The section goes AFTER the usage block, which is where an operator stops
// reading, and it is phrased as the command to run rather than as a description
// of it: the whole point is that it can be copied.
//
// The heading is "Next:" and not "Next in the first hour:" because the table is
// no longer the first hour's — it covers every top-level verb, and a heading that
// says "first hour" over `olivares finops cost` is a heading that lies about the
// one thing it exists to say.
const helpTemplateWithNext = `{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}{{if or .Runnable .HasSubCommands}}{{.UsageString}}{{end}}{{with olivaresNextCommand .}}
Next:
  {{.}}
{{end}}`

// installFirstHourHelp wires the template function and the template. Cobra
// resolves HelpTemplate() up the parent chain, so setting it on the root gives it
// to every subcommand.
func installFirstHourHelp(root *cobra.Command) []string {
	cobra.AddTemplateFunc("olivaresNextCommand", nextCommandFor)
	root.SetHelpTemplate(helpTemplateWithNext)
	return applyFirstHourNextCommands(root)
}
