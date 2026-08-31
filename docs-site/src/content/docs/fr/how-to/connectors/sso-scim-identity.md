---
title: "SSO, SCIM & sources d'identité (attribution ferme)"
description: >-
  Câblez l'identité d'entreprise de bout en bout : login console fédéré
  (OIDC/SAML via le seam de fédération), provisioning SCIM dans le control
  plane, et les sources de roster LDAP / Okta / Entra qui font passer
  l'attribution de l'access map d'approximate à firm.
sidebar:
  order: 8
---

L'identité est la dépendance dure sous l'ensemble de l'access map : l'audit
natif attribue un accès à un **identifiant (credential)**, et seul un roster
d'identité peut lier ce credential à un **agent ou une personne**. Cette page
câble les trois surfaces d'identité : le **login SSO** de la console, le
**provisioning SCIM** dans le control plane, et les **sources de roster**
(LDAP, Okta, Entra ID) qui rendent l'attribution `attributed` au lieu
d'`approximate`.

## 1. SSO console (OIDC / SAML)

Le login fédéré est servi via le **seam de fédération** du moteur. La posture
est honnête par construction :

- Les points de terminaison du flux de login existent dans chaque build, et le
  moteur conserve côté serveur chaque valeur de flux porteuse de secret — le
  state CSRF, le nonce OIDC, le verifier PKCE (seul le *challenge* S256 part
  vers le fournisseur). Authorization Code + **PKCE est toujours actif**.
- Le build par défaut livre le fournisseur `NoFederation` : les deux points de
  terminaison retournent `501 sso_not_configured` — la surface est annoncée
  honnêtement sans IdP câblé. Le fournisseur de fédération qui complète le
  protocole fait partie du build d'entreprise et est **configuré par
  environnement au démarrage** (`OLIVARES_SSO_PROTOCOL`, l'ensemble
  `OLIVARES_OIDC_*` pour OIDC, l'ensemble `OLIVARES_SAML_*` pour SAML).
- L'URI de redirect/ACS que votre IdP doit porter est **exacte**
  (`…/v1/auth/federation/callback` sur l'origine de votre console —
  correspondance exacte RFC 9700, sans astuces de préfixe).

L'onglet **Identity & NHI → SSO & SCIM** de la console documente la
configuration en direct, vérifie l'URI de redirect de votre IdP par rapport à
la valeur exacte attendue, et affiche l'état de la connexion — et là où le
backend d'un panneau est un contrat déclaré pas encore en service, il indique
« backend pending » plutôt que de rendre des données fabriquées :

<img class="light:sl-hidden" src="/console/identity-dark.png" alt="La vue Identity & NHI : configuration SSO avec vérification exacte de l'URI de redirect, le roster NHI et les onglets de posture des clés." />
<img class="dark:sl-hidden" src="/console/identity-light.png" alt="La vue Identity & NHI : configuration SSO avec vérification exacte de l'URI de redirect, le roster NHI et les onglets de posture des clés." />

## 2. Provisioning SCIM (entrant)

Le control plane est un fournisseur de service SCIM 2.0 (RFC 7644) standard à :

```
/v1/scim/v2/Users
/v1/scim/v2/Groups
```

- **Auth :** un **jeton d'API admin/owner** lié au tenant sur l'intégration
  SCIM — le même modèle de jeton opaque que le reste de l'API, pas de type de
  secret SCIM distinct. Le point de terminaison est toujours présent (non
  conditionné par feature).
- **Users** provisionne et déprovisionne les principals ; le déprovisioning par
  votre IdP révoque l'accès dès que les RH le décident.
- **Groups** porte les données de référence identité-vers-groupe. Chaque groupe
  peut mapper vers un rôle du control plane via `mapped_role` — et ce mapping
  est **détenu par l'opérateur** : il est défini côté control plane et audité
  (`scim.group.role.map`) ; un push d'IdP n'escalade jamais silencieusement un
  rôle. Les membres inconnus dans un groupe poussé sont ignorés **et audités**,
  non inventés.

## 3. Sources de roster : LDAP, Okta, Entra ID

Les sources de roster alimentent l'inventaire d'identité du module VI et — c'est
là le point — donnent au module III les liaisons qui font monter l'attribution
en gamme :

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

Options LDAP clés (issues du descripteur livré) : `user_filter` /
`group_filter`, `privileged_group_dns` (groupes dont l'appartenance est
elle-même un signal d'accès privilégié), `nhi_dn_suffix` (quel sous-arbre
contient les identités non humaines), `start_tls`, `page_size`. Le kind `idp`
prend `provider: okta` (avec `api_token`) ou `provider: entra` (avec
`tenant_id` / `client_id` / `client_secret`) ; `okta` et `entra` fonctionnent
aussi directement comme `kind`.

### Comment cela fait monter l'attribution en gamme — précisément

Une source de roster enregistre des identités (par external id) et, là où
l'annuaire les déclare, des **grants autorisés**. Lorsque l'origine d'une arête
observée correspond à une identité de roster **non partagée**, le module III lie
l'accès à cette identité et la confiance de l'arête est élevée à `attributed`.
Les identités que plusieurs charges de travail partagent restent honnêtement
`approximate` — le roster ne peut pas dé-partager un credential ; seule
l'émission d'une identité par agent le peut
([le pont vers la gouvernance](/fr/how-to/govern-and-approve/)).

Des **kinds dédiés d'identité d'agent et d'identité de charge de travail** (les
sources de fédération d'agents — Entra Agent ID, AgentCore, SPIFFE et leurs
pairs) constituent le signal ferme par agent ; les rosters de groupe/annuaire
affinent les personnes et les comptes de service.

## Limites honnêtes

- **Le SSO se complète dans le build d'entreprise.** Le seam, la sécurité du
  flux et la posture 501 sont dans chaque build ; le fournisseur de protocole
  ne l'est pas.
- **Un roster ne peut pas réparer un credential partagé.** Il peut seulement
  vous dire, honnêtement, que le credential est partagé.
- **SCIM est du provisioning entrant** — le control plane ne pousse pas
  d'identités vers votre IdP, et le récepteur de Security-Event-Token est une
  surface entrante, pas un webhook sortant.

## Voir aussi

- [Connecter une source](/fr/how-to/connect-a-source/#la-dépendance-dure--lidentité-par-agent)
  — pourquoi l'identité est la dépendance dure.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — rôles, RBAC et ce
  qu'un `mapped_role` accorde.
- [Connecteurs & paliers de couverture](/fr/reference/connectors/) — la liste
  complète des sources d'identité (Vault, Infisical, Keycloak, SPIFFE, les
  kinds de fédération d'identité d'agent).
