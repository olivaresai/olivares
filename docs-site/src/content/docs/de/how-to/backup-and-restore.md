---
title: "Sichern und wiederherstellen (DR, die sich selbst beweist)"
description: >-
  Verschlüsselte, ledger-kontinuitätssichere Backups mit olivares dr: geplante
  Bundles für SQLite und Postgres, die Wiederherstellung, die die Kette
  verifiziert, der Drill, den Sie ausführen können, ohne die Produktion zu
  berühren — und die zwei Schlüssel, die entscheiden, ob Ihre Nachweise
  überleben.
---

Das Backup einer Control Plane hat eine schwierigere Aufgabe als die meisten: es
muss mit seinem **manipulationserkennbaren Ledger nachweislich intakt**
zurückkehren. `olivares dr` ist um diese Anforderung herum gebaut — jedes Bundle
zeichnet pro-Tenant-Kettenspitzen auf, die Wiederherstellung **schlägt mit
ungleich null fehl, wenn das wiederhergestellte Ledger nicht
kontinuitätssicher ist**, und der Drill-Unterbefehl beweist, dass ein Bundle
wiederherstellbar ist, ohne die Produktion zu berühren.

Das Bundle wird unter einem **KEK verschlüsselt, den Sie bereitstellen** — einer
Argon2id-abgeleiteten Passphrase (`--passphrase-file`) oder einem rohen 32-Byte-
Schlüssel aus Ihrem KMS (`--kek-key-file`); genau einer ist erforderlich. Die
Audit- und Katalog-Signierschlüssel reisen **versiegelt** innerhalb des Bundles.

## Sichern

**SQLite** (Single-Node) — sicher, während `serve` läuft (der Snapshot verwendet
`VACUUM INTO`; WAL erlaubt den gleichzeitigen Lesezugriff):

```bash
olivares dr backup \
  --data-dir /var/lib/olivares --engine sqlite \
  --out /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

**Postgres** — ein konsistentes `pg_dump --format=custom`, gesteuert durch
denselben Befehl (`--engine postgres --dsn … --admin-dsn …`), oder Sie übergeben
einen vorgefertigten Dump mit `--snapshot-file`. Den Dump direkt auszuführen
**erfordert `--admin-dsn`**: `pg_dump` hält `row_security=off` und **bricht als
Anwendungsrolle ab**, sobald es Tabellen mit `FORCE ROW LEVEL SECURITY` erreicht.
Der Befehl **weist den Lauf deshalb von vornherein zurück**, statt ihn starten zu
lassen und am Ende nichts zu produzieren. Für nahezu null RPO erzeugt
`--pitr-ref` ein Keys+Manifest-Begleitbundle, das zu Ihrem WAL-archivierenden
PITR-Setup passt (`deploy/postgres/backup/pitr-setup.md`); die Wrapper-Skripte
`deploy/postgres/backup/pg-dump.sh` / `pg-restore.sh` paketieren denselben
Ablauf.

Zwei Ehrlichkeitsschalter, die man kennen sollte:

- Das Backup **verweigert die Erfassung eines Ledgers, das zum
  Backup-Zeitpunkt nicht verifiziert** — `--allow-unverified` existiert, wird
  protokolliert und ist nicht empfohlen.
- Bei einem **vorgefertigten Snapshot** (`--snapshot-file` / `--pitr-ref`) ohne
  `--admin-dsn` warnt das Backup, dass die erfasste Tenant-Menge RLS-begrenzt und
  **unvollständig** sein kann — der Dump selbst ist korrekt; die Admin-Rolle wird
  für das **mandantenübergreifende Inventar des Manifests** benötigt. (`pg_dump`
  **direkt** auszuführen ist ein anderer Fall: Das wird rundweg zurückgewiesen,
  siehe oben.)

**Zeitplanung:** der Compose-Stack liefert ein
[Backup-Profil](/de/tutorials/getting-started/docker-compose/#3-verschlüsselte-dr-backups-das-backup-profil),
das Helm-Chart einen
[CronJob](/de/tutorials/getting-started/kubernetes/#4-geplante-verschlüsselte-backups);
auf Bare Metal cronen Sie den obigen Befehl. Ihr Zeitplan **ist** Ihr RPO:

| Stufe | Mechanismus | RPO | RTO |
|---|---|---|---|
| SQLite | `dr backup` per cron | das cron-Intervall | < 15 Min |
| Postgres logisch | `pg-dump.sh` per cron | das cron-Intervall | < 30 Min |
| Postgres PITR | Base-Backup + WAL-Archivierung | ≈ Sekunden | < 30 Min |

Spiegeln Sie Bundles **offsite** und halten Sie den KEK **getrennt von den
Bundles** (3-2-1): ein Backup auf demselben Host ist kein Disaster Recovery, und
ein Bundle, das mit seiner Passphrase reist, ist in keinem relevanten Sinne
verschlüsselt.

## Drill — bevor Sie ihn brauchen

`dr verify` beweist, dass ein Bundle wiederherstellbar ist, **ohne Ihr
Data-Dir zu berühren** (SQLite: vollständige Kettenverifizierung in einem
Scratch-Verzeichnis; beendet mit ungleich null, falls unsicher):

```bash
olivares dr verify --in /backups/olivares-dr-<ts>.drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

`dr inspect --in <bundle>` gibt das Manifest aus (kein KEK nötig, keine Secrets
gezeigt) — welche Engine, welche Tenants, welche Kettenspitzen. Führen Sie den
Drill in derselben Kadenz wie das Backup aus; ein nicht verifiziertes Backup ist
eine Hoffnung, keine Kontrolle.

## Wiederherstellen

```bash
olivares dr restore --in /backups/olivares-dr-<ts>.drbundle \
  --data-dir /var/lib/olivares --engine sqlite \
  --passphrase-file <your-dr-passphrase-file>
```

Die Wiederherstellungssequenz ist bewusst gewählt: zuerst die Signierschlüssel
(fail-closed beim Überschreiben — `--force` ist die explizite Überschreibung),
dann der Store-Snapshot, dann **bootet sie den wiederhergestellten Store und
beweist die Ledger-Kontinuität**, mit Beendigung ungleich null, falls die Kette
nicht sicher ist. Verifizieren Sie nach jeder Wiederherstellung erneut gegen
Ihren **off-box** Checkpoint-Pin — ein wiederhergestellter älterer Snapshot kann
einen naiven Walk bestehen, aber den Off-Box-Vergleich nicht
([Fehlerbehebung § Ledger](/de/how-to/troubleshooting/#das-ledger-besteht-die-verifizierung-nicht)).

## Die zwei Schlüssel, die alles entscheiden

| Schlüssel | Regel |
|---|---|
| **Der DR-KEK** (Passphrase oder roher Schlüssel) | ohne ihn ist jedes Bundle Rauschen. Speichern Sie ihn in einem anderen System als die Bundles; beide gleichzeitig zu verlieren ist der Fehlerfall |
| **`audit-signing.key`** (im Data-Dir) | sichern Sie ihn beim Provisioning off-box — die Engine **warnt** nur beim ersten Boot, es gibt keinen erzwungenen Escrow, und ein verlorener Schlüssel macht das Ledger dauerhaft nicht verifizierbar. Pinnen Sie auch den Public Key off-box (`GET /v1/audit/pubkey`) |

Für die KMS-basierte Verwahrung der Signierschlüssel selbst (BYOK-Envelopes,
Rotationszeremonien, `olivares keys`) siehe die
[CLI-Referenz](/de/reference/cli/); für die Durchläufe der Fehlerfälle die
[Seite zur Fehlerbehebung](/de/how-to/troubleshooting/).
