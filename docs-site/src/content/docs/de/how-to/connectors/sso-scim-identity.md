---
title: "SSO, SCIM & Identitätsquellen (feste Attribution)"
description: >-
  Binden Sie Enterprise-Identität durchgängig an: föderierter Konsolen-Login
  (OIDC/SAML über den Federation-Seam), SCIM-Provisionierung in die Control
  Plane und die LDAP- / Okta- / Entra-Roster-Quellen, die die
  Access-Map-Attribution von approximate auf firm hochstufen.
sidebar:
  order: 8
---

Identität ist die harte Abhängigkeit unter der gesamten Access Map: natives
Audit attribuiert einen Zugriff einem **Credential**, und nur ein
Identitäts-Roster kann dieses Credential an einen **Agenten oder eine Person**
binden. Diese Seite bindet die drei Identitätsoberflächen an: Konsolen-**SSO-Login**,
**SCIM-Provisionierung** in die Control Plane und die **Roster-Quellen** (LDAP,
Okta, Entra ID), die die Attribution `attributed` statt `approximate` machen.

## 1. Konsolen-SSO (OIDC / SAML)

Föderierter Login wird über den **Federation-Seam** der Engine ausgeliefert.
Die Posture ist konstruktionsbedingt ehrlich:

- Die Endpunkte des Login-Flows existieren in jedem Build, und die Engine hält
  jeden geheimnisführenden Flow-Wert serverseitig — den CSRF-State, die
  OIDC-Nonce, den PKCE-Verifier (nur die S256-*Challenge* geht an den
  Provider). Authorization Code + **PKCE ist immer aktiv**.
- Der Standard-Build liefert den `NoFederation`-Provider: beide Endpunkte geben
  `501 sso_not_configured` zurück — die Oberfläche wird ehrlich beworben, ohne
  angebundenen IdP. Der Federation-Provider, der das Protokoll vervollständigt,
  ist Teil des Enterprise-Builds und wird **per Umgebung beim Boot
  konfiguriert** (`OLIVARES_SSO_PROTOCOL`, das `OLIVARES_OIDC_*`-Set für OIDC,
  das `OLIVARES_SAML_*`-Set für SAML).
- Die Redirect-/ACS-URI, die Ihr IdP führen muss, ist **exakt**
  (`…/v1/auth/federation/callback` auf Ihrem Konsolen-Origin — RFC-9700-Exact-Matching,
  keine Präfix-Tricks).

Der Konsolen-Tab **Identity & NHI → SSO & SCIM** dokumentiert die
Live-Konfiguration, prüft die Redirect-URI Ihres IdP gegen den exakt
erwarteten Wert und zeigt den Verbindungszustand — und wo das Backend eines
Panels ein deklarierter Vertrag ist, der noch nicht live ist, sagt es
„backend pending“, statt fabrizierte Daten zu rendern:

<img class="light:sl-hidden" src="/console/identity-dark.png" alt="Die Identity-&-NHI-Ansicht: SSO-Konfiguration mit exakter Redirect-URI-Prüfung, das NHI-Roster und Schlüssel-Posture-Tabs." />
<img class="dark:sl-hidden" src="/console/identity-light.png" alt="Die Identity-&-NHI-Ansicht: SSO-Konfiguration mit exakter Redirect-URI-Prüfung, das NHI-Roster und Schlüssel-Posture-Tabs." />

## 2. SCIM-Provisionierung (inbound)

Die Control Plane ist ein standardkonformer SCIM-2.0-Service-Provider (RFC
7644) unter:

```
/v1/scim/v2/Users
/v1/scim/v2/Groups
```

- **Auth:** ein tenant-gebundenes **Admin-/Owner-API-Token** auf der
  SCIM-Integration — dasselbe Opaque-Token-Modell wie der Rest der API, kein
  separater SCIM-Geheimnistyp. Der Endpunkt ist immer vorhanden (nicht
  feature-gated).
- **Users** provisioniert und deprovisioniert Principals; das Deprovisionieren
  durch Ihren IdP entzieht den Zugriff in dem Moment, in dem HR es sagt.
- **Groups** führt Identitäts-zu-Gruppe-Referenzdaten. Jede Gruppe kann über
  `mapped_role` auf eine Control-Plane-Rolle abgebildet werden — und dieses
  Mapping ist **operator-eigen**: es wird auf der Control-Plane-Seite gesetzt
  und auditiert (`scim.group.role.map`); ein IdP-Push eskaliert nie still eine
  Rolle. Unbekannte Mitglieder in einer gepushten Gruppe werden übersprungen
  **und auditiert**, nicht erfunden.

## 3. Roster-Quellen: LDAP, Okta, Entra ID

Roster-Quellen speisen das Identitätsinventar von Modul VI und — das ist der
Punkt — geben Modul III die Bindungen, die die Attribution hochstufen:

```json
{
  "sources": [
    {
      "name": "corp-ldap",
      "kind": "ldap",
      "tenant": "<tenant-id>",
      "config": {
        "url": "ldaps://ldap.corp.example:636",
        "bind_dn": "cn=olivares-ro,ou=svc,dc=corp,dc=example",
        "bind_password": "<reference>",
        "base_dn": "dc=corp,dc=example"
      }
    },
    {
      "name": "okta",
      "kind": "idp",
      "tenant": "<tenant-id>",
      "config": { "provider": "okta", "base_url": "https://corp.okta.com", "api_token": "<reference>" }
    }
  ]
}
```

Wichtige LDAP-Optionen (aus dem ausgelieferten Deskriptor): `user_filter` /
`group_filter`, `privileged_group_dns` (Gruppen, deren Mitgliedschaft selbst
ein Signal für privilegierten Zugriff ist), `nhi_dn_suffix` (welcher Teilbaum
nicht-menschliche Identitäten enthält), `start_tls`, `page_size`. Die
`idp`-Art nimmt `provider: okta` (mit `api_token`) oder `provider: entra` (mit
`tenant_id` / `client_id` / `client_secret`); `okta` und `entra` funktionieren
auch direkt als `kind`.

### Wie dies die Attribution hochstuft — präzise

Eine Roster-Quelle registriert Identitäten (per External ID) und, wo das
Verzeichnis sie deklariert, **erlaubte Grants**. Wenn der Ursprung einer
beobachteten Kante mit einer **nicht gemeinsam genutzten** Roster-Identität
übereinstimmt, bindet Modul III den Zugriff an diese Identität, und die
Confidence der Kante wird auf `attributed` hochgestuft. Identitäten, die sich
mehrere Workloads teilen, bleiben ehrlich `approximate` — das Roster kann ein
Credential nicht entteilen; nur das Ausstellen einer Per-Agent-Identität kann
das ([die Brücke zur Governance](/de/how-to/govern-and-approve/)).

Dedizierte **Agent-Identity- und Workload-Identity-Arten** (die
Agent-Federation-Quellen — Entra Agent ID, AgentCore, SPIFFE und
vergleichbare) sind das feste Per-Agent-Signal; Gruppen-/Verzeichnis-Roster
schärfen Personen und Service-Accounts.

## Ehrliche Grenzen

- **SSO wird im Enterprise-Build vollständig.** Der Seam, die
  Flow-Sicherheit und die 501-Posture sind in jedem Build; der
  Protokoll-Provider nicht.
- **Ein Roster kann ein gemeinsam genutztes Credential nicht reparieren.** Es
  kann Ihnen nur ehrlich sagen, dass das Credential gemeinsam genutzt wird.
- **SCIM ist Inbound-Provisionierung** — die Control Plane pusht keine
  Identitäten zurück an Ihren IdP, und der Security-Event-Token-Receiver ist
  eine Inbound-Oberfläche, kein Outbound-Webhook.

## Verwandt

- [Eine Quelle verbinden](/de/how-to/connect-a-source/#die-harte-abhängigkeit-per-agent-identität)
  — warum Identität die harte Abhängigkeit ist.
- [Governance und Freigabe](/de/how-to/govern-and-approve/) — Rollen, RBAC und
  was ein `mapped_role` gewährt.
- [Connectors & Coverage-Tiers](/de/reference/connectors/) — die vollständige
  Liste der Identitätsquellen (Vault, Infisical, Keycloak, SPIFFE, die
  Agent-Identity-Federation-Arten).
