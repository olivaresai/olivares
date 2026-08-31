---
title: "Modul VI — Identität, Berechtigungen & Governance"
description: >-
  Die Control Plane über das Autorisierungsmodell: Reconciliation des
  Identitäts-Rosters, die Agent↔Identität-Brücke, die Deny-only-ABAC-Engine und das
  Human-in-the-loop-Freigabe-Gate mit einem Append-only-Entscheidungspfad. Die Wurzel
  gouvernierter Aktuierung.
---

Modul VI ist die **Governance-Plane über dem bestehenden Autorisierungsmodell der
Engine** — es implementiert den Enforcer oder die Identitäts-Connectors **nicht** neu,
es konsumiert sie. Es bindet fünf Subsysteme hinter einem Bounded Context (Identität
und ihre Governance): einen Verzeichnis-**Roster**-Reconciler, die
**Agent↔Identität-Brücke**, die Attribution belastbar macht, eine Deny-only-**ABAC**-
Engine, das **Human-in-the-loop**-Freigabe-Gate und Policy-/Identitäts-**Authoring-
Backends**. Dies ist die Wurzel jeder *gouvernierten* Aktion im Produkt.

## Was es ist

Das Modul sitzt auf dem Management-Layer und ist die **Entscheidungs**-Autorität für
die Control Plane: wer und was was tun darf, und welche Aktionen zuerst einen Menschen
erfordern. Sein Vertrag ist die durchsetzbar gemachte Deny-only-, Deny-by-default-
Haltung —

- **Roster-Reconciliation** konvergiert ein verbundenes Verzeichnis (die
  Identitätsquellen) in die kanonischen `Identity`-Entitäten der Engine plus den
  modul-eigenen Collection-/Membership-Graphen, find-or-create geschlüsselt allein auf
  die External ID, sodass es **dieselbe Zeile aktualisiert**, die die Access-Map aus
  einer Audit-Referenz erzeugt. Diese Single-Row-Konvergenz ist es, die belastbare
  Attribution möglich macht.
- **Die Agent↔Identität-Brücke** bindet einen Agenten an die interne ID der
  kanonischen nicht-menschlichen Identität, die sein Credential präsentiert, und löst
  damit die harte Abhängigkeit auf, die es Modul III (der Access-Map) erlaubt, falschen
  Permitted-vs.-Observed-Drift zu annullieren.
- **Die ABAC-Engine** ist ein nativer Evaluator, der **nach** RBAC läuft und nur
  *weiter einschränken* kann — er weitet einen Grant niemals aus.

## Sein Vertrag & seine Entitäten

Modul VI besitzt vier Entitäten im geteilten Datenmodell — eine **Collection** und
eine **Collection-Member**-Kante (der quellen-abgeleitete Group-/Role-Graph,
transitiv innerhalb von Grenzen aufgelöst), eine **Approval** (eine veränderbare
HITL-Anfrage) und einen Append-only-**Approval-Decision**-Pfad. Identitäten werden
**nicht** in eine Modultabelle dupliziert; sie werden in die kanonische
`Identity`-Entität der Engine reconciliiert.

Der **ABAC-Evaluator** implementiert den Policy-Evaluator-Seam der Engine mit
verifizierten Eigenschaften: Jede Regel ist eine **Deny**-Regel; er läuft nach RBAC
innerhalb eines AND, sodass eine Policy den Zugriff niemals erweitern kann; eine
fehlerhafte *aktivierte* Policy **schlägt geschlossen fehl** (verweigert); der
Autorisierungs-Hotpath wird aus einem Per-Tenant-Cache bedient, der **nach** dem
Commit eines Writes invalidiert wird, strikt nach Tenant isoliert. Policy-Specs werden
beim Write **typisiert und neu marshalliert** (Operator-JSON wird niemals wortgetreu
durchgereicht), sodass kein Credential in eine Spec gelangen kann. OPA/Rego ist der
External-Evaluator-Seam, niemals eine in die Engine geschleppte Abhängigkeit.

Das **Freigabe-Gate** ist die Action→Mensch-Nachvollziehbarkeit, die das Audit-Ledger
verankert: Separation-of-Duty und der Duplicate-Decider-Guard schlüsseln auf die
**stabile Benutzeridentität** (ein System-Token kann nicht entscheiden), der
Multi-Approval-Schwellenwert ist auf dem Store **race-safe** (eine nebenläufige
Überschreitung löst sich zu genau einem Gewinner auf), und Ablauf wird beim Lesen lazy
abgeleitet und dann durch einen expliziten, tenant-scoped Sweep materialisiert. Die
Authoring-Backends (Managed-Settings/Hooks, Cedar/OPA Policy-as-code, der
WIF-Objektgraph) fügen einen **Publish→Immutable-Revision→Drift**-Write-Pfad hinzu;
für Cedar wird eine veröffentlichte Policy auf dem laufenden Per-Tenant-Deny-only-
Overlay aktiviert und beim Boot neu geladen, sodass ein `active`-Claim einen Neustart
überdauert.

## Was es konsumiert & produziert

Das Modul **konsumiert** die Autorisierungs- und Audit-Basis der Engine sowie den
typisierten Identitäts-Roster aus den konfigurierten Verzeichnisquellen; es füllt das
`Agent.IdentityID`-Feld, von dem die Access-Map abhängt. Es **produziert**
`FindingReport`-Events auf dem [Event-Bus](/de/reference/events/) — eine **geteilte
Identität**, die an mehr als einen Agenten gebunden ist, plus Approval-**Eskalation**
und **-Ablauf** — jeweils einmal emittiert, gegatet auf einen persistierten Marker,
sodass ein wiederholter Sweep nicht doppelt emittieren kann. Jede privilegierte
Mutation und die recon-relevanten Identitäts- und Bindungs-Reads **self-auditen zum
realen Principal** innerhalb einer committeten Transaktion; der Audit-Akteur ist immer
eine typisierte Principal-Referenz, niemals eine E-Mail.

:::caution[Ehrliche Grenzen]
- **Die ABAC-Engine ist authored und auditiert, aber die Durchsetzung hängt von der
  Komposition ab.** Governance-State wird heute geschrieben und auditiert; der
  Boot-Kompositions-Root verdrahtet den Evaluator und injiziert die
  Directory-Provider. Wo diese nicht verdrahtet sind, ist die Engine nicht in Kraft
  und ein Roster-Sync hat keine Provider — dies ist **benannt, niemals ein stilles
  No-op**.
- **Belastbare Attribution erfordert Identität-pro-Agent.** Eine Bindung knüpft einen
  Agenten an eine *kanonische* Identität, niemals an eine frisch erzeugte, die genutzt
  wird, um die Reconciliation einer geteilten Entität vorzutäuschen. Eine Identität,
  die an mehr als einen Agenten gebunden ist, **kollabiert die Attribution** auf die
  Identitätsebene — ehrlich als Finding ausgewiesen, niemals wiederhergestellt.
- **Die Deny-only-Grammatik ist by design begrenzt.** v1-Regeln matchen nur die
  Attribute, die den Evaluator tatsächlich erreichen; Resource-Attribute-Regeln (z. B.
  Sensitivität) benötigen einen Kern-Seam und sind ein dokumentiertes Follow-up —
  **nicht als inerte Syntax ausgeliefert**, ein unbekanntes Feld wird beim Write
  zurückgewiesen. Policy *schränkt ein*; additive Grants bleiben in RBAC.
- **Ein Modul kann keine Tenants enumerieren.** Approval-Ablauf/-Eskalation wird durch
  einen **expliziten, tenant-scoped Sweep** materialisiert — es gibt keine
  Background-Cross-Tenant-Garantie, denn eine solche zu behaupten wäre eine Lüge. Der
  effektive Ablauf wird beim Lesen weiterhin lazy berücksichtigt.
:::

## Verwandt

- [Modulkatalog](/de/reference/modules/overview/) — wo Modul VI sitzt und sein ehrlicher
  Aktuierungsstatus.
- [Zugriffs- & Ressourcen-Map (III)](/de/reference/modules/iii-access-map/) — der
  Konsument, dessen Attributions-Abhängigkeit dieses Modul auflöst.
- [Event-Bus-Referenz](/de/reference/events/) — das `finding.reported`-Event, das dieses
  Modul emittiert.
- [Gouvernieren und freigeben](/de/how-to/govern-and-approve/) — die Nutzung der Policy-
  und Approval-Flächen.
- [Architektur-Überblick](/de/explanation/architecture/overview/) — die Engine und Layer,
  auf die dieses Modul aufkomponiert.
- [Ehrlichkeit & Grenzen](/de/start/honesty-and-limits/) — die Deny-closed-, Detective-
  by-default-Haltung.
