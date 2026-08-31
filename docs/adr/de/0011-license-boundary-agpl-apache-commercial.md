> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0011: Lizenzgrenze — AGPL-Produkt, Apache-SDK/Connectoren, kommerzielles Enterprise

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** licensing design (final decision); stack license boundary

## Kontext und Problemstellung

Das Produkt brauchte ein Lizenzierungsmodell, das das Produkt wirklich offen hält, ein
Ökosystem von Drittanbieter-Connectoren frei von Copyleft-Reibung lässt und einen sauberen
kommerziellen Pfad offenlässt — ohne Feature-Gating (siehe ADR-0010).

## Entscheidungstreiber

- Ein wirklich offenes, Copyleft-Produkt (nicht source-available, nicht verstümmelt).
- Ein permissives Connector-Ökosystem, damit Dritte es frei erweitern können.
- Eine saubere kommerzielle Ausnahme für diejenigen, die sie benötigen.

## Betrachtete Optionen

- **Reine duale Lizenz:** AGPL-Produkt + Apache-2.0-SDK/Connectoren + kommerzielle Ausnahme.
- **Feature-gegatetes Open Core** (MIT/Apache-Kern + bezahlte Funktionen).
- **Alles permissiv** (MIT/Apache-Kern).
- **Source-available** (BSL, SSPL, PolyForm).

## Entscheidung

Gewählte Option: eine **reine duale Lizenz**. `core/`, `modules/`, `web/` sind
**AGPL-3.0-only**; `sdk/` und `connectors/` sind **Apache-2.0**; `enterprise/` ist
**kommerziell** (`LicenseRef-Olivares-Commercial`). Die Grenze wird ab dem ersten Commit
durch SPDX-Header pro Datei und eine CI-Prüfung durchgesetzt: ein Apache-2.0-Connector
importiert **niemals** die AGPL-Engine.

### Konsequenzen

- **Gut:** Das Produkt ist wirklich offen und Copyleft; Connectoren bleiben permissiv und
  reibungslos; die Grenze wird mechanisch durchgesetzt; ein kommerzieller Pfad existiert,
  ohne irgendetwas zu kappen.
- **Schlecht / Kompromisse:** Mitwirkende müssen die SPDX-Header korrekt halten und die
  Importgrenze respektieren (CI fängt Verstöße ab).
- **Neutral:** Die kommerzielle Ausnahme ist self-serve plus ein Enterprise-Kontakt.

## Warum die Alternativen verworfen wurden

- **Feature-gegatetes Open Core** — kappt das Produkt (siehe ADR-0010), verworfen.
- **Alles permissiv** — verschenkt den Kern ohne kommerzielles Standbein.
- **Source-available (BSL/SSPL/PolyForm)** — kein OSS; killt die Adoption, von der das
  Connector-Ökosystem abhängt.

## Änderung (2026-06-23) — das Modell ist Open Core

Die **oben beschriebene Lizenzgrenze ist unverändert und korrekt**: `core/`+`modules/`+
`web/` sind AGPL-3.0-only, `sdk/`+`connectors/` sind Apache-2.0, `enterprise/` ist
kommerziell, und ein Apache-Connector importiert niemals die AGPL-Engine. Korrigiert wird
die *Einordnung*: Das ausgelieferte Produkt ist **Open Core** (das GitLab-`ee/`-Modell),
**nicht** eine „reine duale Lizenz“ ohne Funktionsunterschiede. Der AGPL-Build ist die
vollständige Governance-Plattform und wird intern niemals beschnitten, um zum Upgrade zu
drängen — aber er ist **nicht identisch** mit der kommerziellen Edition: Die
`enterprise/`-Linie (Multi-IdP-Föderation, Content Firewall/DLP, Hook-Härtung,
Threat-Intel-Feed, Server-Tool-Egress, CyberArk Conjur, Incident-Closed-Loop) ist
**additiver neuer Code, der nie im offenen Build enthalten war** (kein Rug Pull). Daher
sollte „betrachtet/gewählt: reine duale Lizenz“ als Entscheidung über die AGPL/Apache-
*Grenze* gelesen werden; die *Editions*-Entscheidung Open-vs-Commercial ist Open Core.
Siehe `LICENSING.md`

Die Lizenz**grenze** hier wird nicht ersetzt. Separat geändert hat sich die
**Verteilung** der kommerziellen Linie: Der Quellcode von `enterprise/` wird nicht mehr
im öffentlichen Repository ausgeliefert — er wurde in ein privates Repository verschoben,
damit das Build-Tag-Gating real und nicht kosmetisch ist. Das ist eine
Verteilungsentscheidung, festgehalten in **ADR-0020**; die Grenze und die
Attestierungs-only-Lizenz (ADR-0010) bleiben unverändert.

## Änderung (2026-07-28) — zwei überholte Aussagen in der Anmerkung vom 2026-06-23

Die Lizenzgrenze und die Open-Core-Einordnung oben gelten weiterhin. Zwei Punkte der
Enterprise-Liste in der Änderung vom 2026-06-23 beschreiben das Produkt nicht mehr; die
Notiz selbst bleibt exakt wie geschrieben, da sie dokumentiert, was damals angenommen
wurde.

1. **„die Sitzplatzberechtigung, die die Community-Benutzerbegrenzung aufhebt“ existiert
   nicht mehr.** Entscheidung B10 (2026-07-27) hat die Benutzerbegrenzung vollständig
   entfernt: Selbstgehostete Konten sind in jeder Edition unbegrenzt,
   `core/auth.CommunitySeatLimit` ist `0`, `enforceSeatCapTx` ist bedingungslos ein No-op,
   und kein Build — offen oder kommerziell — liest eine Lizenz, um Benutzer zu begrenzen.
   Aktuelle Entscheidung: der kommerzielle Preis-Kanon (privat gepflegt) (`self_hosted.users: unlimited`) und
   `LICENSING.md`
2. **„Threat-Intel-Feed“ beschreibt nicht, wie das Add-on verkauft werden darf.**
   `enterprise/threatintel` liefert einen in den Build kompilierten Basiskatalog sowie
   optionale signierte, versionierte Feed-Artefakte, für die der Betreiber einen
   Publisher-Schlüssel anheftet und die er anwendet; Olivares betreibt keine kuratierte
   Feed-Verteilung und veröffentlicht keine Release-Kadenz. Der kommerzielle Kanon
   (der kommerzielle Preis-Kanon (privat gepflegt), `self_hosted.business.preset`) verbietet, es als „Feed“ zu
   vermarkten, sofern nicht tatsächlich ein signierter Feed betrieben wird. Die
   Operator-CLI behält das Wort für das Artefakt, das sie prüft und anwendet
   (`olivares threatintel verify|apply|pull`) — dies ist der Name des Artefakts, keine
   Aussage darüber, wer es veröffentlicht.

Keiner der beiden Punkte berührt die Lizenzgrenze, über die diese ADR entscheidet.
