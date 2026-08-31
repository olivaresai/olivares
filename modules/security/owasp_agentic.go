// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// classAgentic is the OWASP-Agentic guardrail class.
const classAgentic = "owasp_agentic"

// This detector applies the OWASP "Top 10 for Agentic Applications (for 2026)"
// lens (ASI01–ASI10) — the AGENTIC threats that the text/injection/PII detectors
// don't cover: goal hijack, dangerous tool args, privilege abuse, code exec,
// memory poisoning, inter-agent relay, and rogue/covert behavior. Source taxonomy
// (verified, /tmp/s27-taxonomies.md):
//
//	OWASP GenAI Security Project — "OWASP Top 10 for Agentic Applications
//	(for 2026)" (2025-12-09).
//	https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/
//	  ASI01 Agent Goal Hijack
//	  ASI02 Tool Misuse
//	  ASI03 Identity & Privilege Abuse
//	  ASI04 Agentic Supply Chain Vulnerabilities
//	  ASI05 Unexpected Code Execution
//	  ASI06 Memory & Context Poisoning   (cf. T1 Memory Poisoning, Threats & Mitigations v1.0)
//	  ASI07 Insecure Inter-Agent Communication
//	  ASI08 Cascading Failures
//	  ASI09 Human-Agent Trust Exploitation
//	  ASI10 Rogue Agents
//
// All ten ASI ids are now covered (closed the ASI04/ASI08/ASI09 gap). The
// MCP-side of agentic supply chain (ASI04) is additionally mapped, finding-kind →
// OWASP MCP Top 10 id, in owasp_mcp.go; this detector adds the TEXT signals (an
// instruction to install/register an untrusted tool, an unbounded retry/fan-out that
// cascades, an instruction to auto-approve or socially-engineer the human reviewer).
//
// These signals are mostly relevant on tool_args (a tool call carrying a
// destructive command / privilege grab) and output (an agent emitting covert or
// relayed instructions); they may appear on input too. The rules over-flag rather
// than under-flag a real attack (a guardrail may be noisy; it must not miss).
// Excerpts are produced by scan() — auto-clamped and scrubExcerpt'd — so a command
// or phrase captured in the window can never surface a secret/PII value (docs/SECURITY-HARDENING.md).

// ---- ASI01 Agent Goal Hijack ------------------------------------------------------
// Attacker redirects the agent's objective ("your real goal is …", "your new mission").
var (
	agenticGoalRealRe   = regexp.MustCompile(`(?i)your\s+(?:real|true|actual)\s+(?:goal|objective|task|purpose)\s+is`)
	agenticGoalChangeRe = regexp.MustCompile(`(?i)change\s+your\s+(?:objective|goal|mission|task)\s+to`)
	agenticGoalNewRe    = regexp.MustCompile(`(?i)your\s+new\s+(?:mission|goal|objective|task|directive)`)
)

// ---- ASI02 Tool Misuse ------------------------------------------------------------
// Destructive tool/command arguments. redact:false — these are commands, not
// secrets — but scan() still scrubs the excerpt in case a value rode along.
var (
	agenticRmRfRe     = regexp.MustCompile(`(?i)\brm\s+-[a-z]*r[a-z]*f\b|\brm\s+-[a-z]*f[a-z]*r\b`)
	agenticDropTblRe  = regexp.MustCompile(`(?i)\bDROP\s+TABLE\b`)
	agenticDelWhereRe = regexp.MustCompile(`(?i)\bDELETE\s+FROM\b.*\bWHERE\s+1\s*=\s*1\b`)
	agenticCurlPipeRe = regexp.MustCompile(`(?i)\bcurl\b.*\|\s*(?:ba)?sh\b`)
	agenticWgetPipeRe = regexp.MustCompile(`(?i)\bwget\b.*\|\s*(?:ba)?sh\b`)
	agenticForkBombRe = regexp.MustCompile(`:\(\)\s*\{`)
	agenticMkfsRe     = regexp.MustCompile(`(?i)\bmkfs(?:\.\w+)?\b`)
	agenticDdRe       = regexp.MustCompile(`(?i)\bdd\s+if=`)
)

// ---- ASI03 Identity & Privilege Abuse ---------------------------------------------
// Privilege escalation / over-broad permission grabs.
var (
	agenticSudoRe        = regexp.MustCompile(`(?i)\bsudo\s+`)
	agenticChmod777Re    = regexp.MustCompile(`(?i)\bchmod\s+(?:-[a-zA-Z]+\s+)?0?777\b`)
	agenticAssumeAdminRe = regexp.MustCompile(`(?i)assume\s+role\b.*admin`)
	agenticSetuidRe      = regexp.MustCompile(`(?i)\bsetuid\b`)
	agenticGrantAllRe    = regexp.MustCompile(`(?i)grant\s+all\s+privileges`)
	agenticEscalateRe    = regexp.MustCompile(`(?i)escalate\s+(?:your\s+)?privileges`)
)

// ---- ASI04 Agentic Supply Chain Vulnerabilities -----------------------------------
// Instruction-shaped signals that the agent pull in / trust an UNVERIFIED tool,
// plugin, MCP server, connector or model at run time — the agentic supply-chain
// vector. (The MCP-connector side is mapped separately in owasp_mcp.go.) ATLAS leaves
// these untagged: the 2026 ATLAS agent-supply-chain techniques (AML.T0104 Publish
// Poisoned AI Agent Tool, AML.T0109 AI Supply Chain Rug Pull, AML.T0110 AI Agent Tool
// Poisoning) describe the ADVERSARY's publish/poison act, not an in-band instruction
// to install — so tagging them here would over-claim; they are exercised honestly in
// the red-team tool-poisoning battery instead.
var (
	agenticInstallToolRe  = regexp.MustCompile(`(?i)\b(?:install|add|enable)\s+(?:this\s+|the\s+|an?\s+)?(?:mcp\s+server|plugin|extension|agent\s+tool|connector|tool\s+server)\b`)
	agenticLoadToolURLRe  = regexp.MustCompile(`(?i)\b(?:load|fetch|pull|download)\s+(?:a\s+|the\s+|this\s+)?(?:tool|plugin|agent|model|skill)\s+from\s+https?://`)
	agenticRegisterToolRe = regexp.MustCompile(`(?i)\bregister\s+(?:this\s+|the\s+|a\s+)?(?:tool|mcp\s+server|agent|plugin|connector)\b`)
)

// ---- ASI08 Cascading Failures -----------------------------------------------------
// Instructions that remove a bound on retry/recursion/fan-out, the orchestration
// amplifier that turns one fault into a cascade across the agent estate. (The
// observed-runtime side — a real cascade/loop — surfaces from the orchestration
// module's findings; this detector catches the instruction that induces it.)
var (
	agenticInfiniteRetryRe  = regexp.MustCompile(`(?i)\b(?:retry|repeat|loop)\s+(?:this\s+)?(?:indefinitely|forever|endlessly|without\s+(?:limit|stopping)|until\s+it\s+succeeds)\b`)
	agenticRecursiveSpawnRe = regexp.MustCompile(`(?i)\b(?:recursively|repeatedly)\s+(?:spawn|launch|create|invoke)\s+(?:new\s+)?(?:sub-?)?agents?\b`)
	agenticFanoutRe         = regexp.MustCompile(`(?i)\bfor\s+each\s+\w+,?\s+(?:spawn|launch|create)\s+(?:a\s+)?(?:new\s+)?(?:sub-?)?agent\b`)
	agenticNoLimitRe        = regexp.MustCompile(`(?i)\b(?:no|remove\s+(?:the|any))\s+(?:limit|cap|bound)\s+on\s+(?:retries|recursion|agent\s+spawn|fan[- ]?out)`)
)

// ---- ASI09 Human-Agent Trust Exploitation -----------------------------------------
// Signals that the agent be told to bypass / fatigue / socially-engineer the human
// reviewer — auto-approving, suppressing the confirmation prompt, falsely asserting
// the human already approved, or telling the human something is safe to extract a
// click. This is the HITL-trust attack, distinct from the rogue-agent self-concealment
// of ASI10.
var (
	agenticAutoApproveRe   = regexp.MustCompile(`(?i)\b(?:approve|accept|confirm)\s+(?:all|everything|any(?:thing)?)\s+(?:without|automatically|by\s+default)\b`)
	agenticStopAskingRe    = regexp.MustCompile(`(?i)\bstop\s+asking\s+(?:me\s+)?(?:for\s+)?(?:confirmation|approval|permission)\b`)
	agenticSkipApprovalRe  = regexp.MustCompile(`(?i)\b(?:skip|bypass|disable|turn\s+off)\s+(?:the\s+)?(?:human\s+)?(?:confirmation|approval|review|oversight|hitl)\b`)
	agenticFalseApprovalRe = regexp.MustCompile(`(?i)\bthe\s+(?:user|human|operator)\s+(?:has\s+)?(?:already\s+)?(?:approved|authorized|consented\s+to)\b`)
	agenticTrustMeRe       = regexp.MustCompile(`(?i)\btrust\s+me,?\s+(?:just\s+)?(?:approve|proceed|continue|do\s+it)\b`)
	agenticAssureSafeRe    = regexp.MustCompile(`(?i)\btell\s+the\s+(?:user|human|operator)\s+(?:it'?s|this\s+is)\s+safe\b`)
)

// ---- ASI05 Unexpected Code Execution ----------------------------------------------
// Dynamic code-exec sinks (eval/exec/os.system/subprocess/__import__/Function/child_process).
var (
	agenticEvalRe         = regexp.MustCompile(`(?i)\beval\s*\(`)
	agenticExecRe         = regexp.MustCompile(`(?i)\bexec\s*\(`)
	agenticOsSystemRe     = regexp.MustCompile(`(?i)\bos\.system\s*\(`)
	agenticSubprocessRe   = regexp.MustCompile(`(?i)\bsubprocess\.`)
	agenticDunderImportRe = regexp.MustCompile(`(?i)__import__\s*\(`)
	agenticFunctionCtorRe = regexp.MustCompile(`(?i)\bFunction\s*\(\s*["']`)
	agenticChildProcRe    = regexp.MustCompile(`(?i)\bchild_process\b`)
)

// ---- ASI06 Memory & Context Poisoning ---------------------------------------------
// Attempts to persist a planted instruction into the agent's long-term memory/context
// (cf. T1 Memory Poisoning, Threats & Mitigations v1.0).
var (
	agenticRememberForeverRe = regexp.MustCompile(`(?i)remember\s+(?:that\s+)?for\s+all\s+future`)
	agenticStorePermRe       = regexp.MustCompile(`(?i)store\s+this\s+(?:instruction\s+)?permanently`)
	agenticUpdateMemRe       = regexp.MustCompile(`(?i)update\s+your\s+memory\s+to`)
	agenticFromNowOnRe       = regexp.MustCompile(`(?i)from\s+now\s+on\s+always\s+treat`)
)

// ---- ASI07 Insecure Inter-Agent Communication -------------------------------------
// Relaying a planted instruction to another/downstream agent.
var (
	agenticTellOtherRe  = regexp.MustCompile(`(?i)tell\s+the\s+other\s+agent\s+to`)
	agenticForwardRe    = regexp.MustCompile(`(?i)forward\s+this\s+(?:instruction|command)\s+to\s+agent`)
	agenticDownstreamRe = regexp.MustCompile(`(?i)instruct\s+(?:the\s+)?downstream\s+agent`)
)

// ---- ASI10 Rogue Agents -----------------------------------------------------------
// Covert / unsupervised behavior: act without oversight, suppress logging, hide from
// the human operator.
var (
	agenticActAutonomousRe = regexp.MustCompile(`(?i)act\s+autonomously\s+without\s+(?:approval|oversight|review)`)
	agenticNoReportRe      = regexp.MustCompile(`(?i)do\s+not\s+(?:report|log|record)\s+this\s+(?:action|step|command)`)
	agenticHideFromRe      = regexp.MustCompile(`(?i)hide\s+this\s+from\s+the\s+(?:operator|user|human)`)
)

// agenticShapes is the COMPLETE ASI01–ASI10 rule catalog (all ten ids added
// ASI04/ASI08/ASI09). Every rule carries its exact OWASP ASI id verified against the
// OWASP Top 10 for Agentic Applications (for 2026) (no invented ids). No ATLAS id is
// set on these shapes: an agentic-behavior text signal (a goal-hijack phrase, an
// install instruction, an auto-approve directive) is not the same as a MITRE ATLAS
// technique — the verified ATLAS ids (LLM injection/jailbreak/leakage AML.T0051/54/56/
// 57, and the 2026 agent techniques AML.T0104/0105/0108/0109/0110) describe the
// adversary's act and are tagged where they are genuinely exercised: the prompt-
// injection/output detectors (LLM axis) and the red-team battery. Leaving
// ATLAS empty here is the honest choice over a mis-cite.
var agenticShapes = []shape{
	// ASI01 Agent Goal Hijack — HIGH.
	{rule: "asi01-goal-real", re: agenticGoalRealRe, sev: sdkmodel.SeverityHigh, title: "agent goal hijack: redefining the real goal", owasp: "ASI01"},
	{rule: "asi01-goal-change", re: agenticGoalChangeRe, sev: sdkmodel.SeverityHigh, title: "agent goal hijack: change objective directive", owasp: "ASI01"},
	{rule: "asi01-goal-new-mission", re: agenticGoalNewRe, sev: sdkmodel.SeverityHigh, title: "agent goal hijack: new mission injected", owasp: "ASI01"},

	// ASI02 Tool Misuse — HIGH. Commands, not secrets (redact:false); scan() scrubs.
	{rule: "asi02-rm-rf", re: agenticRmRfRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: recursive force delete (rm -rf)", owasp: "ASI02"},
	{rule: "asi02-drop-table", re: agenticDropTblRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: DROP TABLE", owasp: "ASI02"},
	{rule: "asi02-delete-where-1-1", re: agenticDelWhereRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: unbounded DELETE (WHERE 1=1)", owasp: "ASI02"},
	{rule: "asi02-curl-pipe-sh", re: agenticCurlPipeRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: curl piped to shell", owasp: "ASI02"},
	{rule: "asi02-wget-pipe-sh", re: agenticWgetPipeRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: wget piped to shell", owasp: "ASI02"},
	{rule: "asi02-fork-bomb", re: agenticForkBombRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: shell fork bomb", owasp: "ASI02"},
	{rule: "asi02-mkfs", re: agenticMkfsRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: filesystem format (mkfs)", owasp: "ASI02"},
	{rule: "asi02-dd-if", re: agenticDdRe, sev: sdkmodel.SeverityHigh, title: "tool misuse: raw disk write (dd if=)", owasp: "ASI02"},

	// ASI03 Identity & Privilege Abuse — HIGH.
	{rule: "asi03-sudo", re: agenticSudoRe, sev: sdkmodel.SeverityHigh, title: "privilege abuse: sudo invocation", owasp: "ASI03"},
	{rule: "asi03-chmod-777", re: agenticChmod777Re, sev: sdkmodel.SeverityHigh, title: "privilege abuse: world-writable chmod 777", owasp: "ASI03"},
	{rule: "asi03-assume-admin-role", re: agenticAssumeAdminRe, sev: sdkmodel.SeverityHigh, title: "privilege abuse: assume admin role", owasp: "ASI03"},
	{rule: "asi03-setuid", re: agenticSetuidRe, sev: sdkmodel.SeverityHigh, title: "privilege abuse: setuid", owasp: "ASI03"},
	{rule: "asi03-grant-all", re: agenticGrantAllRe, sev: sdkmodel.SeverityHigh, title: "privilege abuse: grant all privileges", owasp: "ASI03"},
	{rule: "asi03-escalate", re: agenticEscalateRe, sev: sdkmodel.SeverityHigh, title: "privilege abuse: escalate privileges", owasp: "ASI03"},

	// ASI04 Agentic Supply Chain Vulnerabilities — HIGH. Pulling in / trusting an
	// unverified tool, plugin, MCP server or model at run time.
	{rule: "asi04-install-tool", re: agenticInstallToolRe, sev: sdkmodel.SeverityHigh, title: "agentic supply chain: install unverified tool/plugin/MCP server", owasp: "ASI04"},
	{rule: "asi04-load-tool-url", re: agenticLoadToolURLRe, sev: sdkmodel.SeverityHigh, title: "agentic supply chain: load a tool/model from a URL", owasp: "ASI04"},
	{rule: "asi04-register-tool", re: agenticRegisterToolRe, sev: sdkmodel.SeverityMedium, title: "agentic supply chain: register an unverified tool/agent", owasp: "ASI04"},

	// ASI05 Unexpected Code Execution — HIGH.
	{rule: "asi05-eval", re: agenticEvalRe, sev: sdkmodel.SeverityHigh, title: "unexpected code execution: eval()", owasp: "ASI05"},
	{rule: "asi05-exec", re: agenticExecRe, sev: sdkmodel.SeverityHigh, title: "unexpected code execution: exec()", owasp: "ASI05"},
	{rule: "asi05-os-system", re: agenticOsSystemRe, sev: sdkmodel.SeverityHigh, title: "unexpected code execution: os.system()", owasp: "ASI05"},
	{rule: "asi05-subprocess", re: agenticSubprocessRe, sev: sdkmodel.SeverityHigh, title: "unexpected code execution: subprocess", owasp: "ASI05"},
	{rule: "asi05-dunder-import", re: agenticDunderImportRe, sev: sdkmodel.SeverityHigh, title: "unexpected code execution: __import__()", owasp: "ASI05"},
	{rule: "asi05-function-ctor", re: agenticFunctionCtorRe, sev: sdkmodel.SeverityHigh, title: "unexpected code execution: Function() constructor", owasp: "ASI05"},
	{rule: "asi05-child-process", re: agenticChildProcRe, sev: sdkmodel.SeverityHigh, title: "unexpected code execution: child_process", owasp: "ASI05"},

	// ASI06 Memory & Context Poisoning — MEDIUM.
	{rule: "asi06-remember-forever", re: agenticRememberForeverRe, sev: sdkmodel.SeverityMedium, title: "memory poisoning: persist for all future turns", owasp: "ASI06"},
	{rule: "asi06-store-permanent", re: agenticStorePermRe, sev: sdkmodel.SeverityMedium, title: "memory poisoning: store instruction permanently", owasp: "ASI06"},
	{rule: "asi06-update-memory", re: agenticUpdateMemRe, sev: sdkmodel.SeverityMedium, title: "memory poisoning: overwrite agent memory", owasp: "ASI06"},
	{rule: "asi06-from-now-on", re: agenticFromNowOnRe, sev: sdkmodel.SeverityMedium, title: "memory poisoning: from-now-on standing rule", owasp: "ASI06"},

	// ASI07 Insecure Inter-Agent Communication — MEDIUM.
	{rule: "asi07-tell-other-agent", re: agenticTellOtherRe, sev: sdkmodel.SeverityMedium, title: "inter-agent relay: instruct another agent", owasp: "ASI07"},
	{rule: "asi07-forward-to-agent", re: agenticForwardRe, sev: sdkmodel.SeverityMedium, title: "inter-agent relay: forward instruction to agent", owasp: "ASI07"},
	{rule: "asi07-downstream-agent", re: agenticDownstreamRe, sev: sdkmodel.SeverityMedium, title: "inter-agent relay: instruct downstream agent", owasp: "ASI07"},

	// ASI08 Cascading Failures — HIGH. Removing a bound on retry/recursion/fan-out.
	{rule: "asi08-infinite-retry", re: agenticInfiniteRetryRe, sev: sdkmodel.SeverityHigh, title: "cascading failure: unbounded retry/loop", owasp: "ASI08"},
	{rule: "asi08-recursive-spawn", re: agenticRecursiveSpawnRe, sev: sdkmodel.SeverityHigh, title: "cascading failure: recursive agent spawn", owasp: "ASI08"},
	{rule: "asi08-fanout-per-item", re: agenticFanoutRe, sev: sdkmodel.SeverityMedium, title: "cascading failure: per-item agent fan-out", owasp: "ASI08"},
	{rule: "asi08-no-limit", re: agenticNoLimitRe, sev: sdkmodel.SeverityHigh, title: "cascading failure: remove retry/recursion bound", owasp: "ASI08"},

	// ASI09 Human-Agent Trust Exploitation — HIGH. Bypassing/fatiguing/deceiving the
	// human reviewer (auto-approve, suppress the prompt, falsely claim approval).
	{rule: "asi09-auto-approve", re: agenticAutoApproveRe, sev: sdkmodel.SeverityHigh, title: "trust exploitation: auto-approve everything", owasp: "ASI09"},
	{rule: "asi09-stop-asking", re: agenticStopAskingRe, sev: sdkmodel.SeverityHigh, title: "trust exploitation: suppress the approval prompt", owasp: "ASI09"},
	{rule: "asi09-skip-approval", re: agenticSkipApprovalRe, sev: sdkmodel.SeverityHigh, title: "trust exploitation: skip/disable human review", owasp: "ASI09"},
	{rule: "asi09-false-approval", re: agenticFalseApprovalRe, sev: sdkmodel.SeverityMedium, title: "trust exploitation: false claim of prior approval", owasp: "ASI09"},
	{rule: "asi09-trust-me", re: agenticTrustMeRe, sev: sdkmodel.SeverityMedium, title: "trust exploitation: \"trust me, just approve\"", owasp: "ASI09"},
	{rule: "asi09-assure-safe", re: agenticAssureSafeRe, sev: sdkmodel.SeverityMedium, title: "trust exploitation: coach the agent to assure the human it is safe", owasp: "ASI09"},

	// ASI10 Rogue Agents — HIGH.
	{rule: "asi10-act-autonomous", re: agenticActAutonomousRe, sev: sdkmodel.SeverityHigh, title: "rogue agent: act without approval/oversight", owasp: "ASI10"},
	{rule: "asi10-no-report", re: agenticNoReportRe, sev: sdkmodel.SeverityHigh, title: "rogue agent: suppress reporting/logging", owasp: "ASI10"},
	{rule: "asi10-hide-from-human", re: agenticHideFromRe, sev: sdkmodel.SeverityHigh, title: "rogue agent: hide action from operator", owasp: "ASI10"},
}

// owaspAgenticDetector is the OWASP-Agentic guardrail. It runs the ASI01–ASI10 rule
// catalog over the input on every surface; the signals are weighted toward tool_args
// (a destructive/privileged command in a tool call) and output (an agent emitting
// covert or relayed instructions), but they fire on input too — the catalog is
// surface-independent so a planted instruction is caught wherever it appears.
type owaspAgenticDetector struct{}

func newOWASPAgenticDetector() Detector { return owaspAgenticDetector{} }

func (owaspAgenticDetector) Class() string { return classAgentic }

func (owaspAgenticDetector) Inspect(in GuardrailInput) []Detection {
	return scan(classAgentic, in.Text, agenticShapes)
}
