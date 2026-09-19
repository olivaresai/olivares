---
title: "Ihre erste Stunde mit Olivares AI"
description: >-
  Installieren Sie die Control Plane, öffnen Sie die Konsole, verbinden Sie
  einen Coding-Agenten, führen Sie eine gesteuerte Sitzung aus, die eine Aktion
  zulässt und eine andere verweigert, und lesen Sie die Evidenz. Drei Formen:
  lokal (SQLite), Team (Postgres und Docker), hybrid.
---

Diese Seite ist die erste Stunde **in diesem Baum**. Sie ist kein Mockup. Jeder
Befehl der lokalen Form ist der Befehl, den `task smoke:first-hour` gegen
`./bin/olivares` ausführt. Wo eine Form Docker oder Postgres braucht, sagt die
Seite das.

Das Produkt druckt den nächsten Schritt. `olivares quickstart` nennt
Passkey-Registrierung, `olivares agent tool detect`, Inventar, Hook-PEP und
`olivares doctor` nach dem Setup-Token. `olivares doctor` berichtet
`first-hour-coding-agent`, `first-hour-hook-pep` und `first-hour-next-step`.
Diese Prüfungen sind optional. Sie lassen eine gesunde Installation nicht
fehlschlagen.

## Was diese Stunde ist

Installieren → Konsole → **einen** Coding-Agenten verbinden → im Inventar
sehen → gesteuerte Sitzung, die **eine Aktion zulässt und eine andere
verweigert** → Evidenz lesen.

Diese Seite nutzt das **Claude-Code-Hook-PEP**, das dieser Baum bereits
liefert (`olivares claude-hook`). Das Starten eines offiziellen CLI als
Sitzungsprozess ist die Naht der Community-Laufzeit. Diese Stunde dupliziert diesen
Treiber nicht. Sie sendet keinen Modellzug.

`--seed-demo` ist nicht diese Stunde.

## Form 1 — Lokal (SQLite, diese Box)

Dieser Container hat **kein Docker**. PostgreSQL **läuft hier nicht**. Die
lokale Form nutzt eingebettetes SQLite und Loopback-HTTP. Das ist die Form,
die der Smoke-Test wiederholt.

### 1. Bauen und starten

```bash
task build
./bin/olivares version
DATA="$(mktemp -d)"
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8443 --grpc-listen 127.0.0.1:8444 \
  --data-dir "$DATA"
```

Der Smoke-Test nutzt `serve --insecure`, damit `curl` kein selbstsigniertes
Zertifikat vertrauen muss, und übergibt `--listen 127.0.0.1:8443`
ausdrücklich: dieser Loopback-Bind gehört dem Smoke-Test, nicht der
Voreinstellung des Produkts. Ein interaktiver Operator führt
`olivares quickstart` aus (TLS aktiv, keine Standardanmeldedaten, ein einmalig
verwendbares Setup-Token). Dessen voreingestellte Listen-Adresse ist `:8443` —
**alle Schnittstellen**, denn dies ist ein Server
(`cmd/olivares/binddefaults.go`); mit `--listen 127.0.0.1:8443` beschränken Sie
sie auf diese Maschine. Gemessen auf dieser Box am 2026-09-17 druckte
`quickstart --quiet` das Token in **3 s**.

Das Willkommenspanel druckt:

```text
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

Für den voreingestellten Wildcard-Bind gibt das Banner
`https://localhost:8443` aus und listet unter dem Token jede weitere Adresse
auf, unter der dieser Host antwortet — diese sind für den Zugriff von einer
anderen Maschine. Ist das Banner weggescrollt, gibt `olivares first-boot` die
Konsolenadresse(n) und den Stand des ersten Setups erneut aus. Öffnen Sie
`https://localhost:PORT`, bevor Sie einen Passkey registrieren: der Browser
lehnt eine IP als WebAuthn Relying Party ab, und das Produkt sagt das bereits
im Adresshinweis (`cmd/olivares/consoleaddr.go`).

### 2. Administrator und Tenant anlegen

Ein einziger Befehl schließt die Einrichtung gegen die laufende Engine ab – derselbe,
den das Startpanel ausgibt. Das Token wird von der Standardeingabe gelesen, damit es
nie in der Prozessliste erscheint:

```bash
# füge das olst_… Token ein, das die Engine beim Start ausgegeben hat
olivares auth bootstrap --server https://127.0.0.1:8443 \
  --ca-cert <data-dir>/tls.crt \
  --setup-token-file - \
  --email admin@local --password-file ./admin.pw \
  --organization "First hour" --save-context
```

`--save-context` meldet sich an und speichert die Sitzung, sodass der nächste Befehl
bereits authentifiziert ist. Gemessen am 2026-09-18 auf einem leeren Datenverzeichnis:
263 ms.

Dasselbe über die API, wenn Sie direkt gegen die Endpunkte skripten:

```bash
BASE=http://127.0.0.1:8443
curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d '{"token":"olst_…","email":"admin@local","password":"correct-horse-battery-staple"}'
TOKEN=$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')
TENANT=$(curl -sf -X POST "$BASE/v1/system/orgs" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"First hour","slug":"first-hour"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')
```

### 3. Einen Coding-Agenten verbinden und im Inventar sehen

```bash
./bin/olivares agent tool detect -o json
curl -sf -X POST "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -H 'Content-Type: application/json' \
  -d '{"name":"claude-code-local","kind":"claude-code"}'
curl -sf "$BASE/v1/agents" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares agent managed-settings --out ./managed-settings.json
```

`POST /v1/agents` verlangt **kein** AAL3. Quellen, Konnektoren, Workspaces
und Geheimnisse **schon**.

### 4. Gesteuerte Sitzung: Read zulassen, Bash verweigern

Starten Sie mit `OLIVARES_HOOK_PEP_CONFIG` und einer deny-closed Policy neu.
Dann:

```bash
export OLIVARES_HOOK_PEP_URL=http://127.0.0.1:8447/
export OLIVARES_HOOK_PEP_TOKEN="$TOKEN"
export OLIVARES_HOOK_PEP_TENANT="$TENANT"
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/repo/README.md"}}' \
  | ./bin/olivares claude-hook
printf '%s\n' '{"session_id":"sess-first-hour","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf /"}}' \
  | ./bin/olivares claude-hook
```

### 5. Evidenz lesen

```bash
curl -sf "$BASE/v1/audit?action=hook.tool.allow&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
curl -sf "$BASE/v1/audit?action=hook.tool.deny&limit=100" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT"
./bin/olivares doctor --mode user
task smoke:first-hour
```

## Form 2 — Team (Postgres und Docker)

Dieser Container hat **kein Docker**. PostgreSQL **läuft hier nicht**.
Behandeln Sie die folgenden Befehle nicht als auf dieser Box gemessen.

```bash
cp deploy/compose/.env.example deploy/compose/.env
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up --wait --wait-timeout 120
```

Ohne den `olivares_admin`-Pool antwortet `POST /v1/setup` mit
`501 cross_tenant_admin_pool_not_configured`. Siehe
[Mit Docker bereitstellen](/how-to/docker-deployment/).

## Form 3 — Hybrid (lokale Control Plane, Agent auf einer Workstation)

Halten Sie die Control Plane auf lokalem SQLite. Führen Sie den Agenten auf
einer Workstation mit `claude` aus. Eine lebende offizielle CLI-Sitzung (PTY)
ist Sache der Community-Laufzeit, nicht dieser Seite.

## Die AAL3-Schranke (weiterhin wahr)

Das Anlegen von Quellen, Konnektoren, Workspaces und Geheimnissen wird bis
AAL3 **verweigert**. PIV/CAC antwortet auf einer Standardinstallation mit
**501**. Öffnen Sie `https://localhost:PORT`, dann **Identity → Privileged
login**.

## 3. Eine Claude-Code-Sitzung über die Konsole starten

Ohne Inferenz-Credential-Quelle sind stream-json-Starts deny-closed:

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

(`cmd/olivares/sessionruntime.go`). Setzen Sie **eine** von
`OLIVARES_SESSION_RUNTIME_WIF` oder `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`.
Seit v26.10 ist das nicht mehr der einzige Weg: Registrieren Sie die
Zugangsdaten in der Konsole und binden Sie sie an ein Profil. Siehe
[Anbieter hinzufügen und Agenten starten](/de/how-to/add-a-provider/) und
[Eine Anbietersitzung betreiben](/how-to/operate-provider-sessions/).

## Verwandte Themen

- [Ehrlichkeit und Grenzen](/start/honesty-and-limits/)
- [Olivares AI selbst hosten](/how-to/self-hosting/)
- [Eine Anbietersitzung betreiben](/how-to/operate-provider-sessions/)
- [Claude-Code-Hooks-PEP](/how-to/connectors/claude-code-hooks-pep/)
- [Mit Docker bereitstellen](/how-to/docker-deployment/)
