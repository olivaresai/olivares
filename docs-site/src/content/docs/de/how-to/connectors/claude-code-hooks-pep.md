---
title: "Claude Code Hooks & Enforcement (der PEP)"
description: >-
  Die Governance-Hälfte des Claude-Code-Connectors: Hooks, die standardmäßig
  beobachtet werden, und ein optionaler Policy Enforcement Point, der
  PreToolUse-/PermissionRequest-Hooks mit deny oder ask beantwortet — jedes
  Gate als Finding erfasst.
sidebar:
  order: 5
---

[Claude Code anbinden](/de/how-to/connect-claude-code/) verdrahtet die
*Beobachtungs*-Hälfte — OTLP-Telemetrie hinein, Access-Edges heraus. Diese
Seite ist die **Governance-Hälfte**: Die **Hooks** von Claude Code melden
Tool-Entscheidungen an den Connector, und ein optionaler **Policy Enforcement
Point (PEP)** macht aus diesem Kanal ein Gate — der Connector beantwortet
einen passenden `PreToolUse`- / `PermissionRequest`-Hook mit einer
`permissionDecision` von `deny` oder `ask` und erfasst jedes Gate als Finding.

Die Voreinstellung ist bewusst **read-first**: Ohne konfigurierte
Enforcement-Policy werden Hooks *beobachtet, niemals gegated*. Enforcement ist
ein benanntes, explizites Opt-in, und eine ungültige Policy **schlägt beim
Start fehl** — der Connector läuft nicht stillschweigend ungoverned weiter.

## So funktioniert der Hook-Kanal

Der OTLP/HTTP-Empfänger des Connectors (Loopback `127.0.0.1:4318`
standardmäßig) bedient unter `hook_path` (Default **`/hooks`**) auch den
Hook-Endpunkt. Auf der Entwicklermaschine postet die Hook-Konfiguration von
Claude Code ihre Hook-Events an diesen Loopback-Endpunkt — die exakte Syntax
der Hook-Einstellungen gehört in die Dokumentation von Claude Code selbst; was
dieses Produkt besitzt, sind der Empfänger und die folgende Policy.

Hook-Events und OTLP-Telemetrie über denselben Tool-Aufruf werden
**korreliert** (das `correlation_window`, Default 5s, hält eine Seite wartend
auf die andere), sodass eine gegatete Aktion und ihre Telemetrie als eine
kohärente Geschichte landen, nicht als zwei getrennte Datensätze. Eine
Session, die weiterhin hookt, aber über `silence_threshold` (Default 2m)
hinaus OTLP-stumm bleibt, wird als Telemetrie-Lücke markiert — das
Anti-Evasion-Signal.

## Enforcement aktivieren

Füge eine `enforcement`-Policy zur Config der Quelle hinzu
(`OLIVARES_SOURCES_CONFIG`):

```json
{
  "sources": [{
    "name": "claude",
    "kind": "claude",
    "tenant": "<tenant-id>",
    "config": {
      "enforcement": "{\"rules\":[{\"tool\":\"Bash\",\"decision\":\"ask\",\"reason\":\"shell needs a human\"},{\"resource_kind\":\"file\",\"mode\":\"write\",\"decision\":\"deny\"}]}"
    }
  }]
}
```

Regeln matchen auf den Tool-Namen und/oder die Ressourcenart und den
Zugriffsmodus; die Entscheidung ist `deny` oder `ask` (Eskalation an den
Menschen in der Session). Passende `PreToolUse`- / `PermissionRequest`-Hooks
erhalten diese Entscheidung als `permissionDecision` von Claude Code zurück;
alles andere wird beobachtet durchgereicht. Jedes Gate wird als **Finding**
erfasst, sodass der Enforcement-Verlauf abfragbar ist, nicht Folklore.

:::note[Der Kill-Switch übertrumpft alles]
Steht das Estate (oder der spezifische Agent) unter einem
[Notfall-Stopp](/de/how-to/cookbook/kill-switch-drill/), wird `claude.tool.use`
unabhängig von dieser Policy auf der Governance-Schicht abgewürgt — das
Stop-Gate wird vor jeder Per-Tool-Regel geprüft und schlägt fail-closed fehl.
:::

## Flotten-Posture: Managed Settings, beobachtet

Enforcement am Hook ist eine Schicht. Die flottenweite Schicht ist die Datei
mit den **Managed Settings** von Claude Code, die die Quelle
`managed-settings` schreibgeschützt beobachtet:

```json
{
  "sources": [{
    "name": "fleet-policy",
    "kind": "managed-settings",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/managed-settings.json",
      "expected_policy": "{…governance-authored intent…}"
    }
  }]
}
```

| Schlüssel | Default | Bedeutung |
|---|---|---|
| `config_path` | `/etc/claude-code/managed-settings.json` (Linux) | die aktive Managed-Settings-Datei des Hosts (macOS: `/Library/Application Support/ClaudeCode/…`) |
| `scope` | OS-Hostname | Attributions-Scope (Host-ID / Distributionsname) |
| `expected_policy` | — | optionale verfasste Absicht; ist sie gesetzt, meldet der Connector **Drift** (permitted-Policy vs. observed-Config). Leer = nur beobachten |

Verwandte optionale Beobachter auf der `claude`-Quelle: `managed_mcp_path`
(modelliert die Eval-Reihenfolge der Managed-MCP-Allowlist und markiert
Name-only-Allow-Einträge) und `sandbox_path` (Posture-Findings zu den
Sandbox-Lockdown-Einstellungen) — beide schreibgeschützt, beide aus, bis sie
auf eine Datei gerichtet werden.

## Was du in der Konsole siehst

**Claude Code Governance** ist die Authoring- und Truth-Loop-Oberfläche: die
Policy, die du beabsichtigst, die Konfiguration, die Hosts tatsächlich tragen,
und der Drift dazwischen. Gates und Telemetrie-Lücken-Findings landen in
**Security**; die Session selbst bleibt in **Sessions** sichtbar:

<img class="light:sl-hidden" src="/console/claude-policy-dark.png" alt="Die Claude-Code-Governance-Ansicht — Policy-Authoring und Flotten-Posture an einem Ort." />
<img class="dark:sl-hidden" src="/console/claude-policy-light.png" alt="Die Claude-Code-Governance-Ansicht — Policy-Authoring und Flotten-Posture an einem Ort." />

## Ehrliche Grenzen

- **Der PEP gated, was Hooks melden.** Ein Host, dessen Hooks nicht
  konfiguriert sind, wird nicht gegated — paare die Flotte mit dem
  [Managed-Settings-Beobachter](#flotten-posture-managed-settings-beobachtet),
  damit Abwesenheit sichtbar ist, und mit dem
  [Kernel-Backstop](/de/how-to/connectors/ebpf-tetragon/), damit sie nicht blind
  ist.
- **`ask` verweist auf einen Menschen in der Session** — es ist Reibung, kein
  Schloss. `deny` ist das Schloss.
- **Subprozesse sind hier außerhalb des Scopes** (Hooks feuern für die eigenen
  Tool-Aufrufe von Claude Code); siehe die
  [Enterprise-OTel-Seite](/de/how-to/claude-code-enterprise-otel/) dazu, was die
  Telemetrie-Umgebung erreicht und was nicht.

## Verwandt

- [Claude Code anbinden](/de/how-to/connect-claude-code/) — die
  Beobachtungs-Hälfte.
- [Enterprise OTel für Claude Code](/de/how-to/claude-code-enterprise-otel/) —
  Flotten-Telemetrie, Labels, Tracing.
- [Governen und freigeben](/de/how-to/govern-and-approve/) — das
  Autorisierungsmodell, in das sich der PEP einklinkt.
