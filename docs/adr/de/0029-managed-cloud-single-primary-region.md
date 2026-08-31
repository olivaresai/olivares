> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0029: Managed-Cloud-Regionen — eine Primärregion, Datenresidenz durch Self-Hosting

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0027 (managed-cloud ingress), ADR-0028 (managed-cloud database),
  ADR-0020 (enterprise private-repo distribution), ADR-0024 (DDIL offline semantics and
  signed bundles); the platform decision record for the managed cloud.

## Kontext und Problemstellung

Zwei Fragen müssen gemeinsam beantwortet werden, weil eine schlechte Antwort auf die eine
eine schlechte Antwort auf die andere erzwingt: **Wo läuft die Managed Plane**, und **was
sagen wir einem Kunden, der fragt, wo seine Daten gespeichert sind**?

Es ist verlockend, die Region zu wählen, welche die zweite Frage einfach macht — eine Region,
deren Rechtsordnung in einem Compliance-Abschnitt gut klingt —, und jede daraus folgende
Latenz für die tatsächlichen Kunden hinzunehmen. Das ist die falsche Reihenfolge. Es beruht
außerdem auf einem Missverständnis, das einmal dauerhaft festgehalten werden sollte, damit es
niemand erneut herleitet: **Der Hosting-Standort bestimmt NICHT, welches Datenschutzrecht
gilt.** Wer betroffene Personen in einer Rechtsordnung bedient, unterliegt deren Recht,
unabhängig vom Hosting-Standort.

## Entscheidungstreiber

- Latenz zu den Kunden, an die das Produkt tatsächlich verkauft wird.
- Die Compliance-Nachweise, die ein Enterprise-Käufer verlangt; dabei handelt es sich
  überwiegend um Nachweise über den **Infrastrukturanbieter**, nicht über die Region.
- Die festen Kosten einer zweiten Region — und die dauerhafte Komplexität der
  regionsübergreifenden Datenverarbeitung — nicht zu tragen, bevor ein Kunde sie verlangt.
- Eine wahrheitsgemäße, nicht ausweichende Antwort für Kunden mit einer zwingenden
  Residenzanforderung zu haben.

## Betrachtete Optionen

- **A — eine einzige Primärregion im Zielmarkt**, wobei eine zweite Region als
  nachfrageabhängiges Projekt vorgesehen ist.
- **B — zwei Regionen ab dem Launch**, eine je wichtigem Markt.
- **C — eine für die regulatorische Darstellung gewählte Primärregion** anstelle einer Wahl
  nach Kundenlatenz.

## Entscheidungsergebnis

Gewählte Option: **A — eine einzige Primärregion im Zielmarkt (Osten der Vereinigten
Staaten)**. Eine zweite Region wird erst dann zu einem Projekt, wenn eine zahlende Anforderung
vorliegt; sie ist kein Launch-Bestandteil. Die Regionsbindung je Mandant und
regionsübergreifende Replikation liegen bewusst außerhalb des Scopes des ersten
Managed-Releases.

Ein Kunde mit einer **vertraglichen oder regulatorischen Residenzanforderung, welche die
Primärregion nicht erfüllt**, wird durch die **Self-Hosted-Edition** bedient. Sie ist die
primäre Form des Produkts, läuft in der eigenen Infrastruktur des Kunden und beantwortet die
Residenzfrage vollständig statt nur teilweise. Dies ist kein Workaround, sondern die
stärkere Antwort, und sie ist vom ersten Tag an verfügbar.

### Konsequenzen

- **Gut:** Das Deployment umfasst eine Region, eine Datenbank und eine zu berücksichtigende
  Ausfalldomäne; das Latenzbudget wird dort eingesetzt, wo sich die Kunden befinden.
- **Gut:** Die Antwort auf die Residenzfrage ist ehrlich und sofort verfügbar — Self-Hosting
  — statt eines Versprechens auf der Roadmap.
- **Schlecht / Abwägungen:** Ein Kunde, der *Managed Cloud* **und** eine Residenz außerhalb
  der USA verlangt, kann erst bedient werden, wenn eine zweite Region existiert. Dies ist
  eine bekannte, akzeptierte Lücke und sollte in Vertriebsunterlagen klar benannt statt
  kaschiert werden.
- **Schlecht:** Eine einzelne Region ist eine einzelne regionale Ausfalldomäne. Multi-AZ
  (ADR-0028) deckt den Ausfall einer Availability Zone ab, **nicht** den Ausfall einer Region.
  Bei einem regionalen Ausfall wird aus Backups an einem anderen Ort wiederhergestellt; die
  Recovery-Zeit wird in Stunden gemessen und muss **erprobt** werden, bevor sie jemandem
  genannt wird.
- **Neutral und der Grund, dies festzuhalten:** Die Wahl einer US-Primärregion bedeutet, dass
  personenbezogene Daten von betroffenen Personen außerhalb der USA **übermittelt** werden.
  Dafür sind ein gültiger Übermittlungsmechanismus und eine Vereinbarung zur
  Auftragsverarbeitung erforderlich, die den Infrastrukturanbieter als Unterauftragsverarbeiter
  nennt. Dieser ADR schafft **weder das eine noch das andere**. Er hält fest, dass **die Wahl
  der Region die Verpflichtung nicht beseitigt**, damit kein künftiger Leser „wir hosten in
  Region X“ für eine Compliance-Antwort hält. Dies ist ein technischer Entscheidungsdatensatz
  und keine Rechtsberatung; die Instrumente selbst gehören in den Compliance-Track.

## Warum die Alternativen verworfen wurden

- **B (zwei Regionen ab dem Launch)** — abgelehnt, weil dauerhaft doppelt für einen Kunden
  gezahlt würde, den es noch nicht gibt. Eine zweite Region verdoppelt die feste
  Infrastrukturuntergrenze und fügt eine Problemklasse hinzu, die nie mehr verschwindet:
  welche Region für einen Mandanten zuständig ist, was zwischen den Regionen übertragen wird
  und wie eine Residenzaussage je Mandant statt je Plattform nachgewiesen wird. All dies
  lohnt sich, sobald eine unterzeichnete Anforderung es finanziert.
- **C (für die regulatorische Darstellung gewählte Region)** — abgelehnt, weil sie einen
  Absatz erkauft und mit jeder Anfrage dafür bezahlt. Sie liefert außerdem nicht, was sie zu
  liefern scheint: Wie oben beschrieben, bestimmt der Hosting-Standort **nicht** das
  anwendbare Recht. Die Darstellung wäre daher schwächer, als sie klingt, während die
  Latenzkosten genauso hoch wären, wie sie klingen.
