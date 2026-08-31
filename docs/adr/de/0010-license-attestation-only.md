> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0010: Lizenz ist nur Attestierung — niemals Feature-Gating

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** licensing design (final decision); API/authz/audit contract (§7, §13.5)

## Kontext und Problemstellung

Ein kommerzielles Open-Core-Produkt muss entscheiden, was seine Lizenzdurchsetzung zur
Laufzeit *tut*. Die Versuchung besteht darin, Funktionen hinter eine Lizenzprüfung zu
stellen. Bei einem Sicherheitsprodukt, das zugleich eine potenzielle Angriffsfläche für
Authorization-Bypass darstellt, steht das im Widerspruch zu einer Philosophie der „reinen
dualen Lizenz, nichts gekappt".

## Entscheidungstreiber

- Das offene Produkt nicht verstümmeln.
- Die Lizenzvalidierung nicht zu einer Angriffsfläche für Authorization-Bypass machen.
- Air-gapped funktionieren, ohne Lizenzserver.

## Betrachtete Optionen

- **Reine Attestierungs-Lizenzvalidierung**, die niemals etwas blockiert.
- **Feature-Gating** nach Lizenzstufe.

## Entscheidung

Gewählte Option: **nur Attestierung**. Die Lizenzvalidierung erfasst Inhaber und Status
und ist rein informativ; sie **deaktiviert, beeinträchtigt oder blockiert** niemals eine
Anfrage, ein Modul oder einen Boot-Vorgang **im offenen (AGPL-)Binary**. Die Validierung
erfolgt **offline** (eine Ed25519-Signatur; kein Lizenzserver). Das offene Binary ist die
vollständige CORE-Governance-Plattform; die kommerzielle Edition fügt die separaten,
additiven `enterprise/`-Add-ons hinzu (dies ist Open Core, nicht „dasselbe vollständige
Produkt“ — siehe die Änderung unten und ADR-0011).

### Konsequenzen

- **Gut:** Das offene Produkt wird niemals verstümmelt; Lizenzprüfungen können nicht in
  einen Authorization-Bypass umgewandelt werden; das Produkt läuft air-gapped.
- **Schlecht / Kompromisse:** Die kommerzielle Differenzierung ergibt sich aus den
  *Lizenzbedingungen* und den separaten `enterprise/`-Modulen, nicht aus einem Gating des
  Kerns.
- **Neutral:** Lizenztests sind per Design fail-open.

## Warum die Alternativen verworfen wurden

- **Feature-Gating** — macht die offene Edition zu einem schlechteren Produkt, untergräbt
  das Vertrauen und verwandelt eine fälschbare Lizenz in ein sicherheitsrelevantes Gate.
  Verworfen.

## Änderung (2026-06-23)

Diese ADR gilt weiterhin, präzise abgegrenzt: **Der Lizenzschlüssel gate't niemals das
OFFENE Binary.** Das Modell ist **Open Core** (Änderung zu ADR-0011); die frühere Aussage,
„das kostenlose und das lizenzierte Produkt sind dasselbe vollständige Produkt“, war
ungenau und ist oben korrigiert — die kommerzielle Edition hat die additiven
`enterprise/`-Add-ons (genau die „separaten `enterprise/`-Module“, die diese ADR stets
nennt). Ein attestierter Claim wird nur vom geschlossenen Enterprise-Build *verarbeitet*
und nur dafür, diese additiven Add-ons zu berechtigen — eine lokale Entscheidung im
eigenen kommerziellen Build des Kunden. Olivares' eigene Cloud darf dem selbst gemeldeten
Status für die Abrechnung weiterhin niemals vertrauen.

**Änderung (2026-07-27, B10).** Der eine sitzbezogene Verbrauch, den diese Notiz zuvor
beschrieb — `enterprise/seats`, das `license.Claims.MaxUsers` las, um eine
Community-Benutzersitzplatzbegrenzung aufzuheben — ist entfallen: Selbstgehostete
Benutzerkonten sind in jeder Edition UNBEGRENZT, die Begrenzung von 3 wurde entfernt, und
`MaxUsers` dient überall nur der Anzeige. Das stärkt diese ADR, statt sie einzuschränken:
Kein Build, weder offen noch kommerziell, liest nun eine Lizenz, um Benutzer zu begrenzen.
Siehe `LICENSING.md` und der kommerzielle Preis-Kanon (privat gepflegt).
