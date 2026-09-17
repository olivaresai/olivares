---
title: "Modul I — Inventar & Discovery"
description: >-
  Passive Discovery und Katalogisierung von Agents, Sessions, MCP-Servern,
  Skills, Tools, Modellen, Providern und nicht-menschlichen Identitäten, die im
  Estate beobachtet wurden. Wie Entitäten aus Beobachtungen materialisiert
  werden, was Katalog und Provenienz erfassen, wie dauerhafte Frische
  funktioniert, und die Grenzen.
---

Modul I ist der **Katalog beobachteter Entitäten** im Estate: ein passives,
busgetriebenes Inventar von Agents, Sessions, Claude-Code-Instanzen,
MCP-Servern, Skills, Tools, Ressourcen, Modellen, Providern und
nicht-menschlichen Identitäten, die Connectoren tatsächlich benannt haben. Es
betreibt Discovery durch *Zuhören*, nie durch Sondieren. Es erfasst
Beziehungen, Identifikatoren und Lebendigkeit — keine Payloads — und ist
**kein** Zensus von allem, was existiert. Diese Seite ist die Referenz dafür,
was der Katalog enthält, wie Beobachtungsprovenienz und dauerhafte Frische in
der Entwicklung für die nächste Version funktionieren, und was das Modul bewusst
nicht behauptet.

Die Beobachtungsprovenienz und die dauerhafte Frische sind für eine begrenzte
Entwicklungs-Composition akzeptiert. Es handelt sich um Funktionen in Entwicklung
für die nächste Version, nicht um ein veröffentlichtes Release, ein
vollständiges RC oder ein Deployment.

## Was es materialisiert

Connectoren emittieren **Beobachtungen**, keine Entitäten. Sie veröffentlichen
normalisierte [`edge.observed`](/de/reference/events/)- und
[`cost.sampled`](/de/reference/events/)-Fakten auf dem Event-Bus; die Entitäten,
die sie implizieren, werden nie gesendet. Modul I **materialisiert** die
Kernentität, die jede Beobachtung anhand ihrer natürlichen Referenz benennt:
eine Herkunft `session`/`agent`/`identity`, einen MCP-Server, ein Tool, eine
Ressource, einen Skill und — aus Kostenproben — einen Provider und ein Modell
(entdeckt, **ohne Preisgestaltung**; FinOps besitzt das). Das Inventar
abonniert diese beiden Event-Typen; Live-Findings gehören zu
[Modul II](/de/reference/modules/ii-sessions/).

Find-or-create der Kernentität auf dem natürlichen Schlüssel vermeidet, den
Katalog-Alias unter At-least-once-Zustellung im aktuellen Einzel-Schreiber-Modell
zu duplizieren. Zwei Quellen können weiterhin getrennte Beobachtungen behalten,
die diesen Alias teilen; das Teilen beweist nicht, dass sie dieselbe physische
Sache sind, und überträgt weder Owner noch Workspace noch Grants. Eine neue
Event-ID mit demselben Payload ist ein **neuer** Beleg — ob das eine andere
Aktivität ist, bleibt unbekannt. Der `occurrence_count` des Katalogs zählt
**Zustellungen**, einschließlich eines Replays derselben Event-ID, keine
eindeutigen Aktivitäten.

## Beobachtungsprovenienz

Neben dem Katalog-Alias speichert das Modul eine additive, tenant-bezogene
Projektion, wie eine Beobachtung ankam: einen Beleg mit Schlüssel Event-ID,
eine Member-Zeile je materialisierter Entität (native Referenz und, wenn der
Registrierungs-Snapshot gültig ist, eine stabile Beobachtungsidentität) und
eine Konfliktzeile, wenn dieselbe Event-ID später andere projizierte Fakten
trägt. Der ursprüngliche Beleg bleibt erhalten; der Konflikt wird
aufgezeichnet statt überschrieben. Eine fehlende Event-ID erhält nur eine
Speicheridentität und wird nie per Payload dedupliziert. Ein Replay derselben
ID und derselben Fakten aktualisiert den Legacy-Zustellungszähler des Katalogs,
ohne Aliase neu aufzulösen. Die Konsole kann gespeicherte Belege einer
Katalogentität unter
`GET /v1/m/inventory/entities/{kind}/{id}/observations` mit der bestehenden
tenant-weiten Berechtigung `inventory:catalog:read` listen: ein Eintrag je
unterscheidbarem Beleg in aufsteigender Beleg-ID, Seiten von höchstens 25. Jeder
Eintrag ist eine historische Registrierungsinstantaufnahme beim Empfang (nicht
die aktuelle Registrierung oder Gesundheit der Quelle), der von der Quelle
deklarierte Zeitpunkt falls deklariert, erster und letzter Empfang der
behaltenen Fakten, Lieferungen mit übereinstimmenden Fakten und ob eine
konfliktierende erneute Lieferung behalten ist. Eine leere Seite beweist nicht,
dass die Entität nie beobachtet wurde. `has_more`, eine leere Liste und die
Katalogsumme sind keine Abdeckung. Die Katalogfrische auf der Entitätskarte
stammt aus einem erfolgreichen aktuellen Point-Read; sie ist ein anderer
Lesevorgang als der Verlauf. Abdeckung je Quelle/Familie und das C4-Referenz-
Estate bleiben offen.

## Sein Vertrag & seine Entitäten

Das Modul registriert `inventory.catalog_entry` — ein Discovery-Overlay, das
an jede materialisierte Kernentität angehängt wird. Es erfasst, *wie* etwas
gefunden wurde, nicht, *was* es tat: Signalquellen, Hosts soweit bekannt,
First- und Last-seen-Zeitstempel, ein optionales von der Quelle deklariertes
`occurred_at` (entfällt, wenn die Quelle keines deklarierte; verschieden von
`last_seen`, dem Zeitpunkt, zu dem **diese Plattform** es sah), einen
Vorkommenszähler und einen Lebendigkeits-`status` von `active` oder `stale`.
Die Lese-Oberfläche ist klein und schreibgeschützt: eine `summary`-Zählung
nach Art und Quelle, eine paginierte `entities`-Auflistung, filterbar nach
Art und Status, eine Detailansicht für eine einzelne Entität und der
Beobachtungsverlauf je Entität oben. Jeder Read
erfordert eine mandantenbezogene, namespaced Leseberechtigung (die niedrigste
Viewer-Stufe genügt). Katalog- und Provenienz-Writes sind hochfrequent und
werden nicht pro Write auditiert.

Die dauerhafte Frische hält ein zweites Overlay, `inventory.freshness_sweep`:
höchstens eine lazy erzeugte Zeile je Mandant mit dem Cutoff eines offenen
Zyklus, dem opaken Katalog-Cursor und dem letzten Cutoff, dessen Zyklus
endete. Dieser letzte Cutoff verzeichnet einen beendeten **Sweep**, nicht
dass irgendeine Quelle vollständig enumeriert wurde. Die vollständigen Formen
liegen in der [Event-Bus-Referenz](/de/reference/events/) und den typisierten
Schnittstellen des Produkts.

## Dauerhafte Frische

Ein periodischer Sweep markiert einen Katalogeintrag als `stale`, wenn diese
Plattform ihn seit dem Cutoff des Zyklus nicht gesehen hat, und setzt ihn in
dem Moment auf `active` zurück, in dem er wieder auftaucht. Die
Standardschwellen sind 30 Minuten Stille und eine 5-Minuten-Kadenz; das sind
Modul-Defaults, keine Operator-YAML-Oberfläche im aktuellen Binary (der
Composition-Root registriert das Modul mit leeren Host-Settings).

Das Modul enumeriert **keine** Mandanten und fällt **nicht** auf die Mandanten
zurück, die dieser Prozess zufällig beobachtet hat. Ein privater
Composition-Adapter liest das dauerhafte Organisationsverzeichnis und liefert
nur **Kandidaten**: aktive Business-Mandanten, die die Residenz dieser Instanz
bedient — nie die System-Partition, nie eine suspendierte Org, nie einen
Mandanten, der an eine Region gepinnt ist, die diese Instanz nicht bedient.
Jeder Turn öffnet weiterhin den gewöhnlichen tenant-bezogenen Store-Write,
sodass Residenz, Dienstentzug und Leadership erneut geprüft werden; ein
Mandant, dessen Zustand zwischen Snapshot und Turn wechselte, wird dort
abgelehnt.

Ohne ein autoritatives Verzeichnis schlägt der Sweep sichtbar fehl und mutiert
nichts. Unter PostgreSQL braucht diese Autorität den dedizierten
`NOSUPERUSER`-`BYPASSRLS`-Admin-Lese-Pool, der bereits für verzeichnisweite
Reads zwischen Mandanten verwendet wird (`--admin-dsn`). Ohne diesen
attestierten Pool behandelt der Sweep die Zeilen, die die Anwendungsrolle
sehen kann, nicht als vollständiges Verzeichnis. SQLite hat keine
entsprechende Pool-Anforderung. Neue Beobachtungen werden weiter persistiert,
wenn der Data-Handle verdrahtet ist; ein Sweep, der nicht laufen kann, sagt
die Ingestion nicht ab.

Jeder Pass gibt jedem Kandidaten **einen Turn**. Jeder Turn klassifiziert
höchstens eine Seite von 1.000 aktiven Einträgen älter als der Cutoff und
speichert den Cursor, sodass ein Neustart ohne ein neues Event fortsetzt.
Enumeration und jeder Mandanten-Turn haben **getrennte Zeitbudgets**, sodass
ein langsamer Mandant nicht den Rest des Passes verbraucht. Ein lokaler
Fehler hindert spätere Mandanten nicht daran, in einem **lebenden Prozess**
einen Turn zu erhalten. Es gibt keinen mandantenübergreifenden
Neustart-Cursor, keine Fairness unter wiederholten Neustarts und keine
Hochverfügbarkeitsbehauptung.

Wenn ein Turn nicht erfolgreich abschließt, wird diese Seite nicht als
markiert gezählt. Seite und Fortschritt bleiben zusammen; der nächste Turn
liest erneut, was zuletzt gespeichert wurde — die vorherige Grenze oder eine
bereits fortgeschrittene — und macht von dort weiter. Das Produkt
rekonstruiert den Fortschritt nicht aus dem Speicher.

## Was es konsumiert und produziert

Modul I ist ein **Consumer**. Es abonniert `edge.observed` und `cost.sampled`
und schreibt sein Katalog-Overlay, die Kernentitäten, die es ableitet, sowie
die additiven Provenienz- und Frische-Zeilen oben. Es emittiert keine eigenen
Events und exponiert keine Aktuierungsoberfläche. Die Referenzen und ausgewählten Beobachtungsfakten, die es persistiert,
werden so gespeichert, wie sie eintreffen; das Modul bereinigt diese Werte
nicht weiter. Datenminimierung liegt beim Produzenten und muss vor der
Veröffentlichung von Beobachtungen erfolgen; dieses Modul kann sie nicht für
jeden Produzenten zertifizieren. Eine persistierte Referenz kann eine Query,
Anmeldedaten oder andere sensible Daten behalten, wenn ein Produzent sie
veröffentlicht hat. Das Inventar sammelt nicht eigenständig den vollständigen
Rohinhalt der Aktivität und fügt kein eigenes Roh-Payload, Secret, Prompt,
Befehl oder SQL hinzu.

:::caution[Ehrliche Grenzen]
- **Das Inventar besitzt nicht den Access-Graphen.** Seit Entscheidung A
  (2026-06-03) ist Modul III (die Access Map) der **alleinige Schreiber** der
  Read/Write-`AccessEdge` und der einzige Eigentümer der Topologie und des
  Permitted-vs-Observed-Diffs. Das Inventar entdeckt und katalogisiert die
  *Entitäten*, die ein Edge benennt; es erfasst den Edge selbst nicht mehr und
  bedient keine Topologie-Route. Der Graph wird nur befüllt, wenn Modul III
  beim Boot verdrahtet ist.
- **Discovery ist nur so vollständig wie die Signale.** Eine Entität existiert
  im Katalog nur, wenn ein Connector sie beobachtet hat. Das Fehlen im Katalog
  ist **kein** Beweis für das Fehlen im Estate. Das Abschließen eines
  Frische-Sweeps ist keine Quellenabdeckung, keine Vollständigkeit der
  Discovery, keine formale Gesundheit und kein Beweis, dass eine Entität
  entfernt wurde.
- **Lebendigkeit ist Staleness, nicht Gesundheit.** `stale` bedeutet, dass
  diese Plattform die Entität seit dem Cutoff nicht beobachtet hat, nicht
  mehr. Wiedererscheinen setzt sie auf `active` zurück. Die Stille einer
  Session ist normal, und formale Gesundheit/SLA gehören zu Modul XXII. Der
  Sweep mutiert nie den eigenen Lebenszyklus der Kernentität.
- **Keine erfundenen Details.** Das Modul speichert Identifikatoren,
  Beziehungen und Lebendigkeitszähler — niemals eigenständig gesammelten
  vollständigen Rohinhalt der Aktivität — und fügt keine eigenen Payloads,
  Secrets, Prompts, Befehle oder SQL hinzu. Empfangene Ressourcen-URIs dürfen
  als Referenzen gespeichert werden. Das Inventar bereinigt diese Werte nicht
  nachträglich und zertifiziert nicht die Abwesenheit von Query-Strings,
  Anmeldedaten oder personenbezogenen Daten darin.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul I einzuordnen ist und die ehrliche Actuate-Aufteilung.
- [Modul III — die Access Map](/de/reference/modules/iii-access-map/) — der alleinige Eigentümer des R/RW-Graphen und der Drift.
- [Event-Bus-Referenz](/de/reference/events/) — die Events `edge.observed`, `cost.sampled` und `finding.reported`, die Inventar und Sessions konsumieren.
- [Von Null zum Graphen](/de/tutorials/zero-to-graph/) — den Katalog und die Map auf dem Demo-Estate befüllen.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine, die Schichten und der Bus.
