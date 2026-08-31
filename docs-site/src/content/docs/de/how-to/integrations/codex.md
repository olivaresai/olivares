---
title: Codex integrieren
description: >-
  Codex in die Governance-Control-Plane einbinden: den Connector, Managed Config,
  den governed Hook und die Anzeige in der Konsole im laufenden Betrieb.
---

Olivares AI integriert Codex über drei einander ergänzende Ebenen. Im schreibgeschützten Modus
liest die Quelle `codex` Analytics, Compliance, Audit Logs und abgerechnete Kosten mithilfe von
Enterprise-Automatisierungs-Zugangsdaten. Der Connector `codex-managed-config` inventarisiert und
prüft die bereitgestellte System-Policy. Schließlich leitet `olivares codex-hook` Sessions und
Tool-Entscheidungen an den lokalen PEP weiter. Eine über ein persönliches ChatGPT-Abonnement
authentifizierte Session gewährt für sich allein keinen Zugriff auf die Enterprise-APIs.

## Codex hinzufügen

### Voraussetzungen

- Ein Olivares-AI-Enterprise-Tenant und ein Superadmin-Konto mit AAL3-Elevation für
  Roster-Vorgänge.
- Für die Enterprise-Ingestion ein Plattform-API-Key oder ein Workspace-Access-Token mit den
  erforderlichen Read-Scopes sowie die `workspace_id`. Die Anmeldung an der Codex CLI über
  ChatGPT stellt keine Connector-Zugangsdaten bereit.
- Administrativer Zugriff auf die Systemebene des Hosts, um `/etc/codex/requirements.toml`,
  `/etc/codex/managed_config.toml` und den vertrauenswürdigen Hook zu verteilen.
- Ein dedizierter Loopback-Socket für den Codex-PEP. Der Standardwert ist `127.0.0.1:8448`;
  verwenden Sie ihn nicht gemeinsam mit Claude oder Grok, da jeder Agent ein anderes
  Antwortformat erwartet.

1. Öffnen Sie die **Control console** (`/console`) und wählen Sie **Connectors**.
2. Fügen Sie eine Quelle vom Typ `codex` mit einem stabilen Namen, dem Tenant und einem
   Batch-Intervall hinzu. `300` Sekunden sind ein sinnvoller Ausgangspunkt für einen Pilotbetrieb;
   passen Sie die Häufigkeit an das API-Budget und das Aktualitätsziel an.
3. Geben Sie für eine Enterprise-Quelle die Zugangsdaten in das geheime Feld `api_key` ein,
   wählen Sie `auth_mode` (`api_key` oder `access_token`) und geben Sie `workspace_id` an. Die
   Konsole versiegelt den Wert und gibt ihn nie zurück. Speichern, testen und laden Sie die
   Quelle neu.

Sie können `codex` auch ohne Zugangsdaten für ein lokales Kataloginventar hinzufügen. Dieser
Modus fragt weder Analytics, Compliance, Audit Logs noch Costs ab, und `Gather` gibt keine
entfernten Beobachtungen aus.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Konfigurieren Sie, wer Zugang erhält und was verwaltet werden darf: Onboarding von Benutzern, Anbindung von SSO sowie Gestaltung von Arbeitsbereichen und Agent-Gruppen.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Konfigurieren Sie, wer Zugang erhält und was verwaltet werden darf: Onboarding von Benutzern, Anbindung von SSO sowie Gestaltung von Arbeitsbereichen und Agent-Gruppen.">

## Codex konfigurieren

### 1. Schreibgeschützte Enterprise-Quelle

Die folgenden Einstellungen definieren die Abdeckung:

| Einstellung | Standard | Zweck |
|---|---:|---|
| `api_key` | leer | Referenz auf Automatisierungs-Zugangsdaten. Ein leerer Wert aktiviert nur den Offline-Katalog. |
| `auth_mode` | `api_key` | Kennzeichnet die Zugangsdaten als `api_key` oder `access_token`; beide werden als Bearer-Token gesendet. |
| `workspace_id` | leer | Erforderlich für Workspace-bezogene Analytics und Compliance. |
| `analytics` | `true` | Codex-Nutzung und -Adoption; erzeugt strukturierte Samples und Findings. |
| `compliance` | `true` | Codex-Compliance-Logs als Aktivitätsevidenz. |
| `audit` | `true` | Audit Logs der Organisation als Evidenz. |
| `costs` | `false` | Täglich abgerechnete Kosten. Aktivieren Sie dies mit `project_id`, um Codex keine fremden Ausgaben zuzuordnen. |
| `attribute_email` | `false` | Behält `user_id` als stabilen Akteur bei und vermeidet E-Mail als Zuordnungs-PII. |
| `compliance_prompt_scan` | `false` | Sucht bei Aktivierung transient nach Risikomustern und speichert nur strukturierte Findings. |
| `otlp_http` | `false` | Experimenteller Log-Receiver; deaktiviert, weil er einen Port öffnet. Derzeit zählt und leert er Events, konvertiert sie aber nicht in Sessions. |

Lassen Sie `otlp_http` für die Erstintegration deaktiviert. Der governte Hook stellt die
vollständige Session-Ebene bereit; die Aktivierung von OTLP in dieser Version ersetzt diese
Installation nicht.

Speichern Sie die Zugangsdaten über die CLI außerhalb der Shell-History und referenzieren Sie
sie über ihren Namen:

```sh
olivares secrets put \
  --data-dir /var/lib/olivares \
  --name codex/enterprise \
  --value-file /run/secrets/codex-enterprise-token \
  --actor platform-operator \
  --reason codex-enterprise-onboarding

olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-enterprise \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --config api_key=store:codex/enterprise \
  --config auth_mode=access_token \
  --config workspace_id=ws_eng \
  --actor platform-operator \
  --reason codex-enterprise-onboarding
```

Wenn Sie `costs=true` aktivieren, fügen Sie außerdem `project_id=<project-id>` hinzu. Ohne diese
Einschränkung ist die Costs API organisationsweit und kann Ausgaben einbeziehen, die nicht zu
Codex gehören.

### 2. Systemanforderungen und verwaltete Werte

Olivares hält zwei Ebenen getrennt:

- `requirements.toml` enthält Einschränkungen, die Benutzer nicht ausweiten können:
  Genehmigungs-Policies, Sandbox-Modi, Websuche, Remote Control, Hook-Vertrauen, verbotene
  Lesezugriffe und erlaubte MCP-Server.
- `managed_config.toml` enthält verwaltete Anfangswerte. Dies sind Standardwerte; jede
  unveränderlich erforderliche Einschränkung gehört in `requirements.toml`.

Das folgende Policy-Dokument ist gültig und verweigert standardmäßig Netzwerkzugriff, Websuche,
Remote Control und MCP, während Schreibzugriffe auf den Workspace beschränkt bleiben:

```json
{
  "requirements": {
    "allowed_approval_policies": ["untrusted", "on-request"],
    "allowed_sandbox_modes": ["read-only", "workspace-write"],
    "allowed_web_search_modes": [],
    "allow_remote_control": false,
    "allow_managed_hooks_only": true,
    "deny_read": ["~/.ssh"],
    "allowed_mcp_servers": []
  },
  "managed_config": {
    "approval_policy": "on-request",
    "sandbox_mode": "workspace-write",
    "web_search": "disabled",
    "network_access": false
  }
}
```

Validieren Sie die Policy vor der Verteilung und erzeugen Sie danach beide Artefakte mit
demselben Befehl:

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate

olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --requirements-out /etc/codex/requirements.toml \
  --managed-config-out /etc/codex/managed_config.toml
```

Das Rendering schlägt vor dem Schreiben fehl, wenn die Policy einen unbekannten Enum-Wert,
einen MCP-Server ohne Identität oder ungültiges TOML enthält. Registrieren Sie für spätere
Prüfungen des Live-Zustands und der Drift eine zusätzliche Quelle vom Typ
`codex-managed-config`; sie liest beide Systemdateien, ohne sie zu ändern.

### 3. Session-Hook und PEP

Codex liest den gemessenen Hook aus `$CODEX_HOME/hooks.json`. `command` muss eine Zeichenkette
und kein Array sein: Ein Array kann geparst werden, obwohl der Hook nie läuft. Auch die
Inline-Tabelle `[hooks]` in `config.toml` wurde von der gemessenen Version nicht gelesen.

```json
{
  "description": "olivares governed hooks",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PreToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "olivares codex-hook"}]}
    ]
  }
}
```

Der Server wird beim Start von Olivares gemountet, wenn `OLIVARES_CODEX_HOOK_PEP_CONFIG` auf
gültiges JSON verweist:

```json
{
  "listen": "127.0.0.1:8448",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Jede Instanz verwaltet einen Tenant, und die Entscheidung kommt aus dem bereits in Olivares
konfigurierten PDP. Der Client verwendet `OLIVARES_CODEX_HOOK_URL`,
`OLIVARES_CODEX_HOOK_TOKEN`, `OLIVARES_CODEX_HOOK_TENANT`, `OLIVARES_CODEX_HOOK_AGENT`,
`OLIVARES_CODEX_HOOK_ORG` und `OLIVARES_CODEX_HOOK_ACCOUNT`. Stellen Sie diese Werte über den
Prozess und den Secrets Manager bereit; betten Sie sie nicht in `hooks.json` ein.

`allow_managed_hooks_only=true` ist erforderlich, bevor der Hook als Flottenkontrolle dargestellt
werden darf. Ohne erzwungenes Vertrauen kann Codex einen Hook auslassen, ohne ein Event oder eine
Warnung zu erzeugen; eine stille Installation ist keine Enforcement-Evidenz.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Step-up-Authentifizierung erforderlich — AAL3 (Hardware, phishing-resistent)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Step-up-Authentifizierung erforderlich — AAL3 (Hardware, phishing-resistent)">

## CLI-Nutzung

Die Ausgabebeispiele wurden am 30. August 2026 gemessen. Allgemeine Start-Logs wurden
ausgelassen, sodass nur die Befehlsantworten verbleiben.

### Reproduzierbare Offline-Registrierung

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --kind codex \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 300 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "codex-demo" (kind "codex", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → codex
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 300
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

Führen Sie Roster-Änderungen bei SQLite offline und bei gestoppter Engine aus; bei PostgreSQL
können sie parallel zur Engine laufen. Die Konsole ist der empfohlene Weg für Live-Änderungen an
SQLite.

### Konnektivitätstest und seine Grenzen

Die reproduzierbare Messung auf dem Screenshot-Host am 30. August 2026 ergab folgendes Ergebnis:

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name codex-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "codex-demo" (codex): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

Der Prozess wurde mit Code `0` beendet. Auf dem Host war eine Codex-CLI-Session über ChatGPT
authentifiziert, aber `codex-demo` hatte keinen `api_key`: Dieses Ergebnis beweist nur den
Offline-Katalog und dass `Open` die Konfiguration akzeptierte. Es beweist keine
OpenAI-Authentifizierung, ruft `Gather` nicht auf und liest keine einzige Analytics- oder
Compliance-Zeile. Selbst mit Zugangsdaten sendet `sources test` keine Upstream-Anfrage, da `Open`
nur die Clients erstellt. Der erste Datentest besteht aus einem echten Engine-Poll mit
anschließend sichtbaren Beobachtungen.

### Verwaltete Policy validieren

```sh
olivares codex managed-config \
  --policy /etc/olivares/codex-policy.json \
  --validate
```

```text
ok: policy renders to valid Codex managed-config TOML
```

### Lokale Ablehnung des Hooks testen

Wenn der Endpoint absichtlich fehlt:

```sh
printf '%s\n' '{"hook_event_name":"PreToolUse","session_id":"019fc4c3-40c5-7371-9c92-7b269d23897b","tool_name":"Bash","tool_input":{"command":"echo hi"},"tool_use_id":"exec-1"}' | olivares codex-hook
```

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"governed endpoint not configured (deny-closed)"}}
```

Der Prozess wird mit Code `0` beendet, da die Ablehnung im von Codex interpretierten JSON
übertragen wird. Diese Sonde verifiziert den fail-closed-Client; die Annahme eines
`PreToolUse`-Events durch Codex muss zusätzlich auf einem Host getestet werden, auf dem der Hook
als vertrauenswürdig markiert ist.

## Control Console

| Ort | Anzeige | Bedingung für die Anzeige |
|---|---|---|
| **Control console > Connectors** (`/console`) | Quelle, Modus, Häufigkeit, nicht geheime Konfiguration sowie Test/Save/Reload-Aktionen. | Die persistierte Quelle erscheint sofort, ihre Daten nicht. |
| **Health > Connectors** (`/health`) | Connector-Status, Meldung, Trend und letzte Aktivität. | Nachdem das Roster neu geladen wurde. |
| **Observability > Ingestion** (`/observability`) | Zähler für `olivares.codex`, Beobachtungstypen und erster/letzter Empfang. | Nachdem `Gather` Daten ausgegeben hat. Diese prozessweiten Zähler beginnen beim Start und werden bei einem Neustart zurückgesetzt. |
| **Cost & FinOps** (`/finops`) | Geschätzte Analytics-Nutzung und, wenn aktiviert, täglich abgerechnete Kosten. | Gültige Zugangsdaten, `workspace_id` und autorisierte APIs; `costs` erfordert explizites Opt-in. |
| **Security** (`/security`) | Adoptions-Findings, nicht verfügbare Enterprise-Oberflächen und opt-in strukturierte Analyse von Compliance-Daten. | Nach der Erfassung; 403/404-Antworten von Enterprise-Oberflächen werden zu Haltungsevidenz und nicht zu Erfolg. |
| **Sessions** (`/sessions`) | Sessions und Timeline mit Aktion, Modell, Identität, Kosten und Haltung. | Stammt aus dem verwalteten Hook. Die Batch-Quelle allein erzeugt keine Live-Session. |
| **Audit** (`/audit`) | Importierte Aktivitätsevidenz und im Ledger verankerte PEP-Entscheidungen. | Nachdem zuordenbare Logs oder Entscheidungen empfangen wurden. |

Behandeln Sie den Offline-Katalog nicht als Beweis dafür, dass das Modellpanel entferntes
Inventar enthält. Der Connector stellt dem Runtime einen Katalog bereit, aber kein
Modul-Consumer in diesem Baum veröffentlicht ihn auf dieser Ansicht.

<img class="light:sl-hidden" src="/console/health-dark.png" alt="Lebendigkeit, Zuverlässigkeit und Abhängigkeiten in Ihrem gesamten Bestand — abgeleitet aus beobachteter Aktivität und dem Veralterungs-Sweep, niemals durch Abfragen der Infrastruktur.">
<img class="dark:sl-hidden" src="/console/health-light.png" alt="Lebendigkeit, Zuverlässigkeit und Abhängigkeiten in Ihrem gesamten Bestand — abgeleitet aus beobachteter Aktivität und dem Veralterungs-Sweep, niemals durch Abfragen der Infrastruktur.">
<img class="light:sl-hidden" src="/console/finops-dark.png" alt="Token-Kosten über den gesamten Bestand — Trends, Chargeback, Abgleich, Budgets und Prognose. Die Zahlen entsprechen exakt den Angaben des FinOps-Hauptbuchs.">
<img class="dark:sl-hidden" src="/console/finops-light.png" alt="Token-Kosten über den gesamten Bestand — Trends, Chargeback, Abgleich, Budgets und Prognose. Die Zahlen entsprechen exakt den Angaben des FinOps-Hauptbuchs.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Befunde der Schutzleitplanken, die Durchsetzungslage, die Anomalie-Warteschlange und manipulationssichere Vorfallforensik. Die Ebene ist standardmäßig detektivisch — sie zeichnet auf, sie blockiert nicht von sich aus, sofern die Durchsetzung nicht aktiviert und gesteuert ist.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Befunde der Schutzleitplanken, die Durchsetzungslage, die Anomalie-Warteschlange und manipulationssichere Vorfallforensik. Die Ebene ist standardmäßig detektivisch — sie zeichnet auf, sie blockiert nicht von sich aus, sofern die Durchsetzung nicht aktiviert und gesteuert ist.">

## Produktionseinsatz

- **Pilot ohne Zugangsdaten:** Validieren Sie Packaging und Roster mit `codex-demo`, kennzeichnen
  Sie es aber als Offline-Katalog. Verwenden Sie es nicht als Enterprise-Konnektivitätsindikator.
- **Governance-Ingestion:** Verwenden Sie eine schreibgeschützte Automatisierungsidentität und den
  minimalen API-Satz. Lassen Sie `attribute_email=false`, sofern keine genehmigte
  Chargeback-Anforderung besteht.
- **Endpoint-Kontrolle:** Erzeugen Sie die TOML-Dateien aus einer versionierten Policy, verteilen
  Sie sie über das Flottenkonfigurationssystem und pollen Sie ihren Zustand mit
  `codex-managed-config`, um Absicht, Bereitstellung und Drift zu unterscheiden.
- **Session-Kontrolle:** Installieren Sie Hooks zuerst in einer Canary-Gruppe. Bestätigen Sie,
  dass `PreToolUse` eine harmlose Aktion blockiert, bevor Sie den Ring ausweiten. Ein Hook, der
  kein Event erzeugt hat, darf nicht als governed gezählt werden.
- **Korrekte FinOps:** Aktivieren Sie `costs` nur, wenn `project_id` die Daten auf Codex-Ausgaben
  beschränkt. Verwenden Sie Analytics für die Adoption und die Costs API für den abgerechneten
  Betrag; addieren Sie beides nicht, als handele es sich um zwei Rechnungen.

## Was durchgesetzt und was nur beobachtet wird

| Oberfläche | Tatsächliches Verhalten |
|---|---|
| `codex`-Quelle und Enterprise-APIs | **Beobachtet, schreibgeschützt.** Ändert weder die OpenAI-Konfiguration noch fängt es Inferenz ab. |
| Modus ohne `api_key` | **Offline-Katalog.** Beweist weder das ChatGPT-Abonnement noch die Remote-API oder den Workspace. |
| `requirements.toml` | **Setzt Systemeinschränkungen durch**, die Benutzer nicht ausweiten können, einschließlich ausschließlichen Vertrauens in verwaltete Hooks. |
| `managed_config.toml` | **Setzt verwaltete Standardwerte.** Ersetzt keine Einschränkung in `requirements.toml`. |
| `codex-managed-config` | **Beobachtet und vergleicht Drift.** Korrigiert niemals Dateien auf dem Host. |
| `olivares codex-hook` bei `PreToolUse` oder `PermissionRequest` | **Kann die Aktion verhindern.** Codex akzeptiert `permissionDecision=allow` nicht; Olivares stellt Allow als Nichteingriff dar und übersetzt eine `ask`-Anfrage in eine Ablehnung. |
| `PostToolUse` und Lifecycle-Events | **Evidenz mit ungleichen Fähigkeiten.** Ein späterer Block kann ein ausgeführtes Tool nicht rückgängig machen, und `SessionEnd` hat keine Veto-Ausgabe. |
| Codex-OTLP-Receiver | **Partieller Empfang in dieser Version.** Zählt und leert Events, wandelt sie aber noch nicht in Sessions oder Findings um. |

Der Abschluss ist kumulativ: Die Quelle muss neu geladen sein, das erste `Gather` muss
Enterprise-Daten zurückgeben, die System-Policy muss verifiziert sein, der vertrauenswürdige Hook
muss beobachtet sein und `PreToolUse` muss nachweislich mit einem Veto belegt werden. `ANSWERED`
deckt nur den ersten Teil von `Open` ab.
