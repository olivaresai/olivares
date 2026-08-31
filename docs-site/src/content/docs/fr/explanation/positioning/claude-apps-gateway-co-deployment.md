---
title: Co-déploiement de Claude apps gateway et d'Olivares AI
description: >-
  Comment exécuter la Claude apps gateway auto-hébergée d'Anthropic et laisser
  Olivares AI la gouverner comme une surface d'entreprise supplémentaire :
  inventaire, posture, ingestion des audits, corrélation OTLP et endpoint du
  protocole gateway de phase 1.
sidebar:
  order: 9
---

## Définition de Claude apps gateway

La
[Claude apps gateway](https://code.claude.com/docs/en/claude-apps-gateway)
d'Anthropic est un service auto-hébergé fourni dans le binaire `claude` à partir
de la v2.1.195 ; exécutez-le avec `claude gateway --config gateway.yaml` et
adossez-le à PostgreSQL. Elle place une connexion OIDC devant Amazon Bedrock,
Claude Platform on AWS, Google Cloud Agent Platform, Microsoft Foundry ou l'API
Anthropic, afin que les développeurs utilisent les sessions de l'IdP d'entreprise
plutôt que des identifiants de fournisseur locaux. Son fichier `gateway.yaml`
associe les groupes de l'IdP aux listes de modèles autorisés et aux paramètres
gérés, tandis que son API Admin de limites de dépenses permet de plafonner les
dépenses par utilisateur, groupe ou organisation. Elle diffuse la télémétrie via
OTLP et émet des événements d'audit JSON sur une seule ligne. Dans son
[annonce](https://claude.com/blog/introducing-the-claude-apps-gateway) du
29 juin 2026, Anthropic la présente comme une infrastructure gateway fournie
directement par Anthropic pour Claude Code.

## Exécutez-la. Olivares la gouverne.

Si vous utilisez déjà la gateway d'Anthropic, ou prévoyez de le faire,
conservez-la. La doctrine est **« et », pas « ou »** : la gateway d'Anthropic
gère la session gateway de Claude Code, l'accès aux modèles et le routage vers
les fournisseurs en amont ; Olivares AI transforme ce déploiement en surface
gouvernée au sein d'un plan de contrôle plus large.

Le connecteur `claude-apps-gateway` inventorie `gateway.yaml` : émetteur, listes
de modèles autorisés par groupe d'IdP, posture d'administration des dépenses,
destinations OTLP et fournisseurs en amont. Il crée des findings de posture pour
les états de configuration importants aux yeux d'un opérateur de gouvernance et
ingère les événements d'audit JSON de la gateway, de sorte que les refus,
l'émission de sessions et les enregistrements d'inférence rejoignent l'audit
ledger à altération détectable (tamper-evident). Dirigez la diffusion OTLP de la
gateway vers le receiver OTLP d'Olivares : le signal `session.id` pourra alors
être corrélé aux
enregistrements gouvernés du runtime de session. Olivares ne conserve toutefois
que des données structurelles, jamais le payload des prompts.

## Limites documentées

Les choix de périmètre documentés par Anthropic, cités d'après sa documentation
au 2026-07-03, figurent ci-dessous. Ce sont des déclarations de périmètre, pas des
défauts ; elles définissent où doit se situer la frontière d'un co-déploiement.

| Fonctionnalité | Statut | Notes |
|---|---|---|
| SAML, LDAP et autres méthodes d'authentification non OIDC | Non pris en charge. | OIDC uniquement. Placez un bridge OIDC devant la gateway si nécessaire |
| Multi-tenant (plusieurs émetteurs OIDC) | Non pris en charge. | Un émetteur par gateway. Exécutez des instances distinctes |
| UI d'administration | Non disponible. | La configuration est le fichier YAML ; redéployez pour la modifier |
| Chart Helm | Non disponible. | La gateway s'exécute comme un Deployment stateless standard |
| Pipelines CI | Aucun flux de service token n'existe pour les pipelines sans opérateur |  |
| OTLP/gRPC | Non pris en charge. | OTLP via HTTP uniquement |
| Serveur Windows | Non pris en charge. | Déployez sur Linux |
| Catalogue de modèles | Modèles Claude uniquement | la gateway traduit les identifiants Claude pour chaque fournisseur en amont |

## Ce qu'Olivares ajoute à côté

Olivares ne supprime pas ces limites de la gateway d'Anthropic. Il ajoute à côté
le plan de gouvernance manquant.

| Limite de la gateway d'Anthropic | Capacité complémentaire d'Olivares |
|---|---|
| SAML, LDAP et autres méthodes d'authentification non OIDC | Pour la console et le plan de gouvernance d'Olivares, la page [Identité SSO/SCIM](/fr/how-to/connectors/sso-scim-identity/) documente la fédération OIDC/SAML et [l'architecture IdP](/fr/explanation/architecture/where-it-fits-with-your-idp/) associe humains et agents aux rosters SSO/SCIM et SPIFFE/WIF. Cela n'ajoute pas SAML à la gateway d'Anthropic : conservez son fonctionnement OIDC uniquement ou placez un bridge OIDC devant elle. |
| Multi-tenant (plusieurs émetteurs OIDC) | Le [plan de contrôle multi-tenant](/fr/reference/modules/xx-multi-tenancy/) d'Olivares délimite par tenant les entités, findings, sessions et l'audit ledger, avec RLS PostgreSQL pour les déploiements multi-tenant. Exécutez une instance distincte de la gateway pour chaque émetteur et gouvernez chacune comme une surface propre ; ne considérez pas une gateway d'Anthropic comme multi-émetteurs. |
| UI d'administration | La console web d'Olivares est une couche de présentation de l'API décrite par le [module XIX](/fr/reference/modules/xix-api-manage-as-code/), et la documentation de l'identité présente l'UI live **Identity & NHI -> SSO & SCIM**. Il s'agit d'une console d'administration du plan de contrôle, pas d'un éditeur UI du fichier `gateway.yaml` d'Anthropic. |
| Chart Helm | Olivares fournit son propre [déploiement Kubernetes avec Helm](/fr/tutorials/getting-started/kubernetes/) et un opérateur Kubernetes distinct. Il déploie le plan de contrôle Olivares ; il ne prétend pas empaqueter la gateway d'Anthropic. |
| Pipelines CI | Les automatisations Olivares peuvent utiliser des tokens d'API opaques, révocables et liés au tenant via le [manage-as-code](/fr/how-to/manage-as-code/). Pour les identifiants gouvernés du runtime et du déploiement, le broker WIF/SPIFFE émet des identifiants à courte durée de vie. Ce mécanisme est distinct de la gateway d'Anthropic, dont les propres recommandations CI restent un accès direct au fournisseur, sauf si vous choisissez délibérément l'endpoint proxy Olivares ci-dessous. |
| OTLP/gRPC | Le receiver `claude` d'Olivares accepte les endpoints receiver OTLP habituels employés par [OpenTelemetry GenAI](/fr/how-to/connectors/otel-genai/), via HTTP comme gRPC. La gateway d'Anthropic continue d'envoyer les données en OTLP/HTTP ; les autres agents gouvernés peuvent utiliser directement gRPC, et les événements obtenus peuvent alimenter l'audit ledger cryptographique et les [packs de preuves de conformité](/fr/reference/modules/xiii-compliance/). |
| Serveur Windows | Aucune capacité de serveur Windows n'est revendiquée ici. Exécutez les composants côté serveur sur Linux, dans des conteneurs ou sur Kubernetes, et gouvernez les endpoints des développeurs au moyen de la télémétrie, des hooks et des preuves des connecteurs. |
| Catalogue de modèles | Le [module X](/fr/reference/modules/x-models/) gouverne un parc de modèles et de fournisseurs multiples : Claude, OpenAI, Gemini et l'inférence locale ; le connecteur Bedrock ajoute l'observabilité de l'utilisation, des coûts et des Guardrails de Bedrock. La gateway d'Anthropic reste limitée à Claude tandis qu'Olivares gouverne le parc plus large, y compris la posture de Codex via la [gouvernance de l'authentification par abonnement](/fr/explanation/positioning/governing-subscription-authed-agents/). |

## Sur-ensemble du protocole, phase 1

Anthropic publie le protocole de la gateway et invite les tiers à l'implémenter.
Le proxy d'inférence d'Olivares implémente un sur-ensemble de phase 1 décrit dans
le contrat d'ingénierie du protocole apps-gateway : découverte OAuth,
autorisation des appareils selon la RFC 8628, polling des tokens à travers la
couture d'identifiants des sessions après une approbation authentifiée,
distribution en mode document unique des paramètres gérés avec ETag, forme de
liste en lecture seule des limites de dépenses et `GET /protocol`.

Le descripteur consigne lui-même les divergences : les paramètres gérés sont en
mode document unique, l'en-tête de version est `x-olivares-version`, les routes
d'écriture/effectives/d'audit des limites de dépenses renvoient des réponses
`501` conformes, et Olivares conserve son association plus riche des refus de
budget tout en ajoutant `x-should-retry: false`. La phase 1 ne fournit ni le
callback OIDC/la page navigateur `/device` d'Anthropic, ni les règles de fusion
des paramètres gérés par groupe, ni les chemins d'écriture des limites de
dépenses, ni `count_tokens`, ni l'attribution par l'en-tête
`x-claude-code-session-id`.

## Choisir une topologie

- **Gateway seule.** Elle suffit à une organisation OIDC à émetteur unique,
  limitée à Claude, à l'aise avec la gestion du YAML et les redéploiements, et
  satisfaite des limites de dépenses, de la diffusion OTLP et de la sortie
  d'audit JSON propres à la gateway.
- **Gateway + Olivares.** C'est le co-déploiement recommandé lorsque Claude Code
  entre dans un parc réglementé : conservez la gateway d'Anthropic, ajoutez le
  connecteur `claude-apps-gateway`, dirigez OTLP vers Olivares et conservez dans
  le plan de contrôle la vue obtenue sur la posture, le runtime et les preuves.
- **Proxy Olivares comme endpoint du protocole gateway.** Utilisez cette
  topologie lorsque vous souhaitez délibérément que le proxy d'inférence
  d'Olivares fournisse la surface du protocole gateway de phase 1. Elle convient
  si le sous-ensemble livré suffit ; elle ne remplace pas entièrement le flux
  OIDC par navigateur de la gateway d'Anthropic ni l'administration des dépenses
  par les chemins d'écriture.
