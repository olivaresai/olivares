> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0024: DDIL-Offline-Semantik je Ebene und ein einheitliches signiertes Bundle-Format

- **Status:** accepted
- **Date:** 2026-07-09
- **Deciders:** Fran Olivares (ratified the three questions below during design, 2026-07-09)
- **References:** the DDIL work brief; the OTA update framework
  (`core/release/manifest.go`); ADR-0009
  (hash-chained audit ledger); ADR-0013 (embedded Cedar PDP); ADR-0021 (durable
  JetStream bus, enterprise add-on); ADR-0022 (source scoping, forbid-overrides);
  the durable bus seam (`core/eventbus`, ADR-0021); break-glass.

## Kontext und Problemstellung

Olivares wird am taktischen / getrennten Edge eingesetzt (DoD DDIL: „erwartet, dass
Einheiten zumindest teilweise getrennt arbeiten … über air-gapped Netzwerke … und am
taktischen Edge“). Der Edge-Käufer verlangt nicht, dass wir „eine Satellitenverbindung
integrieren“ — ein pLEO-/Satelliten-Bearer ist lediglich intermittierendes IP, über das die
Anwendung unverändert läuft. Gefordert wird, dass Governance weiterarbeitet, wenn die
Verbindung für Stunden oder Tage ausfällt und in kurzen Fenstern zurückkehrt
(„Auftauchen eines U-Boots“).

Die Bausteine existieren bereits und wurden während der Erkundung verifiziert:

- Das **Audit-Ledger ist bereits ein dauerhafter, mandantenspezifischer, hash-verketteter,
  signierter lokaler Store** (`core/internal/store/sqlstore/audit.go`; ADR-0009). Eine
  Trennung erzeugt keine Lücke — sie hält lediglich den externen **Forward-Cursor**
  (`modules/siemforward`, angetrieben von der Eventing-Plattform) an. Es gibt keinen
  ausschließlich im RAM liegenden Audit-Puffer, der verloren gehen könnte.
- Der **PDP wertet gegen den LOKALEN Policy-Store aus** (eingebettetes Cedar, ADR-0013),
  daher funktioniert Policy bereits offline. Unentschieden ist die *Staleness*: Wie lange
  darf ein getrennter Knoten einer Policy weiter vertrauen, die er nicht mehr aktualisieren
  kann?
- Der **dauerhafte Bus** ist ein leader-only, at-least-once JetStream-Overlay (ADR-0021),
  dessen Backend ein privater Enterprise-Build ist; der OSS-Baum liefert nur die Naht. Er
  ist ein *Distributions*-Backbone, kein lokaler Disk-Spool.
- **Der OTA-Updater definiert bereits ein signiertes Bundle** für Air-Gap-Updates: ein
  gzip-Tar aus einem JSON-`manifest.json` plus einer detached Ed25519-Signatur über die
  domain-separierten unveränderten Bytes (`tag || manifest`, Tag
  `olivares.update-manifest.v1\n`), die VOR dem Parsen verifiziert werden
  (`core/release/manifest.go`). Ein separates `airgap-bundle.sh` (cosign, Images + Chart)
  und `core/dr/bundle.go` (AES-GCM-versiegelter DR-Snapshot) existieren ebenfalls.

Drei Fragen müssen vor jedem DDIL-Code geklärt sein, weil sie die Fail-Safe-Richtung und
nicht den Mechanismus definieren.

## Entscheidungstreiber

- **Fail-safe in der richtigen Richtung.** Eine Governance-Control-Plane darf wegen einer
  verlorenen Verbindung niemals Berechtigungen *eskalieren* und Evidenz niemals
  *stillschweigend* verlieren.
- **Missionssicherheit am Edge.** Ein stundenlanger Verbindungsausfall darf nicht zum
  Missionsabbruch führen, wenn die sichere Antwort lokal bereits bekannt war.
- **Keine Formatwucherung.** „Ein verifizierbares Bundle-Format, nicht zwei“
  (DDIL-Design-Brief). Eine zweite handgefertigte Signed-Envelope-Implementierung ist eine
  zweite Stelle, an der Domain Separation fehlerhaft sein kann — genau die Falle
  protokollübergreifender Schlüsselwiederverwendung, für die der OTA-Updater bereits bezahlt
  hat.
- **Ehrlichkeit.** Deklarierte, dokumentierte Grenzen (Disk-Budgets, TTLs, was einen
  unendlichen Ausfall nicht überlebt) statt stiller Kürzung.

## Betrachtete Optionen

### Q1 — Offline-Vertrauen in Policies

- **A. Asymmetrisch (Deny unbegrenzt, Allow verfällt).** Einschränkende Regeln (ABAC-Deny,
  Cedar-`forbid`) bleiben offline unbegrenzt durchgesetzt; positive Grants (Cedar-scoped
  `allow`, ADR-0019/ADR-0022) verfallen nach einem signierten `policy_max_staleness` und
  schlagen deny-closed fehl.
- **B. Vollständig deny-closed nach TTL-Ablauf.** Nach der TTL stellt der Knoten Governance
  vollständig ein.
- **C. Niemals ablaufen, nur warnen.**

### Q2 — Audit-Verhalten bei erschöpftem lokalen Disk-Budget

- **A. Standardmäßig fail-closed, opt-in Degradation.** Standard `block`: neue governte
  Aktionen verweigern, bevor Evidenz verloren geht. Opt-in `degrade`: Segment versiegeln
  und einen **signierten, in-chain Gap-Marker** anhängen, sodass der Verlust
  nachweisbar und niemals still ist.
- **B. Immer fail-closed.**
- **C. Immer degradieren.**

### Q3 — Vereinheitlichung des Bundle-Formats

- **A. `core/sigbundle` + Domain-Tag-Registry extrahieren.** Den OTA-Update-Envelope in ein
  gemeinsames Paket heben; `core/release` hinter einem byte-identischen Golden Test darauf
  refaktorieren; diese DDIL-Arbeit und der Security-Advisories-Feed fügen eigene Domain-Tags
  hinzu.
- **B. `core/release` unverändert lassen; jede Session kopiert das Muster.**

## Entscheidungsergebnis

**Q1 → Option A (asymmetrisch).** Offline nach `policy_max_staleness`:

| Regelklasse | Offline, TTL abgelaufen | Begründung |
|---|---|---|
| ABAC-Deny | **wird weiter durchgesetzt** | eine veraltete Einschränkung kann nur einschränken, niemals eskalieren |
| Cedar-`forbid` (absolut, ADR-0022) | **wird weiter durchgesetzt** | ebenso; Forbid überschreibt bereits alles |
| positiver Cedar-Grant / `allow` | **abgelaufen → deny-closed** | „Ein abgelaufener Grant darf niemals autorisieren“ |
| Break-glass | verfügbar, eigener Ablauf nach 1 h/24 h | der sanktionierte Offline-Fluchtweg |

`policy_max_staleness` ist eine Operator-Einstellung (standardmäßig 72 h), die im
Policy-Bundle mitgeführt und signiert wird; Konsole/CLI zeigen Alter und Ablauf deutlich an.

**Q2 → Option A (standardmäßig fail-closed, opt-in Degradation).** Konfiguration
`audit.spool.on_full`:

- `block` (Standard): Neue governte Aktionen werden verweigert (`503`, deny-closed); Reads
  werden weiter bedient; Konsole/CLI zeigen „audit spool full — governance halted“.
- `degrade` (explizites Opt-in): Versiegelt das aktuelle Segment und hängt einen signierten
  In-Chain-`audit.gap`-Marker `{from_seq, to_seq, reason: "spool_full", count, at}` an,
  damit die Kette kontinuierlich bleibt und der Verlust beweisbar ist.
  `audit.spool.max_bytes` wird deklariert und dokumentiert.

Der Gap-Marker ist die EINZIGE sanktionierte Diskontinuität der Kette; der Offline-
Archivprüfer (`core/audit/archiveverify.go`) wird erweitert, um einen signierten Gap-Marker
als *deklarierte* Grenze statt als `seq-gap`-Fehler zu erkennen.

**Q3 → Option A (`core/sigbundle` extrahieren).** Ein Envelope:

```
core/sigbundle/
  SigningInput(tag, payload) = tag || payload           // verbatim, no canonicalization
  Sign(tag, payload, priv) / Verify(tag, bundle, sig, pub)   // Ed25519, detached, verify-BEFORE-parse
  Envelope: tar.gz{ manifest.json, manifest.json.sig, <payload files by sha256> }
  Manifest: schema_version, kind, created_at, expires?, entries[{name, sha256, size}]
```

`core/release` wird auf die Wiederverwendung von `sigbundle.SigningInput` mit dem Tag
`olivares.update-manifest.v1\n` refaktoriert, abgesichert durch einen Golden Test, der
bestätigt, dass `release.ManifestSigningInput(b)` Byte für Byte unverändert ist (damit jede
bereits ausgestellte Release-Signatur weiter verifiziert). Die **Domain-Tag-Registry** (eine
Tabelle + Test auf Eindeutigkeit/keine Präfixkollision) verzeichnet jedes Tag:

| Tag | Owner | Hinweis |
|---|---|---|
| `olivares.update-manifest.v1\n` | `core/release` (Update-Manifest) | nach Refaktorierung byte-identisch |
| `olivares.ddil-bundle.v1\n` | diese DDIL-Arbeit | NEU — Air-Gap-Bundle für Policy+Audit+Evidenz |
| `olivares.security-advisories.v1\n` | Security-Advisories-Feed | NEU — signierter OSV-Advisories-Feed |

`core/license` (reiner, mit `{` beginnender JSON-Payload) und die Domains von
Audit-Ereignissen/-Checkpoints (`olivares.audit.*`) bleiben nachweislich disjunkt von jedem
Tag (ein Tag beginnt nie mit `{`, und die Audit-Domains sind längenpräfigierte Preimages,
keine Tar-Bundles). `core/dr/bundle.go` bleibt bewusst **unverändert**: Es ist ein
*versiegelter* (AES-GCM), unsignierter DR-Snapshot — ein anderes Vertrauensmodell
(Vertraulichkeit statt Publisher-Authentizität) —, dessen Einbeziehung beides vermischen
würde.

### Konsequenzen

- **Gut:** Fail-safe in der richtigen Richtung auf beiden Ebenen; ein auditierter Envelope
  und eine Domain-Separation-Disziplin statt drei; der Edge verweigert nach einem langen
  Ausfall weiterhin, was immer verweigert wurde; Evidenzverlust ist standardmäßig unmöglich
  und bei expliziter Erlaubnis nachweisbar.
- **Schlecht / Abwägungen:** Positive Grants funktionieren bei einem wirklich langen
  Ausfall nach `policy_max_staleness` nicht mehr (abgemildert durch Break-glass und die TTL
  als Operator-Entscheidung); der Modus `degrade` tauscht Evidenz gegen Verfügbarkeit und
  muss bewusst aktiviert werden; die Refaktorierung von `core/release` berührt frisch
  gemergten OTA-Updater-Code (abgemildert durch den Golden Byte-Identity-Test).
- **Neutral / Follow-ups:** Der Security-Advisories-Feed hängt von `core/sigbundle` und
  seinem eigenen Tag ab; der Archivprüfer erhält ein `declared-gap`-Vokabular;
  `docs/deploy/ddil.md` dokumentiert Disk-Budgets, TTL und was einen unendlichen Ausfall
  nicht überlebt.

## Warum die Alternativen verworfen wurden

- **Q1-B (vollständig deny-closed):** ein Missionsabbruch. Eine länger als die TTL
  ausgefallene Verbindung würde eine Edge-Einheit anhalten, obwohl ihre Deny-Regeln nie
  zweifelhaft waren.
- **Q1-C (niemals ablaufen):** Ein im Zentrum widerrufener Grant bliebe am Edge für immer
  aktiv — ein unbegrenztes Autorisierungsfenster ist für eine Governance-Ebene inakzeptabel.
- **Q2-B (immer fail-closed):** entfernt einen legitimen Operator-Trade-off (einige
  Edge-Missionen dürfen nicht anhalten); der signierte Gap-Marker macht Degradation bereits
  ehrlich.
- **Q2-C (immer degradieren):** ein schwacher Standard für ein Governance-Produkt —
  policy-bedingter stiller Evidenzverlust ist genau das, was das Ledger verhindern soll.
- **Q3-B (Muster kopieren):** drei Envelope-Implementierungen und drei Möglichkeiten, Domain
  Separation falsch umzusetzen; die Lektion aus protokollübergreifender
  Schlüsselwiederverwendung lautete gerade, dass ein Schlüssel über zwei Nachrichtentypen
  ohne Tag einen Fälschungsvektor erzeugt.

## Implementierungshinweis (2026-07-10)

Q2 ist wie ratifiziert implementiert. Der Gap-Marker deklariert den verworfenen Bereich
`{from_seq, to_seq, count, reason, at}` als Sequenzlücke, deren Hash-Verkettung
kontinuierlich bleibt. Live-Kettenprüfer, Archivexporter und Offline-Archivprüfer erkennen
einen korrekt deklarierten, korrekt signierten Marker als deklarierte Grenze
(`declared_gaps` in ihren Berichten), während jede nicht deklarierte oder inkonsistente
Diskontinuität weiterhin fehlschlägt. Das Budget misst die exakten logischen Bytes der
gespeicherten Ereigniswerte über einen inkrementellen Zähler, der bei jedem budgetierten
Boot aus dem Ledger neu berechnet wird; Integritätsmechanik (Checkpoints, Archivanker, der
Marker selbst) wird über Budget zugelassen, aber vollständig berücksichtigt, und die
Systemebene wird wie jeder andere Writer budgetgesteuert.

Eine parallele Implementierung, die die Kette lückenlos hielt (Zusammenfassungsmarker ohne
Sequenzlücke, physische Seiten-/Relationsmessung, Ausnahme für die Systemebene), wurde am
selben Tag integriert und bei der Abstimmung durch diese ersetzt: Der ratifizierte Text
schreibt den deklarierten Bereich und die Erweiterung des Prüfers vor, und der exakte Zähler
beseitigt Messhysterese und die Probleme der modifizierten v3-Migration des physischen
Ansatzes. Die ersetzte Variante bleibt zu Referenzzwecken in der Historie.
