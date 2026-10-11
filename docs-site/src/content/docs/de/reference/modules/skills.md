---
title: "Skills — der immutable Katalog und die gepinnten Zuweisungen"
description: >-
  Eines der 32 Module: der immutable Skills-Katalog und seine gepinnten
  Zuweisungen. Skill-Packs werden als digest-validierte, immutable Revisionen
  installiert; was ein Ziel nutzen darf, ist ein registrierter, auditierter Pin —
  nie eine Live-Ordneränderung.
---

Skills (`modules/skills`, `olivares.skills`) ist eines der 32 Module. Es besitzt den
**immutableen Skills-Katalog** und seine **gepinnten Zuweisungen**: Ein Skill-Pack
wird einmal installiert, Eintrag für Eintrag gegen seinen Manifest-Digest validiert
und als immutable Revision veröffentlicht — danach wird es nie an Ort und Stelle
editiert. Was ein Ziel (ein Agent, eine Gruppe, ein Workspace) nutzen darf, ist eine
**Zuweisung**, die eine Pack-Revision pinnt, und jeder Pin wird ins Audit-Ledger
geschrieben (`skills.assignment.pin`).

## Was es liefert

- **Der Katalog.** Installierte Packs unter `/v1/m/skills/packs`, jede unveränderliche
  Revision speichert ihren Manifest-Digest; ein Pack, das noch zugewiesen oder von
  aufgezeichneten Konversationen genutzt wird, zu retirieren wird verweigert, nicht
  still kaskadiert gelöscht.
- **Der eingebaute Katalog.** Ein mitgeliefertes Set upstreamer Skill-Packs, per
  Commit und Archiv-Digest gepinnt (`builtin/PIN.json`), bei der Installation exakt
  wie ein hochgeladenes Archiv validiert. Nichts davon läuft bei der Installation.
- **Die Zuweisungen.** `GET/POST /v1/m/skills/assignments` und
  `PUT/DELETE /v1/m/skills/assignments/{id}` pinnen eine Revision an ein Ziel, im
  Scope des aktiven Workspace. Zwei zugewiesene Skills mit gleichem Namen und
  unterschiedlichem Inhalt werden vor Sitzungsstart verweigert, damit ein Launch
  deterministisch ist.
- **Git-Import.** Ein operator-scoped Importer kann ein Skill-Pack aus einer
  Git-Quelle unter einer gepinnten Policy holen; der Import landet als weitere
  immutable Revision im Katalog, nicht als Live-Checkout, den das Engine liest.

## Oberflächen und Berechtigungen

Die Routen des Moduls leben in seinem Beta-Namespace (`/v1/m/skills/…`), dokumentiert
in der [Modul-Routen-Referenz](/reference/api-beta/) und gerendert in der
Skills-Ansicht der Konsole. Lesevorgänge brauchen die Read-Berechtigung; ein Pack oder
eine Revision zu installieren braucht Write; Retirieren braucht Admin; Zuweisungen
brauchen die Assign-Berechtigung. Eine Session löst ihre Skills nur über registrierte
Zuweisungen auf — ein Ordner, der sich auf der Festplatte ändert, ändert nie, was ein
bereits gepinnter Launch ausführen wird.

:::caution[Ehrliche Grenzen]
- **Neu seit dem Release 26.10.1<!-- release-fixed -->.** Das offizielle 26.10.1<!-- release-fixed -->-Binary enthält dieses
  Modul nicht; es kommt mit dem nächsten Release, und diese Seite beschreibt das
  Modul, wie es auf der aktuellen Integrationslinie existiert.
- **Der Katalog steuert; er führt nicht aus.** Installieren, Pinnen und Importieren
  führen nie Skill-Code aus — die Ausführung bleibt bei den Tools und Sessions, die
  eine gepinnte Revision konsumieren.
:::

## Verwandtes

- [Modul-Katalog](/de/reference/modules/overview/) — die 32 Module und wo dieses
  darunter sitzt.
- [MCP, Skills & Fähigkeiten](/de/reference/modules/v-capabilities/) — die steuernde
  Sicht über Tools und Fähigkeiten, die dieser Katalog speist.
- [Interner Katalog & Marketplace](/de/reference/modules/xiv-catalog/) — der kuratierte
  Marketplace freigegebener Agenten, MCP-Server und Skills.
- [Modul-Routen (beta)](/reference/api-beta/) — die `/v1/m/skills/`-Operationen.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — warum Unveränderlichkeit
  ausgesagt wird, nicht unterstellt.
