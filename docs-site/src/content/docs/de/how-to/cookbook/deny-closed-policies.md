---
title: "Rezept: deny-closed-Policies (Cedar / OPA)"
description: >-
  Den Restrict-only Policy Decision Point verdrahten: ein Cedar-forbid-Overlay
  oder eine permit-by-default OPA-Policy, validiert und im Dry-Run getestet vor
  der Veröffentlichung — Policies, die Zugriff nur entziehen, niemals erweitern
  können.
sidebar:
  order: 1
---

**Ziel:** attributbasierte Restriktionen oberhalb von deny-by-default RBAC
hinzufügen — zum Beispiel „niemand fasst Ressourcen mit dem Tag `secret` an,
ganz gleich, was seine Rolle erlaubt".

Die eine Invariante, die man im Kopf behalten muss: der PDP **restringiert
nur**. Die Entscheidung setzt sich als RBAC ∩ natives ABAC ∩ externer PDP
zusammen — eine Policy kann niemals gewähren, was das Rollenmodell verweigert
([das Modell](/de/how-to/govern-and-approve/#die-policy-naht-abacpdp-schränkt-nur-ein)).

## Cedar (eingebettet, primär)

Die Engine auswählen, auf die Policy-Datei verweisen und neu starten:

```bash
OLIVARES_PDP_ENGINE=cedar
OLIVARES_PDP_CEDAR_FILE=/etc/olivares/policy.cedar
```

Eine Cedar-Policy ist ein **forbid-Overlay** — das Basis-`permit` steht für
„RBAC hat bereits entschieden", und deine `forbid`-Regeln subtrahieren davon:

```cedar
permit(principal, action, resource);

forbid(principal, action, resource)
  when { resource.kind == "credential" && resource.sensitivity == "secret" };
```

Zwei Fakten zur Autorenschaft, gegen den Adapter verifiziert: `resource.kind`
und `resource.sensitivity` sind immer im Entscheidungs-Input vorhanden
(unbedingt referenzierbar); jedes andere Attribut muss mit `has()` abgesichert
werden, sonst kann die Regel nicht greifen. Ein `permit`, das du schreibst,
kann die Entscheidung niemals erweitern.

## OPA (über HTTP)

```bash
OLIVARES_PDP_ENGINE=opa
OLIVARES_PDP_OPA_URL=http://opa.internal:8181
OLIVARES_PDP_OPA_PATH=/v1/data/olivares/decision
OLIVARES_PDP_OPA_TOKEN=<bearer-reference>     # optional
```

Verfasse das Rego **permit-by-default**:

```rego
package olivares

default allow := true

allow := false if {
  input.resource.sensitivity == "secret"
  input.action == "read"
}
```

`true` = keine Restriktion. `false`, ein fehlendes Ergebnis oder **jeder
Transport- oder Nicht-2xx-Fehler scheitert geschlossen** (fail closed) — die
Anfrage wird verweigert, niemals stillschweigend ungoverned.

## Validieren, Dry-Run, Veröffentlichen

Das Governance-Modul stellt einen Policy-Lebenszyklus bereit, damit eine
fehlerhafte Policy niemals blind in Produktion landet:

```bash
# Compile-Check der Quelle:
curl -ks -X POST "$BASE/v1/m/governance/pdp/validate" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d @policy.json

# Eine Entscheidung OHNE Audit-Seiteneffekte vorab prüfen:
curl -ks -X POST "$BASE/v1/m/governance/pdp/dry-run" \
  -H "Authorization: Bearer $TOKEN" -H "X-Olivares-Tenant: $TENANT" \
  -d '{"principal":"…","action":"…","resource":{"kind":"credential","sensitivity":"secret"}}'

# Dann veröffentlichen (policy-admin-Berechtigung):
curl -ks -X POST "$BASE/v1/m/governance/pdp/publish" …
```

`GET /v1/m/governance/pdp/versions` listet auf, was deployt ist;
`POST /v1/m/governance/pdp/explain` erklärt eine Entscheidung.

## Die Sicherheitseigenschaften verifizieren

- Mit einer **ungültigen** Policy-Datei neu starten: die Engine deaktiviert
  nur den externen PDP und protokolliert das — RBAC und natives ABAC governen
  weiter; die Control Plane fällt nicht aus.
- Jede Restriktion, die der PDP anwendet, wird **auditiert** — prüfe das
  Ledger nach einer verweigerten Anfrage.

## Hinweise

- Policies werden versioniert und veröffentlicht, nicht als heiß editierte
  Dateien in Produktion betrieben — behandle die Veröffentlichung als
  geprüfte Änderung.
- Für genehmigungspflichtige (statt verweigerte) Aktionen siehe
  [HITL-Genehmigungen](/de/how-to/cookbook/hitl-approvals/).
