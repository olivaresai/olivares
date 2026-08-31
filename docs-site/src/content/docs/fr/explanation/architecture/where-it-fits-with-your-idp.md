---
title: Où Olivares AI s'insère par rapport à votre IdP
description: >-
  Olivares AI n'est pas un fournisseur d'identité. Il fédère l'identité des
  agents depuis les registres que vous exploitez déjà — Entra Agent ID, AWS
  AgentCore Identity, Google Agent Identity — en lecture seule, et l'utilise pour
  attribuer l'access map. Comment il se compose avec votre IdP, SSO/SCIM,
  SPIFFE/WIF, et les standards ID-JAG / XAA.
sidebar:
  order: 3
---

Une première question fréquente d'un architecte sécurité est : *« Est-ce encore
un système d'identité que je dois exploiter ? »* Non. **Olivares AI n'est pas un
fournisseur d'identité et ne possède pas d'identités.** Il **consomme** les
identités que vous émettez déjà — pour les humains, depuis votre IdP via SSO/SCIM ;
pour les agents, depuis les registres d'identité d'agents que les hyperscalers ont
rendus généralement disponibles — et les utilise pour attribuer *qui ou quoi* se
trouve derrière chaque edge de l'[access map](/fr/explanation/). Cette note explique
exactement où se situe la jointure.

## Le découpage en couches

```
   Your IdP (Entra ID / Okta / Google)         ← humans: SSO + SCIM (unchanged)
   Agent-identity registries                    ← agents: Entra Agent ID,
     (Entra Agent ID / AgentCore / Google)        AgentCore Identity, Google Agent Identity
            │  read-only roster sync
            ▼
   Olivares AI  ── SPIFFE/WIF roster ──► R/RW access map (attributed edges)
            │                            └─ Permitted-vs-Observed drift
            └─ deny-closed gates (approvals, hooks PEP, MCP gating) — never an IdP
```

- **Les humains** s'authentifient via **votre** IdP. Olivares AI s'intègre avec
  les standards **SSO et SCIM** pour les comptes opérateur et le mapping
  groupe-vers-rôle ; il ne stocke pas de credentials et ne devient pas un second
  annuaire.
  → [Identité SSO & SCIM](/fr/how-to/connectors/sso-scim-identity/)
- **Les agents** obtiennent leur identité depuis les registres que vous avez déjà
  adoptés. Olivares AI fédère ces rosters en **lecture seule** vers un roster
  **SPIFFE/WIF** interne, de sorte que chaque accès observé puisse être lié à une
  identité gouvernée et nommée plutôt qu'à un processus anonyme.

## Ce que fait réellement la fédération d'identité des agents

Le control plane livre des connecteurs de roster en lecture seule pour les
registres d'identité d'agents GA, chacun vérifié face à sa source primaire et
**deny-closed** (pas de credential → roster vide, jamais une erreur fantôme) :

- **Microsoft Entra Agent ID** — importe les identités d'agents, les blueprints et
  les relations owner/sponsor via Microsoft Graph ; fait remonter les orphelins
  assertés par le registre. Les blueprints portant des credentials par mot de
  passe à longue durée de vie déclenchent un finding de **long-lived-credential
  drift**.
- **AWS AgentCore Identity** — importe le roster d'agents ; les agents dotés d'une
  identité de service correspondent à un kind d'identité service-account.
- **Google Agent Identity** — importe les identités reasoning-engine ; la référence
  est un **SPIFFE ID** complet, de sorte qu'elle converge avec le roster SPIFFE par
  external id.

Ces mappings alimentent l'axe d'[attribution de l'access map](/fr/reference/glossary/#attribution-confiance)
(`firm` / `approximate` / `unknown`) — ils ne le réimplémentent pas. La fédération
est strictement en lecture seule : Olivares AI ne mute **jamais** un registre
distant. Les signaux d'appartenance et d'orphelins sont transférés au cycle de vie
des identités non humaines, de sorte qu'un orphelin asserté par le registre apparaît
à travers la machinerie de gouvernance existante.

:::note[Expérimental et design-toward, étiqueté comme tel]
Les descripteurs cross-écosystème (**OASF**) et les **AGNTCY Agent Badges** sont
traités comme **expérimentaux** jusqu'à ce qu'ils satisfassent la conformité aux
verifiable credentials. Les rosters encore en preview (par ex. la Gemini Enterprise
Agent Platform de Google) sont câblés comme des **jointures**, non revendiqués comme
opérationnels. Nous marquons ce qui est GA, ce qui est preview et ce qui est
design-toward — nous ne les confondons pas.
:::

## ID-JAG, XAA et l'authentification client basée sur SPIFFE

Les standards enterprise pour l'accès agent *délégué et attribuable* convergent, et
le control plane est construit pour les suivre plutôt que d'inventer le sien :

- **ID-JAG** (Identity Assertion JWT Authorization Grant) et **XAA** (Cross-App
  Access) sont le pattern émergent pour qu'un IdP émette une autorisation
  **scopée et attribuable** pour un agent agissant à travers plusieurs applications
  — l'extension d'autorisation gérée par l'entreprise dans les travaux
  d'autorisation MCP. À mesure qu'ils arrivent, le token attribuable devient un
  autre signal haute fidélité que l'access map peut lier à une identité gouvernée.
- **Authentification client OAuth basée sur SPIFFE**
  (`draft-ietf-oauth-spiffe-client-auth`) permet aux flux OAuth propres au plane de
  s'authentifier avec un **SVID** dès qu'un serveur d'autorisation publie le support
  — par-dessus le mTLS deny-by-default existant. C'est **design-toward**, sans
  revendication de conformité, jusqu'à ce que le draft et le support serveur se
  stabilisent.
- **À durée de vie courte par défaut.** Les credentials statiques à longue durée de
  vie découverts dans l'estate sont signalés comme une classe de drift, en
  cohérence avec les recommandations **Five Eyes** (2026) selon lesquelles les
  credentials d'agents devraient être à durée de vie courte.

## Ce que cela signifie pour vous

- Vous conservez votre IdP, votre SSO, votre SCIM et le registre d'identité
  d'agents que vous avez standardisé. Rien ne migre.
- Olivares AI devient l'endroit où **toutes** ces identités rencontrent le
  **comportement observé** de votre estate — la seule couche qui peut dire « cet
  agent, de ce registre, possédé par cet humain, utilise un accès que la policy n'a
  jamais accordé. »
- Parce que la fédération est en lecture seule et auto-hébergée, cette corrélation
  n'impose aucun transfert : il n'y a pas de télémétrie obligatoire et, par défaut,
  aucune sortie du plan de contrôle. Ne franchit votre périmètre que ce que **vous**
  configurez à cette fin : les appels à vos API de modèles, les sorties SIEM/webhook
  que vous raccordez et, si vous en provisionnez un, un fournisseur externe d'embeddings.

## Liens connexes

- [Agent / Identité / NHI](/fr/reference/glossary/#identity--nhi) — les définitions du
  glossaire.
- [vs tours de contrôle IA](/fr/explanation/positioning/vs-control-towers/) —
  l'intégration bidirectionnelle avec les plans d'administration de l'écosystème.
