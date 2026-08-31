---
title: "Enterprise-OpenTelemetry für Claude Code konfigurieren"
description: >-
  Die empfohlene Enterprise-Telemetrie-Haltung für eine Claude-Code-Flotte:
  Managed-Settings-env, das den freigegebenen OTel-Export einschaltet, Operator-Labels
  via OTEL_RESOURCE_ATTRIBUTES, die zu FinOps-Dimensionen werden, die Tracing-Beta
  für die Subagent-Hierarchie und die Datenschutz-Stellschrauben — samt ihrer Pflichten — ausbuchstabiert.
---

Der OpenTelemetry-Export von Claude Code ist der **freigegebene Beobachtungspfad** für eine
governte Flotte: Er ist nicht plan-gegated, er trägt sitzungsattribuierte Telemetrie,
und die Managed-Settings-Ebene kann ihn für jeden Entwickler einschalten — ohne
irgendetwas zu proxyen. Diese Seite ist die *Enterprise*-Konfiguration aufbauend auf
[Claude Code verbinden](/de/how-to/connect-claude-code/): was flottenweit zu setzen ist, was
jede Stellschraube bringt und welche Pflicht sie erzeugt. Die Schlüsselnamen und die Semantik unten wurden
am 2026-06-10 gegen die eigene Dokumentation von Claude Code verifiziert (Client 2.1.17x);
prüfe sie dort erneut, bevor du neue codierst — sie entwickeln sich schnell.

:::note[Managed-env governt nur Claude Code]
Der Managed-`env`-Block konfiguriert den **Claude-Code-Prozess**. OTEL_*-Variablen
werden **nicht** an Subprozesse propagiert (Bash-Befehle, Hooks, MCP-Server); nur
`TRACEPARENT` wird von Shell-Subprozessen geerbt, solange Tracing aktiv ist. Plane die
Subprozess-Observability separat (das Kernel/eBPF-Backstop).
:::

## Was du bekommst

| Stellschraube | Was sie bringt | Pflicht, die sie erzeugt |
|---|---|---|
| Managed-Telemetrie-`env` | Jede Sitzung exportiert OTLP an deinen Collector — Beobachtung, die die eigene Konfiguration eines Entwicklers überdauert | Keine — strukturelle Telemetrie standardmäßig |
| `OTEL_RESOURCE_ATTRIBUTES` | Org-definierte Labels (Team, Projekt, Kostenstelle) auf **jedem Metrik-Datenpunkt und jedem Event-Record**; das Control Plane routet sie in FinOps-Ausgabendimensionen | Halte die Label-Werte nicht-sensitiv; der Connector setzt sie auf eine Allowlist und bereinigt sie |
| Tracing-Beta | `claude_code.llm_request` / `claude_code.tool`-Spans tragen `agent_id` / `parent_agent_id` — die **Subagent-Hierarchie pro Instanz** im Access Graph | Beta-Oberfläche: bei Upgrade verifizieren |
| `OTEL_LOG_TOOL_DETAILS=1` | `tool_parameters` auf Tool-Events — einschließlich **welcher Befehl abgelehnt wurde** bei einer abgelehnten Tool-Entscheidung | Tool-Eingaben verlassen den Host: eine Residenz-/Maskierungspflicht, die du selbst tragen musst |
| `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` | `app.entrypoint` (cli / sdk-ts / claude-vscode …) — welche Oberfläche jede Sitzung gestartet hat | Keine (Label niedriger Kardinalität) |

## Schritt 1 — den Export von der Managed-Ebene aus einschalten

Verfasse die Telemetrie-`env` in deiner Managed-Settings-Policy (der
`TelemetryEnv`-Helper des `managed-settings`-Connectors rendert genau diese
Haltung): Telemetrie aktivieren, den OTLP-Exporter auf den Control-Plane-Collector
richten und sowohl Metriken als auch Logs exportieren. Verweise für die vollständige Variablenreferenz auf
die eigene Monitoring-Dokumentation von Claude Code — kopiere keine Werte von hier von Hand.

:::caution[Niemals Collector-Credentials inline einbetten]
Eine Managed-Settings-Datei ist Klartext auf jedem Host. Die Authoring-Ebene weist
`OTEL_EXPORTER_OTLP_HEADERS` mit einem Wert genau aus diesem Grund zurück — authentifiziere
den Collector mit mTLS oder einer Secret-Manager-Referenz, niemals mit einem Inline-Token.
:::

Content-Erfassung (Prompts, Tool-Bodies) bleibt **aus**, sofern du dich nicht aktiv dafür entscheidest — und der
Control-Plane-Connector behält unabhängig davon nur strukturelle Daten, was auch immer der
Client emittiert.

## Schritt 2 — die Flotte für FinOps labeln

Setze `OTEL_RESOURCE_ATTRIBUTES` in derselben Managed-env, mit strikter W3C-Baggage-
Formatierung (Werte prozent-kodieren; keine Leerzeichen oder Anführungszeichen):

```
OTEL_RESOURCE_ATTRIBUTES=team=payments,project=atlas,cost_center=cc-42
```

Seit Client 2.1.161 reiten diese Werte auf **jedem Metrik-Datenpunkt und jedem Event-
Record** mit, nicht nur auf dem OTLP-Resource-Block — und benutzerdefinierte Schlüssel überschreiben niemals die
Standardattribute. Auf dem Control Plane listest du die Schlüssel, die du honorierst, in der
`resource_labels`-Allowlist des claude-Connectors; der Connector bereinigt die Werte und
hängt sie als Labels an die Identity-Edges der Sitzung und an jedes Cost
Sample. FinOps befördert `team` und `project` zu erstklassigen Ausgabendimensionen, sodass
"Claude-Code-Ausgaben nach Team aufschlüsseln" durchgängig funktioniert. Schlüssel, die nicht auf der Allowlist sind,
werden verworfen — minimale Daten standardmäßig.

## Schritt 3 — Subagent-Hierarchie (Tracing-Beta)

Aktiviere die Enhanced-Telemetry-Beta plus einen Traces-Exporter in der Managed-env, um
Spans zu erhalten. Die Subagent-Identitätsattribute (`agent_id`, `parent_agent_id`) sind
**span-only** — sie erscheinen auf keiner Metrik und keinem Log-Event — und leben auf den
`claude_code.llm_request`- (seit 2.1.139) und `claude_code.tool`- (seit 2.1.145)
Spans. Der Connector bildet sie in den Access Graph ab als:

- `session → identity.subagent` — die Subagent-**Instanz**, die gehandelt hat, und
- `parent agent → identity.subagent` — **wer sie gespawnt hat** (fehlt bei Agenten, die die
  Hauptsitzung direkt gespawnt hat).

Das ist es, was zwei gleichzeitige Subagenten desselben Typs unterscheidbar macht —
der `subagent_type` des `Agent`-Tools allein ist ein Typ-Label, keine Instanz.

## Schritt 4 — optionale Fidelity-Stellschrauben

- `OTEL_LOG_TOOL_DETAILS=1` fügt Tool-Events `tool_parameters` hinzu — auch bei abgelehnten
  Tool-Entscheidungen (seit 2.1.157), sodass ein Ablehnungs-Finding den
  bereinigten Befehl benennen kann, der blockiert wurde. Der Connector reduziert Eingaben bei der
  Ingestion auf bereinigte Resource-Referenzen und speichert sie niemals roh; aber die Werte verlassen
  DOCH den Host des Entwicklers, daher ist das Aktivieren eine bewusste Residenz-
  Entscheidung.
- `OTEL_METRICS_INCLUDE_ENTRYPOINT=true` fügt allen Metriken und Events `app.entrypoint`
  hinzu (standardmäßig aus). Der Connector erfasst es als Sitzungstopologie — eine
  SDK-eingebettete Flotte hat eine andere Risiko-Haltung als interaktive CLI-Nutzung.

## Ehrliche Grenzen dieses Pfads

- **Unauthentifizierte Loopback-Ingestion.** Der kooperative Receiver bindet standardmäßig an
  Loopback und muss dort bleiben; alles Erreichbare kann Telemetrie fälschen (siehe
  [Claude Code verbinden](/de/how-to/connect-claude-code/)).
- **Subprozesse sind nicht abgedeckt.** OTEL_* erreicht keine Bash-/Hook-/MCP-
  Subprozesse; nur `TRACEPARENT` wird unter Tracing geerbt.
- **Der Admin-Plane-Feed kann Drittanbieter nicht sehen.** Die Claude Code
  Analytics API verfolgt Nutzung nur auf der Claude API — Claude Platform on AWS,
  Microsoft Foundry, Amazon Bedrock und Gemini Enterprise Agent Platform (formerly Vertex AI) sind nicht enthalten. Für eine Flotte
  auf diesen Oberflächen ist **dieser OTel-Pfad die einzige Beobachtung, die du hast**, und
  der Shadow-Auth-Detektor auf dem Admin-Feed kann sie nicht freigeben.
- **Die Kostenzahlen hier sind Schätzungen.** Die Per-Request-Kostentelemetrie wird
  gegen die autoritativen Kostenberichte abgeglichen; eine Quelle der Kosten pro
  Sitzung, niemals beide.

## Nächste Schritte

- [Claude Code verbinden](/de/how-to/connect-claude-code/) — die Basis-Verkabelung, auf der diese
  Seite aufbaut.
- [Governance und Freigabe](/de/how-to/govern-and-approve/) — die Durchsetzungshälfte
  (Managed Settings, Hooks, der PEP).
- [Audit an Splunk weiterleiten](/de/how-to/forward-audit-to-splunk/) — die
  Findings, die diese Telemetrie erzeugt, an dein SIEM versenden.
