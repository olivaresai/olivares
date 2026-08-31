---
title: "Modul XX — Mandantenfähigkeit & Org-Verwaltung"
description: >-
  Das Fundament der Isolation: Jede Kernentität trägt eine tenant_id, und der
  Store weigert sich zu öffnen, solange diese Grenze nicht auf der Query-Ebene
  durchgesetzt wird. Was das Datenmodell heute garantiert und was Org-Hierarchie
  und delegierte Administration noch sind.
---

Modul XX ist kein Dienst, der an der Engine hängt — es ist eine **Eigenschaft der
Engine selbst**. Es gibt kein separates Mandanten-Modul zum Anbinden; stattdessen
trägt das Kern-Datenmodell auf jeder Entität eine Mandantengrenze, und der Store
setzt sie unterhalb jeder Query durch. Diese Seite ist die Referenz dafür, was diese
Grenze heute garantiert, und für die Teile der Org-Verwaltung, die sich noch im
Entwurfsstadium befinden.

## Was es ist

Mandantenfähigkeit lebt in der Engine-Schicht (Schicht 0), zusammen mit der eigenen
API der Plattform (Modul XIX), denn Isolation nachträglich in ein laufendes
Datenmodell einzubauen ist die Art von Änderung, die man später nicht mehr sicher
durchführen kann. Jede Kernentität trägt eine **`tenant_id`**, und ein Aufrufer
übergibt sie nie als freien Parameter: Er **fixiert den Mandanten einmalig** und
erhält einen Scope, dessen Repositories bereits an ihn gebunden sind. Es gibt **in der
API kein Vokabular, um Mandanten zu überschreiten** — diese Abwesenheit ist die erste
Isolationsbarriere, noch vor jedem Datenbankmechanismus. Der privilegierte
mandantenübergreifende Scope (eine Org anlegen, Orgs auflisten, einen Mandanten
verwerfen) ist **ausschließlich über den eigenen Startvorgang der Engine** erreichbar,
niemals durch ein Modul.

## Der Vertrag und die Entitäten

Das Mandantenmodell gehört dem Datenmodell-Vertrag, nicht einem modulspezifischen
Schema. Die Wurzelentität ist die **`Org`**, die *der* Mandant ist: Wenn die Engine
eine Org initialisiert, wird deren Identifier zum Mandanten-Identifier, und die eigene
Audit-Kette der Org wird im selben Moment etabliert. Jede andere Kernentität — Agenten,
Sessions, Ressourcen, Identitäten, Policies, Kostendatensätze, Findings, Deployments,
die Access Map und das Audit-Ledger — wird **innerhalb** eines Mandanten-Scopes
angelegt und beim Schreiben mit diesem Mandanten gestempelt; der Aufrufer kann das
nicht überschreiben.

Die Isolation wird auf der Query-Ebene durchgesetzt, je nach Deployment:

- Auf **PostgreSQL** läuft jede Tabelle mit `tenant_id` unter `FORCE ROW LEVEL
  SECURITY` mit einer pro Transaktion gebundenen `tenant_isolation`-Policy. Eine
  Transaktion, die keinen Mandanten bindet, **löst einen Fehler aus**, statt
  stillschweigend null Zeilen zurückzugeben (fail-closed). Die Anwendungsrolle ist kein
  Superuser und hat niemals `BYPASSRLS`, und `FORCE` bindet die Policy auch an den
  Tabelleneigentümer. Die Eigentümerschaft ist eine Deployment-Entscheidung: Die
  standardmäßige Single-Role-Installation belässt die Anwendungsrolle als Eigentümerin der
  Datenbank — RLS bindet sie weiterhin, aber eine Eigentümerin kann ihre eigenen Tabellen
  ändern, diese Haltung ist also manipulationsevident (tamper-evident) und nicht
  eigentümerfest. Die harte Privilegiengrenze — eine Anwendungsrolle, die zusätzlich nicht
  Eigentümerin ist — ergibt sich aus der geteilten Owner/App-Topologie, in der eine
  separate Owner-Rolle das Provisioning ausführt und die Anwendungsrolle nur das DML
  erhält, das sie braucht.
- Auf **SQLite** (dem Single-Node-Deployment) gibt es keine Row-Level-Security; die
  Äquivalenz ergibt sich aus zwei Tatsachen — der *einzige* Pfad zur Datenbank ist das
  deskriptorgenerierte SQL, das stets das Mandantenprädikat anhängt, und **Tripwire-
  Trigger** brechen jeden Schreibvorgang ab, dessen Mandant nicht zum gepinnten Scope
  passt.

Ein **Startup-Selbsttest** fragt nach der Migration die aktiven Isolations-Wächter ab
und **weigert sich, den Store zu öffnen**, falls irgendeine Tabelle mit `tenant_id`
ungeschützt ist — so wird ein vergessener Wächter auf einer neuen Tabelle zu einem
Boot-Fehler, nicht zu einem stillen Leck.

## Was es konsumiert und produziert

Modul XX hat keine Event-Bus-Oberfläche und keine Aktuierung. Es konsumiert kein
`edge.observed`, gibt keine Findings aus und ruft keinen Provider auf — es ist das
Substrat, *durch* das die anderen Module schreiben. Sein einziger beobachtbarer Effekt
ist struktureller Natur: Jede Entität, die irgendein Modul persistiert, ist bereits
mandantengebunden, und jede Mutation an einer auditierten Entität hängt innerhalb
derselben Transaktion an das [hash-verkettete Audit-Ledger](/de/reference/events/) dieses
Mandanten an.

:::caution[Ehrliche Grenzen]
- **Was das Datenmodell tatsächlich modelliert, ist `Org`-als-Mandant + die
  Isolationsgrenze** — nicht die vollständige Org-Hierarchie. **Teams, Projekte,
  delegierte Administration, ebenenspezifische Rollen sowie Nutzung/Abrechnung pro Org
  befinden sich im Entwurfsstadium**, sind keine ausgelieferten Entitäten. Betrachten
  Sie die Mandantengarantie des Produkts heute als: *eine Org = ein isolierter Mandant,
  durchgesetzt auf der Query-Ebene.*
- **Lese-Isolation auf SQLite erfolgt durch die Query-Ebene, nicht durch die Engine.**
  SQLite hat keine Row-Level-Security: Lese-Scoping ist eine Eigenschaft des generierten
  SQL (Schreibvorgänge sind zusätzlich durch die Tripwire-Trigger abgedeckt).
  Mandantenfähigkeit **im großen Maßstab ist PostgreSQL mit RLS** als Absicherung auf
  Kernel-Ebene; SQLite ist das Single-Node- / Air-Gapped-Deployment.
- **Der mandantenübergreifende Admin-Scope ist auf PostgreSQL deployment-abhängig.**
  Orgs mandantenübergreifend aufzulisten erfordert auf PostgreSQL eine dedizierte
  Admin-Rolle und betrifft das Deployment, nicht den Anwendungscode. Auf SQLite
  (Single-Writer) funktioniert es direkt.
- **Mandantenfähigkeit ist keine delegierte Administration.** Wer *innerhalb* eines
  Mandanten handeln darf — Rollen, Freigaben, Funktionstrennung — wird von
  [Modul VI](/de/reference/modules/vi-governance/) geregelt, nicht hier. Modul XX
  garantiert die Wand zwischen Mandanten; Modul VI bewacht die Tür innerhalb eines
  Mandanten.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul XX einzuordnen ist und sein ehrlicher Aktuierungsstatus.
- [Identität, Berechtigungen & Governance](/de/reference/modules/vi-governance/) — Rollen und delegierte Autorität innerhalb eines Mandanten.
- [Architekturüberblick](/de/explanation/architecture/overview/) — die Engine-Schicht und das allgemeine Datenmodell.
- [Event-Bus-Referenz](/de/reference/events/) — das mandantenspezifische Audit-Ledger, an das jede Mutation anhängt.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — was heute gebaut ist im Vergleich zum Entwurfsstadium.
