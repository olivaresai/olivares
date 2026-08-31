> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0009: Append-only-, hash-chained-, signiertes Audit-Ledger

- **Status:** accepted
- **Datum:** 2026-06
- **Entscheider:** Olivares AI
- **Referenzen:** API-/Authz-/Audit-Vertrag (§6, Entscheidung §13.4); Threat-Model (Ledger)

## Kontext und Problemstellung

Das Audit-Ledger ist eines der sensibelsten Assets des Produkts: Wenn es still verändert werden
kann, lügt das Produkt. Es muss Manipulationen erkennbar machen und externe, verifizierbare Kopien
unterstützen — und dabei ehrlich darüber sein, was die On-Host-Integrität garantieren kann und was nicht.

## Entscheidungstreiber

- Manipulationstransparenz: Ein Umschreiben der Historie muss erkennbar sein.
- Off-Box-Verifizierbarkeit für Compliance und Incident Response.
- Kein neues Storage-Subsystem für Checkpoints.

## Betrachtete Optionen

- **Append-only + Hash-Chain + Ed25519-signierte Checkpoints**, mit Export in eine externe
  WORM-/SIEM-Kopie.
- **Eine einfache Audit-Tabelle** mit Kontrollen auf Anwendungsebene.

## Entscheidungsergebnis

Gewählte Option: ein **Append-only-, hash-chained-Ledger**; ein Checkpoint ist selbst ein
signiertes Audit-Event (Ed25519, abgetrennte Signatur), sodass das Umschreiben von Historie vor
einem Checkpoint kryptografisch erkennbar ist. Das Ledger exportiert in externe SIEM-/WORM-Formate
(CEF, LEEF, syslog, OTLP — ein vollständiger, postbarer Export-Request; die
reine LogRecord-Projektion ist als eigenes Export-Token `otlp_log_record` verfügbar — OCSF),
wobei jeder Datensatz die Chain-Felder trägt, sodass ein SIEM
offline erneut verifizieren kann; PII wird niemals exportiert.

### Konsequenzen

- **Gut:** Manipulationstransparenz ohne separate Checkpoint-Tabelle; Offline-Re-Verifizierung;
  SIEM-fähiger Export.
- **Schlecht / Abwägungen:** Der On-Disk-Signaturschlüssel hält einen Host-Root- / DB-Superuser
  nicht auf — daher ist der **externe WORM-/SIEM-Export die eigentliche Anti-Tamper-Kontrolle**,
  und die Dokumentation sagt das auch.
- **Neutral:** Der Export war zum Zeitpunkt dieser Entscheidung pull-basiert; eine
  Schnittstelle für einen automatischen Push-Forwarder existierte, war aber noch nicht
  implementiert.

  > **Status-Nachtrag, 2026-07-25.** Der Push-Forwarder ist implementiert und verdrahtet:
  > `modules/siemforward` erfüllt `audit.Forwarder`, und `cmd/olivares/boot.go` startet
  > eine Ledger-Pumpe pro Tenant, sobald ein `audit.recorded`-Eventing-Abonnement
  > existiert. `NopForwarder` gilt, wenn kein Forwarding konfiguriert ist. Der
  > Pull-Export bleibt unverändert. Die Entscheidung selbst bleibt unberührt.

  > **Status-Nachtrag, 2026-07-28.** Die obige Aussage „Ein SIEM kann offline erneut
  > verifizieren“ galt zum Zeitpunkt dieser Entscheidung nur für die VERKETTUNG der Chain
  > und eine Checkpoint-Signatur: Die Projektionen enthielten keine Bindung an die Metadaten
  > eines Events, sodass sich der Hash eines Datensatzes nicht aus einer exportierten Zeile
  > neu berechnen ließ. Das ist jetzt möglich — jeder Input, den der Chain-Hash verarbeitet,
  > wird in jedem Dialekt übertragen, einschließlich der Metadatenbindung — und diese Bindung
  > wird pro Datensatz GEBLINDET, sodass die Vervollständigung des Preimages nichts über die
  > dahinterliegenden Metadaten preisgibt. Drei Aussagen bleiben getrennt, und der Satz in
  > diesem ADR deckt nur die erste ab: Neuberechnung des Preimages, NICHT Authentizität
  > (ein extern vertrauenswürdiger Schlüssel), NICHT Vollständigkeit (benachbarte Datensätze
  > und ein Checkpoint).
  >
  > Zwei Konsequenzen gehören in diesen Record. Beide Metadaten-Hashregeln bleiben nun
  > dauerhaft aktiv und werden pro Zeile durch einen gespeicherten Blind-Wert unterschieden,
  > weil ein Append-only-Ledger die Hashregel bereits versiegelter Zeilen nicht neu formulieren
  > kann, ohne eine legitime Historie von einer gefälschten ununterscheidbar zu machen. Aus
  > demselben Grund erhielt das Archivformat eine Version, um den Blind-Wert zu übertragen,
  > wobei die vorherige Version dauerhaft akzeptiert wird: Ein Artefakt, das noch Jahre später
  > lesbar sein soll, darf seine Version nicht nachträglich verlieren. Die Entscheidung selbst
  > bleibt unberührt.

## Warum die Alternativen verworfen wurden

- **Einfache Audit-Tabelle** — bietet keine kryptografische Manipulationstransparenz; für ein
  Sicherheitsprodukt, dessen Ledger-Integrität „alles“ ist, inakzeptabel.
