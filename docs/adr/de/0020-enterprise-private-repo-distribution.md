> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0020: Verteilung der Enterprise-Edition aus einem separaten privaten Repository

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** ADR-0010 (license is attestation-only), ADR-0011 (license boundary),
  `LICENSING.md`

## Kontext und Problemstellung

Das Lizenzmodell ist Open Core: AGPL-Kern/-Module/-Web bilden die vollständige
Community-Edition, SDK/Connectoren stehen unter Apache-2.0, und der Zweig `enterprise/`
enthält additiven kommerziellen Code, der nur mit `-tags enterprise` gebaut wird
(ADR-0011). Bisher wurde der Quellcode von `enterprise/` jedoch **im öffentlichen
Repository ausgeliefert**. Da das Aktivierungs-Gate das Build-Tag ist (nicht die Lizenz,
die gemäß ADR-0010 nur der Attestierung dient) und die Lizenz niemals den Runtime-Zugriff
beschränkt, konnte jeder mit `git clone && go build -tags enterprise` kostenlos das
vollständige kommerzielle Binary erzeugen. Der kommerzielle Schutzwall beruhte vollständig
auf der rechtlichen Lizenz (Ehrensystem bei sichtbarem Quellcode).

## Entscheidungstreiber

- Das Build-Tag-Gating **real** statt kosmetisch machen: Das kommerzielle Binary darf nicht
  von jedermann aus öffentlichem Quellcode gebaut werden können.
- Das AGPL-Community-Binary **Bit für Bit unverändert** halten — kein Rug Pull und keine
  Entfernung einer Funktion, die bereits offen ausgeliefert wurde.
- Die Lizenzgrenze je Verzeichnis (ADR-0011) und die reine Attestierungslizenz (ADR-0010)
  unverändert beibehalten.

## Betrachtete Optionen

- **`enterprise/` im öffentlichen Repository belassen** (das GitLab-Modell mit `ee/` in
  einem Repository, source-available). Ehrlich, aber der Schutzwall ist ein Ehrensystem
  über sichtbarem, frei kompilierbarem Quellcode.
- **`enterprise/` in ein separates privates Repository verschieben** (das Grafana-Modell:
  öffentlicher OSS-Quellcode + herunterladbares Enterprise-Binary aus privatem Quellcode).

## Entscheidungsergebnis

Gewählte Option: **`enterprise/` in ein separates privates Repository verschieben**. Das
öffentliche Repository enthält weder den `enterprise/`-Baum noch das
`//go:build enterprise`-Wiring in `cmd/olivares` oder Werkzeuge, die mit
`-tags enterprise` bauen. Das kommerzielle Binary wird im privaten Repository gebaut,
indem der kommerzielle Baum und das Wiring über einen gepinnten Checkout des öffentlichen
Baums gelegt werden (der öffentliche Baum ist ein Submodul; das Wiring wird in das
`package main` von `cmd/olivares` eingeblendet, was `go.work` nicht allein durch
Modulauswahl leisten kann).

Dies ändert nur die **Verteilung**, nicht die Lizenzierung:

- **ADR-0011 (Lizenzgrenze) bleibt unverändert:** `enterprise/` ist weiterhin
  `LicenseRef-Olivares-Commercial`; die AGPL/Apache-Grenze bleibt intakt.
- **ADR-0010 (reine Attestierungslizenz) bleibt unverändert:** Das offene Binary liest
  weiterhin niemals eine Lizenz, um etwas zu aktivieren oder zu deaktivieren. Die Lizenz
  wird nur deshalb *materiell* bedeutsam, weil der Quellcode, der einen attestierten Claim
  (die Add-on-Berechtigungen) liest, nicht mehr öffentlich ist — nicht weil die Lizenz nun
  den Runtime-Zugriff beschränkt.

### Konsequenzen

- `git clone` des öffentlichen Repositorys + `go build -tags enterprise` erzeugt das
  kommerzielle Binary nicht mehr: Der benötigte Quellcode ist privat. Das Gating ist jetzt
  real.
- Das standardmäßige AGPL-Binary bleibt unverändert — es hat `enterprise/` nie gelinkt.
- Das open≡enterprise-Schemaparitäts-Gate (es benötigt beide Editionen) wandert in das
  private Repository, den einzigen Baum, der beide bauen kann.
- Zwei Repositorys und ein kleiner Overlay-Assembly-Schritt sind der Preis; das öffentliche
  Release-Artefakt bleibt unberührt (es wurde bereits mit `-tags release` und nie mit
  `-tags enterprise` gebaut).

## Änderung (2026-07-28) — die oben genannte Sitzplatzberechtigung ist entfallen

Die Verteilungsentscheidung gilt weiterhin: `enterprise/` liegt in einem privaten
Repository, und das Build-Tag-Gating ist real. Nicht mehr zutreffend ist das in den
Konsequenzen verwendete *Beispiel* — „der Quellcode, der einen attestierten Claim (die
Sitzplatzberechtigung) liest“. Entscheidung B10 (2026-07-27) hat die Benutzerbegrenzung
entfernt, daher gibt es keine Sitzplatzberechtigung und kein Build liest eine Lizenz, um
Benutzer zu begrenzen; die verbleibenden attestierten Claims werden nur gelesen, um die
additiven Add-ons zu berechtigen. Der ursprüngliche Satz bleibt unverändert, weil er
festhält, was bei dieser ADR-Entscheidung zutraf. Aktuelle Entscheidung:
der kommerzielle Preis-Kanon (privat gepflegt) (`self_hosted.users: unlimited`) und
`LICENSING.md`
