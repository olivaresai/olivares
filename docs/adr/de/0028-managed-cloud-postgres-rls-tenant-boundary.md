> Maschinell übersetzt. Maßgeblich ist die englische Fassung.

# ADR-0028: Managed-Cloud-Datenbank — verwaltetes PostgreSQL mit Row-Level Security als Mandantengrenze

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0005 (SQLite by default, PostgreSQL at scale), ADR-0027
  (managed-cloud ingress), ADR-0029 (managed-cloud regions), ADR-0022 (source-scoping
  subject axes); the platform decision record for the managed cloud; PostgreSQL
  documentation on row security policies and the AWS database guidance on multi-tenant
  isolation with row-level security, consulted 2026-08-02:
  `https://aws.amazon.com/blogs/database/multi-tenant-data-isolation-with-postgresql-row-level-security/`.

## Kontext und Problemstellung

ADR-0005 legte PostgreSQL bereits als Datenbank des Produkts für den Betrieb in großem
Maßstab fest, und das Produkt verfügt bereits über die Row-Level-Security-Mechanismen für
das Tenant-Scoping. Die Managed Cloud benötigt kein neues Datenmodell; sie benötigt eine
Entscheidung darüber, **wer die Datenbank betreibt**, und darüber, **worauf genau wir uns
verlassen, damit die Zeilen eines Mandanten von denen eines anderen getrennt bleiben**.

Die zweite Hälfte ist wichtiger als die erste. „Wir verwenden Row-Level Security“ ist erst
dann eine Eigenschaft, wenn die Rollen so angeordnet sind, dass die Policies tatsächlich
greifen. PostgreSQL nimmt zwei Kategorien von Aufrufern von Tabellen-Policies aus:
Superuser und Rollen mit dem Attribut `BYPASSRLS` — und standardmäßig **umgeht der
Eigentümer einer Tabelle RLS und unterliegt ihren Policies überhaupt nicht**, es sei denn,
die Tabelle wird mit `FORCE ROW LEVEL SECURITY` geändert. Eine Anwendung, die sich mit der
Rolle verbindet, die das Schema erstellt hat, verfügt daher über *keine* Mandantenisolation,
obwohl es so aussieht. Dies ist der mit Abstand kostspieligste Fehler, der in diesem Entwurf
möglich ist, und er bleibt unbemerkt.

## Entscheidungstreiber

- Die Mandantenisolation muss **von der Datenbank** durchgesetzt werden, nicht durch die
  Sorgfalt bei jeder künftigen Abfrage.
- Der alleinige Operator sollte PostgreSQL nicht selbst betreiben: Patching, Failover und
  Point-in-Time Recovery sind genau die Arbeiten, die das Managed-Angebot abnehmen soll.
- Recovery muss eine Eigenschaft der Plattform sein und darf nicht von einem Runbook
  abhängen, an dessen Ausführung sich jemand erinnern muss.
- Jede Aussage über Isolation muss **von außerhalb der Anwendung testbar** sein.

## Betrachtete Optionen

- **A — selbstverwaltetes PostgreSQL auf virtuellen Maschinen.** Volle Kontrolle und die
  niedrigsten Stückkosten; dafür liegen jedes Upgrade, jede Failover-Übung und jede
  Backup-Verifizierung bei uns.
- **B — der verwaltete PostgreSQL-Dienst des Cloud-Anbieters, Multi-AZ**, mit automatisierten
  Backups und Point-in-Time Recovery.
- **C — der PostgreSQL-kompatible Cluster-Dienst des Anbieters** (Shared-Storage-Architektur,
  I/O-Abrechnung je Anfrage in der Standardkonfiguration).
- **D — eine PostgreSQL-Plattform eines Drittanbieters**, die aus derselben Region erreichbar
  ist.

## Entscheidungsergebnis

Gewählte Option: **B — verwaltetes PostgreSQL, Multi-AZ**, mit Row-Level Security als
Mandantengrenze. Die nachstehende Rollenaufteilung wird als Teil der Entscheidung und nicht
als Implementierungsdetail behandelt.

Die Rollenaufteilung ist normativ:

1. Die Anwendung verbindet sich mit einer Rolle, die die mandantenspezifischen Tabellen
   **nicht besitzt** und **nicht über `BYPASSRLS` verfügt**.
2. Jede mandantenspezifische Tabelle trägt **`FORCE ROW LEVEL SECURITY`**, sodass
   Eigentümerschaft allein keine Policy umgehen kann — dies schützt vor einer künftigen
   Migration, die den Eigentümer einer Tabelle ändert.
3. Die für Migrationen verwendete administrative Rolle ist nicht die Rolle im
   Connection-String der Anwendung.
4. **Geltungsbereich, ausdrücklich festgehalten, damit er nie vorausgesetzt wird:** Dieser
   Eintrag regelt die **Mandanten-Datenebene** — das Schema mit mandanteneigenen Zeilen, für
   das die Engine bereits `ENABLE ROW LEVEL SECURITY`, `FORCE ROW LEVEL SECURITY` und eine
   an eine Session-Einstellung gebundene Policy pro Mandant ausgibt. Die **eigenen
   Kontrollmetadaten** der Managed Plane (Mandantenregister, Abrechnungsledger,
   Nutzungssnapshots) liegen in einem **separaten Schema mit einer separaten Haltung**: Heute
   stützen sie sich auf Scoping auf Anwendungsebene mit einer einzigen Anwendungsrolle und
   ohne für Mandanten zugängliches SQL. Diese Haltung mag für Kontrollmetadaten durchaus die
   richtige Antwort sein — gegenwärtig ist sie jedoch **geerbt statt entschieden**, und sie
   entspricht nicht dem, was Leser unter „wir verwenden Row-Level Security“ verstehen. Wer
   die Managed Plane baut, muss **schriftlich festhalten, welche Haltung dieses Schema hat
   und warum**, bevor es Datensätze eines zahlenden Kunden enthält.

### Konsequenzen

- **Gut:** Patching, Multi-AZ-Failover, automatisierte Backups und Point-in-Time Recovery
  werden zu Plattform-Eigenschaften. Das mit dem Produkt ausgelieferte
  Disaster-Recovery-Runbook bleibt das Artefakt für Self-Hosted-Deployments; es ist aber
  keine tägliche betriebliche Pflicht der Managed Plane mehr.
- **Gut:** Isolation wird von außen testbar. Das Abnahmekriterium ist eine **als
  Anwendungsrolle ausgeführte** Abfrage, die versucht, Zeilen eines anderen Mandanten zu
  lesen, und keine erhält — keine Behauptung in einem Entwurfsdokument.
- **Schlecht / Abwägungen:** eine höhere feste monatliche Untergrenze als bei einer einfachen
  virtuellen Maschine; außerdem folgen Upgrades der Engine-Version dem Kalender des
  Anbieters statt unserem.
- **Neutral:** Die administrative Rolle des verwalteten Dienstes ist eine privilegierte
  Datenbankrolle, **aber kein PostgreSQL-Superuser** — sie hat keinen Zugriff auf das
  Betriebssystem und kann die Host-Authentifizierungskonfiguration nicht umschreiben. Das
  reduziert den Blast Radius sinnvoll, ist aber nicht der Grund dafür, dass Row-Level
  Security greift; entscheidend ist die oben beschriebene Rollenaufteilung.
- **Explizit NICHT verifiziert und nicht vorauszusetzen:** ob diese administrative Rolle auf
  der laufenden Engine über `BYPASSRLS` verfügt. Dies lässt sich mit einer einzigen Abfrage
  (`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user;`) gegen eine reale
  Instanz prüfen und gehört in die Phase, in der erstmals eine solche Instanz erstellt wird.
  Bis die Abfrage ausgeführt wurde, darf kein Dokument behaupten, dass die administrative
  Rolle den Mandanten-Policies unterliegt.

## Warum die Alternativen verworfen wurden

- **A (selbstverwaltetes PostgreSQL)** — abgelehnt, weil es genau die betriebliche Last an
  uns zurückgibt, die die Managed Plane abfangen soll, und sie auf einen Operator
  konzentriert: Versions-Upgrades, Failover-Übungen und eine Backup-Verifizierung, die nur
  dann real ist, wenn jemand regelmäßig eine Wiederherstellung daraus durchführt. Der
  Kostenvorteil ist real und absolut betrachtet gering; das Betriebsrisiko ist dagegen alles
  andere als gering.
- **C (PostgreSQL-kompatibler Cluster-Dienst)** — als verfrüht abgelehnt. Der Workload ist
  ein kleines transaktionales Schema mit einer moderaten Schreibrate. Die
  Shared-Storage-Architektur löst Skalierungsprobleme, die dieser Workload nicht hat, bei
  einer höheren Untergrenze und I/O-Abrechnung je Anfrage in der Standardkonfiguration. Sie
  bleibt der natürliche Upgrade-Pfad, falls die Schreibrate dies jemals rechtfertigt.
- **D (PostgreSQL-Plattform eines Drittanbieters)** — für den primären Store abgelehnt. Das
  Verhalten von Row-Level Security, das Superuser-Modell und die verfügbaren Rollenattribute
  unterscheiden sich je nach Anbieter und müssten jeweils erneut anhand der oben
  beschriebenen Isolationseigenschaft verifiziert werden. Es gibt keinen Grund, an der einen
  Grenze, die nicht versagen darf, ein anbieterspezifisches Risiko einzugehen.
