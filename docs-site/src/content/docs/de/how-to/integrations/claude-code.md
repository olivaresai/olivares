---
title: Claude Code integrieren
description: >-
  Claude Code in die Governance-Control-Plane einbinden: den Connector, Managed
  Settings, den governed PEP-Hook und die Anzeige in der Konsole im laufenden Betrieb.
---

Diese Integration bindet Claude Code in die Governance-Control-Plane ein, ohne Olivares AI zu
einem obligatorischen Proxy zu machen. Der `claude`-Connector empfängt OTLP-Telemetrie und
Hook-Events, korreliert Sessions und erfasst R/RW-Zugriffe, Kosten und Findings. Wenn präventive
Kontrolle erforderlich ist, fragt der verwaltete Hook `olivares claude-hook` den lokalen
Olivares-PEP vor jeder Tool-Nutzung ab. Diese beiden Ebenen sind unabhängig: Der Empfang von
Telemetrie bedeutet nicht, dass eine Policy durchgesetzt wird.

## Claude Code hinzufügen

### Voraussetzungen

- Ein Olivares-AI-Binary, das den First-Party-Connector `claude` enthält.
- Die UUID des Enterprise-Tenants, dem die Beobachtungen zugeordnet werden.
- Claude Code auf den zu verwaltenden Endgeräten. Der lokale Receiver benötigt keinen
  Anthropic-API-Key.
- Lokale Konnektivität von Claude Code zum Olivares-Receiver. Die Standardwerte sind
  `127.0.0.1:4317` für OTLP/gRPC und `127.0.0.1:4318` für OTLP/HTTP und kooperative Hooks.
- Ein ausführbarer temporärer Pfad für den Olivares-Dienst. `claude` läuft als isoliertes Plugin;
  auf Systemen, auf denen `/tmp` mit `noexec` gemountet ist, setzen Sie `TMPDIR` in der
  Service-Unit auf ein dediziertes Verzeichnis im Besitz des Olivares-Servicekontos.

Stellen Sie die OTLP-Receiver und den kooperativen Endpoint nicht außerhalb von Loopback bereit.
Sie authentifizieren den Sender nicht; jeder erreichbare Host könnte daher Telemetrie fälschen.
Der governte PEP ist eine getrennte Oberfläche: Er verwendet einen eigenen lokalen Socket,
authentifiziert jede Anfrage und zeichnet jede Entscheidung auf.

1. Öffnen Sie die **Control console** (`/console`) und wählen Sie den Tab **Connectors**. Das
   Connector-Roster ist global: Ein Superadmin-Konto ist erforderlich; Speichern, Testen und
   Neuladen erfordern AAL3-Elevation.
2. Fügen Sie eine Quelle vom Typ `claude` mit einem stabilen Betriebsnamen wie
   `claude-code-prod`, dem passenden Tenant, dem Modus `live`, dem Intervall `0` und aktiviertem
   Status hinzu. Ein Intervall von null ist korrekt: Dieser Connector hält Receiver offen, statt
   in Batches zu pollen.
3. Speichern Sie die Quelle und wählen Sie **Reload**. Die Zeile bestätigt Name, Typ, Modus und
   Status. Die Testaktion der Konsole ist für `claude` nicht verfügbar, da es sich um einen
   Out-of-Process-Connector handelt; die Validierung erfolgt beim Speichern. Der vollständige
   Open-Test verwendet `olivares sources test` und startet das Plugin.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Konfigurieren Sie, wer Zugang erhält und was verwaltet werden darf: Onboarding von Benutzern, Anbindung von SSO sowie Gestaltung von Arbeitsbereichen und Agent-Gruppen.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Konfigurieren Sie, wer Zugang erhält und was verwaltet werden darf: Onboarding von Benutzern, Anbindung von SSO sowie Gestaltung von Arbeitsbereichen und Agent-Gruppen.">

## Claude Code konfigurieren

Verteilen Sie zwei Konfigurationen gemeinsam: die Beobachtungsquelle und die verwaltete
Agent-Policy.

### 1. Receiver und Datenminimierung

Die sichere Anfangskonfiguration entspricht den Standardwerten:

| Quelleneinstellung | Anfangswert | Wirkung |
|---|---:|---|
| `enable_grpc` | `true` | Stellt OTLP/gRPC auf `grpc_addr` (`127.0.0.1:4317`) bereit. |
| `enable_http` | `true` | Stellt OTLP/HTTP und den kooperativen Hook auf `http_addr` (`127.0.0.1:4318`) bereit. |
| `hook_path` | `/hooks` | Pfad des kooperativen Hooks innerhalb des HTTP-Receivers. |
| `content_capture` | leer | Bewahrt die Struktur, aber keine Prompts, Tool-Bodys oder API-Bodys. Extended Reasoning wird immer redigiert. |
| `enforcement` | leer | Beobachtet Hooks; diese Quelle gibt keine präventiven Entscheidungen zurück. |
| `allow_public_bind` | `false` | Verweigert das Binden außerhalb von Loopback. |

Wenn ein Host mehrere OTLP-Receiver betreibt, weisen Sie jedem eine andere Loopback-Adresse zu
und verwenden Sie denselben Wert in der Agent-Konfiguration. Claude, Codex und Grok verwenden in
einigen Modi standardmäßig `4318` und können nicht gleichzeitig denselben Socket binden.

### 2. Managed Settings und der governte PEP

Erzeugen Sie die systemweite Claude-Code-Datei mit dem Olivares-Binary:

```sh
olivares agent managed-settings \
  --otel-endpoint http://127.0.0.1:4317 \
  --out /etc/claude-code/managed-settings.json
```

Der Generator installiert `allowManagedHooksOnly: true`, einen `PreToolUse`-Hook, der
`olivares claude-hook` ausführt, und den Redaktions-Hook `PostToolUse`. Er aktiviert außerdem
OTLP mit dem Protokoll `grpc`; daher verwendet der obige Endpoint Receiver `4317` und nicht den
HTTP-Receiver `4318`. Die Datei gehört in die verwaltete Systemebene, nicht in das `HOME` der
Session.

Der PEP-Server ist aktiviert, wenn Olivares mit einer über `OLIVARES_HOOK_PEP_CONFIG` angegebenen
Datei startet. Das folgende Beispiel ist eine gültige Policy für einen Tenant:

```json
{
  "listen": "127.0.0.1:8447",
  "tenants": [
    {
      "tenant": "11111111-1111-4111-8111-111111111111",
      "require_firm_identity": true,
      "enforcement": "enforce",
      "policy": {
        "version": "claude-prod-v1",
        "default": "allow",
        "rules": [
          {
            "tool": "Bash",
            "decision": "ask",
            "reason": "Shell commands require human confirmation"
          }
        ]
      }
    }
  ]
}
```

Von Olivares gestartete Sessions erhalten kurzlebige Werte für `OLIVARES_HOOK_PEP_URL`,
`OLIVARES_HOOK_PEP_TOKEN`, `OLIVARES_HOOK_PEP_TENANT` und die Agent-Zuordnung. Bei einer
unabhängig gestarteten Session muss der Betreiber diese Werte über den Secrets-Kanal bereitstellen;
schreiben Sie sie nicht in `managed-settings.json`. Wenn der Endpoint fehlt oder nicht verfügbar
ist, gibt `olivares claude-hook` `deny` zurück.

Verwenden Sie für einen zunächst nicht blockierenden Rollout den Modus `observe` mit einem
zukünftigen RFC3339-Wert für `observe_until`. Diese Ausnahme ist temporär: Ein fehlender,
ungültiger oder abgelaufener Zeitstempel wird zu `enforce`. Plattforminvarianten — einschließlich
Identität, Tenant, Kill Switch, Firewall und fail-closed-Fehlern — bleiben durchgesetzt, während
Business-Regeln beobachtet werden.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Step-up-Authentifizierung erforderlich — AAL3 (Hardware, phishing-resistent)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Step-up-Authentifizierung erforderlich — AAL3 (Hardware, phishing-resistent)">

## CLI-Nutzung

Die folgenden Ausgabenausschnitte wurden am 30. August 2026 mit dem aus diesem Worktree gebauten
Binary gemessen. Allgemeine Startmeldungen der Engine wurden ausgelassen.

### Quelle registrieren

Stoppen Sie bei SQLite die Engine, bevor Sie das Roster über die CLI ändern, da SQLite ein
Single-Writer-Profil verwendet. Bei PostgreSQL kann der Vorgang parallel zur Engine laufen.
Verwenden Sie für Live-Änderungen an SQLite die Konsole.

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --kind claude \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 0 \
  --config mode=live \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "claude-code-prod" (kind "claude", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → claude
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 0
  enabled: - → true
  config.mode: - → live
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

`--actor` und `--reason` sind erforderlich, da diese Änderung die Datenherkunft verändert und im
Audit-Ledger aufgezeichnet wird.

### Connector validieren und öffnen

```sh
olivares sources validate \
  --data-dir /var/lib/olivares \
  --name claude-code-prod

olivares sources test \
  --data-dir /var/lib/olivares \
  --name claude-code-prod \
  --timeout 20s
```

```text
source "claude-code-prod"
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
configuration: VALID (everything that can be decided without the network)
  ? not checked here: the "claude" connector runs out-of-process, so its connector identity is only known once the binary is launched (`olivares sources test` launches it)
source "claude-code-prod" (claude): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

`validate` öffnet keine Sockets. `test` ruft `Open` und `Close` auf, aber nicht `Gather`, bindet
die Quelle nicht in die Engine ein und beweist nicht, dass Claude Code Telemetrie sendet. Wenn das
Plugin trotz gesetztem Ausführungs-Bit mit `permission denied` fehlschlägt, prüfen Sie, ob das
`TMPDIR` des Prozesses auf einem `noexec`-Volume liegt.

### Fail-closed-Verhalten des Hooks bestätigen

Wenn der Endpoint absichtlich nicht konfiguriert ist, gibt der Client eine Ablehnung im von
Claude Code erwarteten Format zurück:

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' | olivares claude-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed PEP endpoint not configured (deny-closed)"}}
```

Diese Sonde prüft den lokalen Client und keine entfernte Policy-Entscheidung. Testen Sie in
Produktion außerdem eine erlaubte Regel, eine verweigerte Regel und eine `ask`-Anfrage mit fester
Identität, bevor Sie den Rollout ausweiten.

## Control Console

Das Hinzufügen einer Quelle erzeugt keine historischen Daten. Nach dem Neuladen des Rosters und
dem Empfang des ersten Events können Betreiber die folgenden Ansichten verwenden:

| Ort | Anzeige | Interpretation des Zustands |
|---|---|---|
| **Control console > Connectors** (`/console`) | Name, Typ `claude`, Modus, nicht geheime Konfiguration, Roster-Status sowie Speicher- und Reload-Aktionen. | „Gespeichert“ beweist Persistenz. Es beweist nicht, dass ein Event eingetroffen ist. |
| **Health > Connectors** (`/health`) | Connector-Zustand, Betriebsmeldung, Trend und zuletzt bekannter Poll bzw. letzte Aktivität. | Ein offener Receiver kann gesund sein, während der Agent still bleibt. |
| **Observability > Ingestion** (`/observability`) | Datensätze nach Quelle, Typen `edge`, `cost` und `finding`, Signal sowie erstes/letztes Event. | Dies sind prozessweite Zähler seit dem Start; sie werden beim Neustart zurückgesetzt und sind nicht Tenant-spezifisch. |
| **Sessions** (`/sessions`) | Session, Zustand, Aktion, Modell, Tokens, Kosten, letzte Aktivität sowie Haltung `enforced` oder `observed`. | Die Haltung fasst Event-Evidenz zusammen; sie wird nicht aus der Registrierung des Connectors abgeleitet. |
| **Access map** (`/access-map`) | Aus beobachteten Tools, MCP und Ressourcen zugeordnete R/RW-Edges. | Eine beobachtete Edge beschreibt Aktivität und entspricht keiner vorherigen Autorisierung. |
| **Cost & FinOps** (`/finops`) | Aus empfangener Telemetrie abgeleitete Kosten- und Token-Samples. | Die Abdeckung ist auf Exporte der Flotte begrenzt; Aufrufe ohne OTLP lassen sich nicht rekonstruieren. |
| **Security** (`/security`) | Telemetrielücken, Sandbox-/MCP-Haltung und andere ausgegebene Findings. | Ein fehlendes Finding macht eine unbeobachtete Oberfläche nicht compliant. |
| **Claude Policy** (`/claude-policy`) | Authoring, Verteilung, Versionen und Check-in-Status für verwaltete Claude-Code-Oberflächen. | Verteilung und Drift-Verifikation sind getrennte Fakten und werden separat angezeigt. |

<img class="light:sl-hidden" src="/console/sessions-dark.png" alt="Live-Betrieb von Agenten — was jede Sitzung gerade tut, ihre Tokens, Kosten und Taktung, aktualisiert über einen Live-Stream.">
<img class="dark:sl-hidden" src="/console/sessions-light.png" alt="Live-Betrieb von Agenten — was jede Sitzung gerade tut, ihre Tokens, Kosten und Taktung, aktualisiert über einen Live-Stream.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Befunde der Schutzleitplanken, die Durchsetzungslage, die Anomalie-Warteschlange und manipulationssichere Vorfallforensik. Die Ebene ist standardmäßig detektivisch — sie zeichnet auf, sie blockiert nicht von sich aus, sofern die Durchsetzung nicht aktiviert und gesteuert ist.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Befunde der Schutzleitplanken, die Durchsetzungslage, die Anomalie-Warteschlange und manipulationssichere Vorfallforensik. Die Ebene ist standardmäßig detektivisch — sie zeichnet auf, sie blockiert nicht von sich aus, sofern die Durchsetzung nicht aktiviert und gesteuert ist.">

## Produktionseinsatz

- **Gestufter Rollout:** Beginnen Sie mit strukturellen Inhalten und Regeln im Beobachtungsmodus
  mit Ablaufdatum. Prüfen Sie False Positives und stellen Sie dann jeden Tenant auf `enforce` um.
- **Flottenverwaltung:** Verteilen Sie `/etc/claude-code/managed-settings.json` per RPM,
  unveränderlichem Image, Ansible, Salt oder einem gleichwertigen Enterprise-Konfigurationsmanager.
  Prüfen Sie die Live-Datei mit einer zweiten `managed-settings`-Quelle auf Abwesenheit oder Drift.
- **Funktionstrennung:** Das Plattformteam pflegt Receiver und Verfügbarkeit; das Security-Team
  versioniert Regeln; Tenant-Verantwortliche prüfen `ask`-Anfragen und Findings. Jede privilegierte
  Änderung bleibt zuordenbar.
- **Datenminimierung:** Lassen Sie `content_capture` leer, sofern kein genehmigter forensischer
  Bedarf mit definierter Residenz und Aufbewahrung besteht. Strukturdaten reichen üblicherweise
  für Adoptions- und Kostenanalysen aus.
- **Gehärtete Hosts:** Halten Sie Receiver auf Loopback, stellen Sie dem Plugin ein minimales
  ausführbares temporäres Verzeichnis bereit und machen Sie die Policy schreibgeschützt. Lockern
  Sie `noexec` nicht global, nur damit der Connector startet.

## Was durchgesetzt und was nur beobachtet wird

| Oberfläche | Tatsächliches Verhalten |
|---|---|
| OTLP-Telemetrie und kooperativer Hook des `claude`-Connectors | **Beobachtet.** Der Sender kooperiert; der Loopback-Receiver authentifiziert nicht, und ein lokaler Prozess kann ein Signal auslassen oder fälschen. |
| Leere Einstellung `enforcement` an der Quelle | **Beobachtet.** Dies ist der Standard und blockiert keine Tools. |
| `olivares claude-hook` + PEP + Managed Settings | **Setzt** `allow`, `ask` oder `deny` für Events durch, die Claude Code mit einem Veto belegen kann, und zeichnet die Entscheidung auf. Endpoint-Fehler führen nach dem deny-closed-Prinzip zur Ablehnung. |
| `allowManagedHooksOnly` in der verwalteten Ebene | **Härtet die Installation** gegen Benutzer- oder Projekt-Hooks, die mit dem PEP konkurrieren könnten. |
| `PostToolUse` | **Beobachtet und redigiert nach der Aktion.** Bereits erzeugte Wirkungen des Tools können nicht rückgängig gemacht werden. |
| Aktionen außerhalb des Claude-Code-Prozesses und Hooks | **Durch diese Verdrahtung nicht abgedeckt.** Verwenden Sie Betriebssystemkontrollen, natives Auditing und Netzwerk-Policies als Rückfallebenen. |

Die Betriebsverifikation erfordert vier getrennte Prüfungen: ein persistiertes Roster, einen
geöffneten Connector, ein in **Ingestion** sichtbares Event und ein tatsächlich vom PEP blockiertes
Tool. Keine dieser Prüfungen ersetzt eine der drei anderen.
