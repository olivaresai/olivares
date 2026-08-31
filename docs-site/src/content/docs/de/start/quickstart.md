---
title: Schnellstart
description: >-
  Von null zu einer befüllten Read/Write-Zugriffsgraph mit einem echten
  Permitted-vs-Observed-Drift-Ergebnis in etwa fünf Minuten — zuerst auf der
  mitgelieferten Demo-Estate, dann auf einem echten pgAudit-Connector, um zu
  beweisen, dass es keine Demo ist.
---

Dies ist der schnelle Weg, um zu sehen, *wofür* Olivares AI gedacht ist: eine
**Read/Write-Zugriffskarte** Ihres Estate und der **Permitted-vs-Observed-Drift**
darüber — die Lücke zwischen dem Zugriff, der einem Agenten *gewährt* wird, und
dem Zugriff, der ihn *beobachtet* nutzt.

Sie erreichen dieses Ergebnis zweimal, in insgesamt etwa fünf Minuten:

1. **In einer Minute, auf der mitgelieferten Demo-Estate** — der sofortige
   "Wie sieht das überhaupt aus"-Einstieg (synthetische Beobachtungen, die durch
   die echte Engine fließen).
2. **Dann auf einem echten Connector** — derselbe Graph und Drift, diesmal
   wortgetreu aus einem PostgreSQL-**pgAudit**-Log geparst, um zu beweisen, dass
   der Hero auf echten Daten läuft, nicht auf einer Demo.

Jeder der untenstehenden Befehle wird exakt wie geschrieben von
`scripts/quickstart-smoke.sh` ausgeführt
([Reproduzierbarkeit](#5-reproduzieren-sie-dies-selbst)) — diese Seite kann also
nicht stillschweigend vom Binary abweichen.

Es ist ein Lernpfad, kein Produktiv-Deployment. Für die echte Installation (keine
Standard-Anmeldedaten, ein einmaliges Setup-Token, TLS) gehen Sie zu
[Self-Hosting](/de/how-to/self-hosting/). Für eine geführte UI-Durchsicht siehe das
[Zero-to-Graph-Tutorial](/de/tutorials/zero-to-graph/).

:::caution[Der Demo-Modus dient ausschließlich dem Lernen]
`--seed-demo` stellt einen Demo-Administrator mit einem **öffentlichen Passwort
aus dem Quellbaum** und synthetischen Daten bereit und **verweigert den Start auf
einer Nicht-Loopback-Adresse**. Verwenden Sie ihn niemals für eine echte
Installation — der echte Erstinstallationspfad ist Schritt 3 unten und in
[Self-Hosting](/de/how-to/self-hosting/).
:::

## 1. Das einzelne Binary bauen

Aus einem Checkout des Repositorys (benötigt Go 1.26+, [Task](https://taskfile.dev)
und pnpm — `task build` bündelt die Web-UI vor dem Kompilieren; der Store ist
reines Go-SQLite, also keine C-Toolchain nötig):

```bash
task build                      # compiles ./bin/olivares with the web UI embedded
./bin/olivares version
```

`task build` erzeugt ein einziges eigenständiges Artefakt unter `./bin/olivares`
— die Engine, die eingebettete Web-UI und die First-Party-Connector-Plugins. Die
**Container- und Kubernetes-Installationen umhüllen genau dieses Binary**: ein
veröffentlichtes Image plus eine Compose-Datei ([Self-Hosting](/de/how-to/self-hosting/)),
oder ein flaches Manifest, das Sie mit `kubectl apply -f deploy/manifests/install.yaml`
anwenden (kein Helm erforderlich). Der Hero, den Sie unten sehen, ist auf allen
dreien identisch — nur der Demo-Seed unterscheidet sich (nur Loopback, niemals in
einer echten Installation).

## 2. Die Demo-Estate booten (nur Loopback)

```bash
DATA="$(mktemp -d)"
./bin/olivares serve --insecure --seed-demo \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$DATA"
```

`--insecure` liefert Klartext-HTTP über Loopback aus (in Ordnung für eine lokale
Demo; **TLS ist sonst standardmäßig aktiviert**). Sie sehen ehrliche
`WARN`-Zeilen für die Nahtstellen, die ab Werk deny-closed sind (kein Judge, kein
Embedder, kein Approval-Gate, keine echten Quellen), dann ein **DEMO MODE**-Banner
mit den Anmeldedaten:

```text
demo@olivares.local / olivares-demo-estate
```

Die synthetische Estate fließt durch denselben **echten** Event-Bus, wie es ein
Live-pgAudit- oder OpenTelemetry-Collector tun würde — nur die Beobachtungen sind
geseedet.

## 3. Den Zugriffsgraphen und seinen Drift erreichen (der Hero)

Lassen Sie den Server laufen; melden Sie sich in einem zweiten Terminal an, lösen
Sie den Demo-Mandanten auf und rufen Sie den Graphen und seinen Drift ab:

```bash
BASE=http://127.0.0.1:8901
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"demo@olivares.local","password":"olivares-demo-estate"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;[print(o["tenant_id"]) for o in json.load(sys.stdin)["items"] if o["slug"]=="demo"]')"

# The read/write access map — module III:
curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool

# The Permitted-vs-Observed drift:
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

Die Demo-Estate liefert genau **20 Knoten und 13 Kanten** zurück, und der Drift
deckt **8 unerwartete Zugriffe** und **2 ungenutzte Grants** auf. Jede Kante trägt
die Ehrlichkeitsachsen des Produkts, sodass Sie jeden Befund ohne Raten lesen
können:

- **`mode`** — `read` / `write` / `readwrite` / `unknown`: die R/W-Klassifizierung,
  wortgetreu aus dem Signal übernommen, niemals abgeleitet.
- **`attribution_tier`** — `firm` / `approximate` / `unknown`: wie fest der Zugriff
  an eine *bestimmte* Agenten- oder Workload-Identität gebunden ist. In der Demo
  sind **6 Kanten firm und 7 approximate** — z. B. ein Agent, der eine Ressource
  liest, die ihm nie gewährt wurde (`appdb.public.secrets`, *firm*), gegenüber
  einer Shared-Pool-Identität, die Logs schreibt (`appdb.public.logs`, ehrlich
  *approximate*).
- **`coverage_tier`** — `clean` / `lossy` / `opaque` / `mixed`: die Genauigkeit des
  Signals der *Ressource*, orthogonal zur Attribution.

:::tip[Eine zentrale differenzierende Fähigkeit]
Der **Diff zwischen Permitted und Observed** ist *Least-Privilege-Drift* — das,
was Sie finden wollen, bevor es ein Auditor oder ein Angreifer tut. Der Seed
beweist, dass er echt ist, nicht "alles ist Drift": die 3 gewährten **und**
beobachteten Kanten gleichen sich ab und fallen aus dem Drift-Ergebnis heraus; nur
echte Lücken bleiben (8 unerwartete Zugriffe + 2 Grants, die deklariert, aber nie
genutzt werden). Und das Produkt erfindet niemals ein Label, das es nicht beweisen
kann — eine Attribution, die lediglich `approximate` ist, sagt das auch, statt
einen `firm`-Agenten zu erfinden.
:::

Derselbe Graph wird in der eingebetteten Web-UI unter `http://127.0.0.1:8901`
dargestellt (melden Sie sich mit den Demo-Anmeldedaten an und wechseln Sie zur
Organisation **Demo Estate**).

Stoppen Sie den Demo-Server (`Ctrl-C`) vor dem nächsten Schritt.

## 4. Beweisen Sie es auf einem echten Connector (keine Demo)

Der Hero ist keine geseedete Magie: er läuft auf dem, was auch immer Ihre Quellen
beobachten. Hier verdrahten Sie den **echten pgAudit-Connector** — denselben
Codepfad, den eine Produktivinstallation nutzt — gegen ein PostgreSQL-Audit-Log,
mit **keinem Demo-Seed**.

Zunächst ein kleines `pgAudit`-csvlog (drei echte Audit-Zeilen: zwei Reads und ein
Write durch eine Anwendung). In der Produktion schreibt pgAudit diese in das
Postgres-Log; hier steht eine Datei für dieses Tail:

```bash
WORK="$(mktemp -d)"
python3 - "$WORK/postgresql.csv" <<'PY'
import csv, sys
def row(ts, user, db, msg, app):
    r = [''] * 26
    r[0], r[1], r[2] = ts, user, db
    r[11] = 'LOG'; r[13] = msg; r[22] = app; r[23] = 'client backend'
    return r
rows = [
    row("2026-06-09 09:00:01.001 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers", "billing-agent"),
    row("2026-06-09 09:00:02.002 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders", "billing-agent"),
    row("2026-06-09 09:00:03.003 UTC", "claude_rw", "salesdb", "AUDIT: SESSION,3,1,READ,SELECT,TABLE,public.secrets", "billing-agent"),
]
with open(sys.argv[1], 'w', newline='') as f:
    csv.writer(f).writerows(rows)
PY
```

Führen Sie nun einen **echten Erststart** durch: booten Sie einmal ohne
Standard-Anmeldedaten, beanspruchen Sie das einmalige Setup-Token und erstellen
Sie einen Mandanten, an den der Connector angehängt wird.

```bash
BASE=http://127.0.0.1:8901
./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$WORK/data" > "$WORK/server.log" 2>&1 &
SERVER=$!
sleep 2

# The one-time setup token is printed to stdout on first boot (look for `olst_…` on the
# server's console, or read it from the redirected log):
SETUP="$(grep -oE 'olst_[A-Z0-9]+' "$WORK/server.log" | head -1)"

curl -sf -X POST "$BASE/v1/setup" -H 'Content-Type: application/json' \
  -d "{\"token\":\"$SETUP\",\"email\":\"admin@local\",\"password\":\"correct-horse-battery-staple\"}"

TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

TENANT="$(curl -sf -X POST "$BASE/v1/system/orgs" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Production","slug":"prod"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["tenant_id"])')"
echo "tenant: $TENANT"

kill "$SERVER"                  # stop the first-run server; we restart it with pgAudit wired
```

Connectors werden aus einer einzigen Operator-Konfigurationsdatei verdrahtet, per
Wert, niemals von der Engine persistiert. Richten Sie pgAudit auf das Log für Ihren
Mandanten aus und **starten Sie neu** mit der Konfiguration:

```bash
cat > "$WORK/sources.json" <<JSON
{"sources":[{"name":"salesdb-pgaudit","kind":"pgaudit","tenant":"$TENANT",
  "config":{"log_path":"$WORK/postgresql.csv","format":"csvlog"}}]}
JSON

OLIVARES_SOURCES_CONFIG="$WORK/sources.json" ./bin/olivares serve --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$WORK/data"
```

Das Boot-Log gibt `ingest: wired source … kind=pgaudit` aus. Melden Sie sich in
einem zweiten Terminal erneut an und lesen Sie den Graphen — diesmal sind die
Kanten **wirklich geparst**, nicht geseedet:

```bash
TOKEN="$(curl -sf -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"email":"admin@local","password":"correct-horse-battery-staple"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')"

curl -sf "$BASE/v1/m/accessmap/graph?limit=200" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
curl -sf "$BASE/v1/m/accessmap/drift" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" | python3 -m json.tool
```

Sie erhalten **3 Kanten** — `salesdb.public.customers` (read), `…orders` (write),
`…secrets` (read) — jede mit `signal_source: pg_audit` und `coverage_tier: clean`
(pgAudit meldet R/W wortgetreu), und der Drift markiert alle **3 als unerwartete
Zugriffe** (noch ist kein Grant verdrahtet, also ist jeder beobachtete Zugriff
Drift).

:::note[Ehrlich per Default: approximate, bis Sie Identität verdrahten]
Diese echten Kanten landen als `attribution_tier: approximate`, nicht `firm` — das
pgAudit-Signal benennt eine Datenbank-Rolle/Anwendung, keinen *governten Agenten*.
Das ist der ehrliche Default: das Produkt wird nicht behaupten, dass es einen
Zugriff fest einem Agenten zugeschrieben hat, den es nicht beweisen kann. Sie
verdienen sich `firm`, indem Sie eine Identitätsquelle (LDAP/IdP/SPIFFE)
verdrahten, die die Anmeldedaten an eine Agenten- oder Workload-Identität bindet —
siehe [Eine Quelle anbinden](/de/how-to/connect-a-source/). Die Demo-Estate zeigt
`firm`-Kanten gerade deshalb, weil sie ihre Agenten vorab bindet.
:::

:::note[Die Form des Endpunkts]
Das Permitted-vs-Observed-Ergebnis wird unter `/v1/m/accessmap/drift` ausgeliefert
(es gibt kein `/diff`). Die `/v1/m/accessmap/*`-Routen gehören nicht zum stabilen
Kernvertrag mit 53 Pfaden; sie werden als separates **Beta**-Dokument in der
[Modulrouten-Referenz](/reference/api-beta/) veröffentlicht. Die
[API-Referenz](/reference/api/) dokumentiert die stabile Kernfläche.
:::

## 5. Reproduzieren Sie dies selbst

Alles oben Stehende wird von Anfang bis Ende gegen das echte Binary überprüft:

```bash
task smoke:quickstart          # or: scripts/quickstart-smoke.sh
```

Es bootet die Demo-Estate **und** den echten pgAudit-Pfad, führt die exakten
Befehle dieser Seite aus und überprüft die Zahlen (20 Knoten / 13 Kanten, 8
unerwartet + 2 ungenutzt, 3 echte pgAudit-Kanten). Wenn der Pfad
Installation→Wert oder das Drift-Ergebnis je aufhört, wahr zu sein, schlägt der
Smoke fehl — das ist der Vertrag, der diese Seite ehrlich hält. Er ist in wenigen
Sekunden Wandzeit abgeschlossen; der oben von Hand durchlaufene Pfad sind die
dokumentierten **fünf Minuten**.

## Nächste Schritte

- **Setzen Sie es echt ein:** die Getting-Started-Tutorials durchlaufen jedes
  Installationsszenario von Anfang bis Ende —
  [Single Node (systemd)](/de/tutorials/getting-started/single-node/),
  [Docker Compose](/de/tutorials/getting-started/docker-compose/),
  [Kubernetes/Helm](/de/tutorials/getting-started/kubernetes/) und
  [Air-Gapped](/de/tutorials/getting-started/air-gapped/);
  [Self-Hosting](/de/how-to/self-hosting/) ist die Entscheidungsseite über alle
  hinweg.
- **Füttern Sie es mit echten Signalen:** [Eine Quelle anbinden](/de/how-to/connect-a-source/)
  und der [Connector-Katalog](/de/reference/connectors/) — was jede Quelle beobachtet,
  ihr ehrliches Coverage-Tier und wie man Identität verdrahtet, damit die
  Attribution `firm` wird.
- **Härten Sie es:** [Security-Hardening](/de/how-to/security-hardening/) — sichere
  Defaults, Human-in-the-Loop-Freigaben und das Verifizieren eines Release, bevor
  Sie es ausführen.
- **Kennen Sie die Grenzen:** [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) —
  was heute läuft, was im Design-Stadium ist und was das Produkt bewusst nicht tut.
