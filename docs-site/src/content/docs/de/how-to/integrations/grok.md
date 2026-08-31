---
title: Grok Build integrieren
description: >-
  Grok Build in die Governance-Control-Plane einbinden: den Connector, den
  governed Hook und die Anzeige in der Konsole im laufenden Betrieb.
---

Die `grok`-Integration stellt **Grok Build, den Terminal-Agent**, von dem Host aus, auf dem er
läuft, unter Governance. Im schreibgeschützten Modus liest sie die TOML-Konfiguration, das Sandbox-Profil, Namen von
MCP-Servern, Systemanforderungen und die Datei, die Hooks deaktiviert. Sie kann auch OTLP-Traces
empfangen. Dies ist nicht der xAI-API-Connector: Er fragt keine entfernten Modelle ab und benötigt
kein Provider-Secret. Präventive Tool-Kontrolle verwendet `olivares grok-hook` und einen separaten
lokalen PEP.

## Grok Build hinzufügen

### Voraussetzungen

- Olivares AI und Grok Build auf demselben Host oder schreibgeschützt auf dem Connector-Host
  gemountete Grok-Konfigurationspfade.
- Die UUID des Tenants, dem die Haltung zugeordnet wird.
- Die Berechtigung des Olivares-Servicekontos, `~/.grok/config.toml`,
  `/etc/grok/requirements.toml`, `~/.grok/disabled-hooks` und, falls konfiguriert, die kompatible
  `managed-settings.json` zu lesen.
- Ein Superadmin-Konto mit AAL3-Elevation, wenn die Quelle über die Konsole erstellt wird.

Geben Sie für diese Quelle keinen xAI-Key ein. Sie hat kein Secret-Feld und führt keine
Inference-API-Aufrufe aus.

1. Öffnen Sie die **Control console** (`/console`) und wählen Sie den Tab **Connectors**.
2. Fügen Sie eine Quelle vom Typ `grok` mit dem Namen `grok-demo` — oder einem stabilen
   Hostnamen —, dem Tenant, einem Batch-Intervall und aktiviertem Status hinzu. `60` Sekunden
   machen Haltungsänderungen in einem Pilotbetrieb sichtbar, ohne lokale Dateizugriffe in eine
   kontinuierliche Schleife zu verwandeln.
3. Speichern Sie die Quelle, wählen Sie **Test** und laden Sie das Roster neu. Die Zeile bestätigt
   den Roster-Eintrag; erst das nächste `Gather` liest die Dateien und gibt Findings aus.

<img class="light:sl-hidden" src="/console/guias-connectors-dark.png" alt="Konfigurieren Sie, wer Zugang erhält und was verwaltet werden darf: Onboarding von Benutzern, Anbindung von SSO sowie Gestaltung von Arbeitsbereichen und Agent-Gruppen.">
<img class="dark:sl-hidden" src="/console/guias-connectors-light.png" alt="Konfigurieren Sie, wer Zugang erhält und was verwaltet werden darf: Onboarding von Benutzern, Anbindung von SSO sowie Gestaltung von Arbeitsbereichen und Agent-Gruppen.">

## Grok Build konfigurieren

### 1. Hostinventar und Anforderungen

| Quelleneinstellung | Standard | Messgegenstand |
|---|---|---|
| `agent_ref` | `grok-build` | Stabile Referenz in den Findings. |
| `config_path` | `~/.grok/config.toml` | Vom Benutzer deklariertes Sandbox-Profil und Namen der MCP-Server. |
| `requirements_path` | `/etc/grok/requirements.toml` | Systemebene, die die effektive Konfiguration einschränkt. |
| `disabled_hooks_path` | `~/.grok/disabled-hooks` | Vom Benutzer deaktivierte Hook-Namen, einer pro Zeile. |
| `managed_settings_path` | leer | Mit Grok kompatible `managed-settings.json` von Claude Code; leer bedeutet „nicht gemessen“. |
| `otlp_http` | `false` | Trace-Receiver; deaktiviert, bis der Betreiber einen Port reserviert. |

Unter Linux lautet die Mindestanforderung für die Durchsetzung der Sandbox:

```toml
[sandbox]
profile = "strict"
```

Verteilen Sie dies in `/etc/grok/requirements.toml` mit administrativer Eigentümerschaft. `strict`
beschränkt Schreibzugriffe auf den Workspace, `~/.grok/` und temporäre Verzeichnisse und blockiert
den Netzwerkzugriff gemäß der dokumentierten Linux-Garantie. Derselbe Wert in
`~/.grok/config.toml` ist nur eine Benutzerpräferenz: Kommandozeilenoptionen und die Umgebung
können die Konfiguration beeinflussen, während `requirements.toml` die einschränkende Ebene ist.

Um MCP einzuschränken, deklarieren Sie in `requirements.toml` nur die Tabellen
`[mcp_servers.<nombre-aprobado>]`, welche die Flotte verwenden darf. Olivares inventarisiert die
Namen, nicht die Befehle, URLs oder Zugangsdaten in diesen Tabellen. Eine fehlende Datei, eine
nicht lesbare Datei und eine vorhandene Datei ohne `[mcp_servers]` erzeugen unterschiedliche
Zustände; „nicht gemessen“ wird nie als „keine“ angezeigt.

Grok kann aus Kompatibilitätsgründen auch `/etc/claude-code/managed-settings.json` lesen. Setzen
Sie `managed_settings_path` nur, wenn Olivares diese Oberfläche messen soll. Verwenden Sie einen
Claude-Hook nicht ungeprüft wieder: Grok-Payloads verwenden camelCase-Schlüssel und
snake_case-Events und benötigen `olivares grok-hook`.

### 2. Governter Hook

Installieren Sie `olivares grok-hook` über den nativen Discovery-Mechanismus der bereitgestellten
Grok-Version: entweder über eine Settings-JSON-Datei, aus der Grok den Schlüssel `hooks` liest,
oder über eine `*.json`-Datei in einem Hook-Verzeichnis wie `~/.grok/hooks/`. Grok lädt diese
Dateien nach Namen. Olivares definiert den vollständigen Authoring-Wrapper nicht, und dieser Baum
enthält ihn nicht; verwenden Sie das Schema der installierten Version und setzen Sie den Befehl
exakt auf:

```text
olivares grok-hook
```

Der PEP wird gemountet, wenn `OLIVARES_GROK_HOOK_PEP_CONFIG` beim Start von Olivares auf eine
gültige Konfiguration verweist:

```json
{
  "listen": "127.0.0.1:8449",
  "tenant": "11111111-1111-4111-8111-111111111111"
}
```

Jede Instanz verwaltet einen Tenant und erfordert eine feste Identität. Der Client liest
`OLIVARES_GROK_HOOK_URL`, `OLIVARES_GROK_HOOK_TOKEN`, `OLIVARES_GROK_HOOK_TENANT`,
`OLIVARES_GROK_HOOK_AGENT`, `OLIVARES_GROK_HOOK_ORG` und `OLIVARES_GROK_HOOK_ACCOUNT`. Stellen
Sie diese Werte über den Prozess und den Secrets Manager bereit; das Token gehört nicht in das
Hook-JSON.

Der dem Hook zugewiesene Name ist relevant. Ein Benutzer kann ihn zu `~/.grok/disabled-hooks`
hinzufügen, woraufhin der Dispatcher ihn unabhängig von seiner verwalteten Herkunft auslässt.
Weder `requirements.toml` noch MDM schränken diese Datei ein. Der Connector liest sie und gibt
ein Finding mit hohem Schweregrad und den deaktivierten Namen aus, kann die Deaktivierung aber
nicht verhindern.

### 3. Optionale OTLP-Traces

Wenn `otlp_http=true` gilt, lauscht der Receiver standardmäßig auf `127.0.0.1:4318` und akzeptiert
`POST /v1/traces`, den für Grok Build gemessenen Pfad. Diese nicht authentifizierte Eingabe muss
auf Loopback bleiben. Wenn ein anderer Connector bereits `4318` verwendet, wählen Sie einen freien
lokalen Port und übernehmen Sie denselben Wert in `otlp_http_addr` und den OTLP-Endpoint des Agents.

Die Erfassung reduziert Traces auf Zuordnung, Span-Namen und `session_id`; Inhalte werden nicht
gespeichert. In dieser Version gibt der nächste Poll ein aggregiertes Finding mit Span-, Session-
und Drop-Zahlen aus. Verwenden Sie den Hook für Timeline und Tool-bezogene Kontrolle.

<img class="light:sl-hidden" src="/console/guias-config-step-up-dark.png" alt="Step-up-Authentifizierung erforderlich — AAL3 (Hardware, phishing-resistent)">
<img class="dark:sl-hidden" src="/console/guias-config-step-up-light.png" alt="Step-up-Authentifizierung erforderlich — AAL3 (Hardware, phishing-resistent)">

## CLI-Nutzung

Die folgenden Beispiele wurden am 30. August 2026 mit dem Worktree-Binary ausgeführt. Allgemeine
Startmeldungen wurden ausgelassen.

### Lokale Quelle registrieren

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --kind grok \
  --tenant 11111111-1111-4111-8111-111111111111 \
  --poll-seconds 60 \
  --actor platform-operator \
  --reason integration-guide-rollout
```

```text
created source "grok-demo" (kind "grok", tenant "11111111-1111-4111-8111-111111111111", enabled true)
  kind: - → grok
  tenant: - → 11111111-1111-4111-8111-111111111111
  poll_seconds: - → 60
  enabled: - → true
→ reload a running engine to apply: POST /v1/console/runtime/reload, or `kill -HUP <pid>` (it also applies at next boot)
```

Stoppen Sie bei SQLite die Engine vor einer Offline-Roster-Änderung oder verwenden Sie die
Live-Konsole. Bei PostgreSQL kann der Befehl parallel zur Engine laufen. `--actor` und `--reason`
ordnen die Änderung der Datenherkunft zu.

Fügen Sie für vom Standard abweichende Pfade explizite Konfigurationswerte hinzu:

```sh
olivares sources set \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --config config_path=/srv/grok-home/.grok/config.toml \
  --config requirements_path=/etc/grok/requirements.toml \
  --config disabled_hooks_path=/srv/grok-home/.grok/disabled-hooks \
  --config managed_settings_path=/etc/claude-code/managed-settings.json \
  --actor platform-operator \
  --reason grok-paths-for-service-user
```

### Konnektivitätstest und tatsächliches Lesen der Dateien

Die reproduzierbare Messung auf dem Screenshot-Host am 30. August 2026 ergab folgendes Ergebnis:

```sh
olivares sources test \
  --data-dir /var/lib/olivares \
  --name grok-demo \
  --timeout 20s
```

```text
configuration: VALID (everything that can be decided without the network)
source "grok-demo" (grok): ANSWERED — the connector opened with this configuration and was closed again
NO SOURCE ROW WAS WRITTEN and nothing was wired into a running engine.
```

Der Prozess wurde mit Code `0` beendet. Auf diesem Host lief eine Grok-Session und
`~/.grok/config.toml` war vorhanden; `/etc/grok/requirements.toml` und
`~/.grok/disabled-hooks` fehlten. `sources test` las keine dieser Dateien: `Open` löst nur die
Konfiguration auf, und `test` schließt sofort, ohne `Gather` aufzurufen. Daher beweist `ANSWERED`
weder die Session noch die Sandbox oder Findings. Um das Lesen der Dateien zu testen, laden Sie
die Engine neu und prüfen Sie die vom nächsten Poll ausgegebenen Findings.

### Fail-closed-Verhalten des Hook-Clients verifizieren

Wenn der Endpoint nicht konfiguriert ist:

```sh
printf '%s' '{"hookEventName":"pre_tool_use","sessionId":"s-1","toolName":"Bash"}' | olivares grok-hook
```

Standardausgabe:

```json
{"decision":"deny","reason":"no governance endpoint is configured (deny-closed)"}
```

Standardfehler:

```text
no governance endpoint is configured (deny-closed)
```

Der Exit-Code ist `2`, was Grok für `pre_tool_use` als Veto interpretiert. Bei anderen Events wird
eine Ablehnung aufgezeichnet, kann die Aktion aber nicht verhindern; der Client meldet dies auf
stderr, statt Enforcement zu behaupten.

## Control Console

| Ort | Anzeige | Betriebseinschränkung |
|---|---|---|
| **Control console > Connectors** (`/console`) | `grok`-Roster, konfigurierte Pfade, Intervall, Modus sowie Test/Save/Reload-Aktionen. | Der Test öffnet und schließt den Connector; er liest die TOML-Dateien nicht. |
| **Health > Connectors** (`/health`) | Quellenstatus, Meldung, Trend und letzter Poll. | Prozesszustand beweist nicht, dass eine fehlende Datei unter Governance steht. |
| **Observability > Ingestion** (`/observability`) | Von `olivares.grok` ausgegebene Findings, erster/letzter Datensatz und bei Aktivierung aggregierte OTLP-Aktivität. | Prozessweite Zähler seit dem Start; sie werden zurückgesetzt und sind nicht Tenant-spezifisch. |
| **Security** (`/security`) | Beobachtetes und durchgesetztes Sandbox-Profil, MCP-Namen, Vorhandensein/Gültigkeit der Anforderungen, Managed-Settings-Kompatibilität und deaktivierte Hook-Namen. | „Nicht lesbar“ bleibt unbekannt und wird nicht zu abwesend. |
| **Sessions** (`/sessions`) | Session, Aktion, Identität, Berechtigungsmodus, letzte Aktivität und Haltung `enforced` oder `observed`. | Erfordert Hook-Events. Lokales Inventar erzeugt keine Session. |
| **Audit** (`/audit`) | Zuordenbare PEP-Entscheidungen und verkettete Evidenz. | Existiert nur für Aufrufe, die den PEP erreichten; ein deaktivierter Hook hinterlässt eine Lücke. |

Erwarten Sie keinen Modellkatalog, keine xAI-Ausgaben und keine Prompts: Diese Quelle verwendet
die xAI API nicht, und der OTLP-Receiver verwirft Inhalte.

<img class="light:sl-hidden" src="/console/observability-counters-dark.png" alt="Standardbasierte Erfassungszustände und Hauptbuch-korrelierte Trace-Detailanalyse. Die Kennzahlen gelten engine-weit (prozessglobal), nicht pro Mandant; Standards sind auf die Versionen und Reifegrade festgelegt, die die zuständigen Gremien deklarieren.">
<img class="dark:sl-hidden" src="/console/observability-counters-light.png" alt="Standardbasierte Erfassungszustände und Hauptbuch-korrelierte Trace-Detailanalyse. Die Kennzahlen gelten engine-weit (prozessglobal), nicht pro Mandant; Standards sind auf die Versionen und Reifegrade festgelegt, die die zuständigen Gremien deklarieren.">
<img class="light:sl-hidden" src="/console/security-dark.png" alt="Befunde der Schutzleitplanken, die Durchsetzungslage, die Anomalie-Warteschlange und manipulationssichere Vorfallforensik. Die Ebene ist standardmäßig detektivisch — sie zeichnet auf, sie blockiert nicht von sich aus, sofern die Durchsetzung nicht aktiviert und gesteuert ist.">
<img class="dark:sl-hidden" src="/console/security-light.png" alt="Befunde der Schutzleitplanken, die Durchsetzungslage, die Anomalie-Warteschlange und manipulationssichere Vorfallforensik. Die Ebene ist standardmäßig detektivisch — sie zeichnet auf, sie blockiert nicht von sich aus, sofern die Durchsetzung nicht aktiviert und gesteuert ist.">

## Produktionseinsatz

- **Linux-Endpoint-Baselines:** Verteilen Sie `requirements.toml` als Root-eigene Datei und pollen
  Sie jeden Host. Abwesenheit wird zu einem verwertbaren Finding und nicht zu einem grünen Standard.
- **MCP-Kontrolle:** Vergleichen Sie benutzerdefinierte Namen mit den vom Administrator fixierten
  Namen. Die Variable `GROK_CONFIG` kann keine sensiblen Tabellen wie MCP, Authentifizierung oder
  Egress hinzufügen; dieser Schutz stammt von Grok, und Olivares meldet ihn, ohne ihn zu duplizieren.
- **Hook-Canary:** Beginnen Sie mit einem harmlosen Tool und bestätigen Sie Event, Entscheidung und
  Wirkung. Überwachen Sie anschließend `disabled-hooks` kontinuierlich, da die Kontrolle per Name
  verschwinden kann.
- **Gemeinsam verwendete Endpoints:** Konfigurieren Sie absolute Pfade zum tatsächlichen `HOME` des
  Kontos, das Grok ausführt. Das `~` des Olivares-Dienstes kann auf einen anderen Benutzer
  aufgelöst werden und eine korrekte Messung des falschen Hostprofils erzeugen.
- **Minimale Telemetrie:** Aktivieren Sie OTLP nur, wenn das aggregierte Signal erforderlich ist,
  und reservieren Sie einen eigenen lokalen Socket. Priorisieren Sie für präventive Governance die
  zuverlässige Ausführung des Hooks.

## Was durchgesetzt und was nur beobachtet wird

| Oberfläche | Tatsächliches Verhalten |
|---|---|
| `grok`-Quelle | **Beobachtet, schreibgeschützt.** Liest Dateien und gibt Findings aus; ändert Grok Build nicht und ruft xAI nicht auf. |
| `/etc/grok/requirements.toml` | **Setzt im Agent** die eingeschränkten Sandbox- und MCP-Werte durch. Olivares verifiziert Vorhandensein und deklarierte Wirkung. |
| `~/.grok/config.toml` | **Beobachtete Präferenz.** Für sich allein keine administrative Policy. |
| `olivares grok-hook` bei `pre_tool_use` | **Kann das Tool verhindern**, wenn der Befehl läuft und mit `2` beendet wird. Der Client lehnt nach dem deny-closed-Prinzip ab, wenn der PEP fehlt oder fehlschlägt. |
| Andere Grok-Events | **Beobachtet.** Die Ablehnung bleibt als Evidenz erhalten, aber das Event bietet kein entsprechendes Veto. |
| Timeout, Absturz oder ein Hook, der nie läuft | **Der Agent schlägt offen fehl.** Grok fährt fort; das interne fail-closed-Verhalten von `olivares grok-hook` greift nur, wenn der Prozess aufgerufen wird. |
| `~/.grok/disabled-hooks` | **Kann sogar einen verwalteten Hook deaktivieren.** Olivares erkennt dies im Nachhinein, aber keine Anforderungsebene verhindert es. |
| OTLP-Receiver | **Beobachtet Aggregate.** Authentifiziert nicht, speichert keine Inhalte und ersetzt nicht die Hook-Timeline. |

Eine Bereitstellung darf nicht allein deshalb als „enforced“ bezeichnet werden, weil die Sandbox
fixiert ist. Der Abschluss erfordert wirksame Anforderungen, einen tatsächlich laufenden Hook,
kontinuierliche Überwachung auf seine Abwesenheit in `disabled-hooks`, ein sichtbares Event und
ein nachgewiesenes `pre_tool_use`-Veto.
