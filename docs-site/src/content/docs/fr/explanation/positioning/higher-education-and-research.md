---
title: Enseignement supérieur et recherche
description: >-
  Pourquoi un control plane auto-hébergé convient aux universités et aux
  institutions de recherche — appliquer une politique d'usage acceptable à travers
  un parc fédéré, isoler les travaux à risque dans des sandboxes, et produire des
  rapports d'attribution, sans envoyer les données des étudiants ou de la recherche
  vers le cloud d'un fournisseur.
sidebar:
  order: 5
---

Les universités et les institutions de recherche ont adopté l'IA plus vite qu'elles
ne l'ont gouvernée. Les enquêtes **EDUCAUSE** rapportent qu'une large majorité
(**~80 %**) du personnel de l'enseignement supérieur utilise désormais des outils
d'IA, tandis que **moins d'un quart (<25 %)** connaissent les politiques d'IA de leur
établissement (EDUCAUSE AI Landscape / enquêtes communautaires, 2025–2026 —
estimations d'enquête ; voir
[Contexte de marché et sources](/fr/explanation/positioning/market-context-and-sources/)).
Cet écart — usage généralisé, faible connaissance des politiques — résume en une ligne
le problème de gouvernance de l'enseignement supérieur.

Le secteur a aussi des contraintes qui rendent **un control plane SaaS américain
difficile à vendre** : des données de recherche soumises à des conditions de
financement ou d'IRB, des dossiers d'étudiants soumis au droit de la vie privée (FERPA
aux États-Unis, RGPD dans l'UE), et une culture d'informatique décentralisée et
fédérée où chaque département gère sa propre pile. Un control plane auto-hébergé et
source-available y est naturellement adapté, précisément en raison de ces contraintes.

## Trois missions que le control plane assure pour l'enseignement supérieur

### 1. Appliquer la politique d'usage acceptable à travers un parc fédéré

Les politiques d'usage acceptable (AUP) pour l'IA sont d'ordinaire un PDF que personne
ne lit. Le control plane transforme les parties qui sont *techniques* en quelque chose
d'observable et d'applicable :

- **Découvrir** les agents, copilotes et serveurs MCP réellement utilisés à travers
  les départements — y compris ceux de l'ombre que la politique n'a jamais anticipés.
- **Cartographier** ce que chacun peut lire ou écrire, et **diffuser Permis vs
  Observé** afin que l'agent d'un groupe de recherche atteignant un système qui ne lui
  a jamais été accordé apparaisse comme une dérive.
- **Appliquer** les lignes techniques en deny-closed là où la plateforme se situe dans
  un chemin de décision — approbations/HITL, le
  [PEP par hooks Claude Code](/fr/how-to/connectors/claude-code-hooks-pep/), le contrôle
  des outils MCP — plutôt que de compter sur le fait que tout le monde a lu l'AUP.

Le périmètre honnête : la plateforme applique ce qui est *exprimable en politique sur
les actions et les accès des agents*. Elle n'arbitre pas les questions d'intégrité
académique et ne lit pas les intentions — elle rend les garde-fous techniques réels et
le reste auditable.

### 2. Isoler les travaux à risque dans des sandboxes

La recherche et les travaux pédagogiques impliquent couramment du code non fiable, des
prompts adverses et des agents expérimentaux. Les modules de **sandbox de
simulation/test d'agents** et de **red-teaming** de la plateforme permettent
d'exercer un comportement à risque en isolation, à l'écart des systèmes de production,
avec consignation des résultats.

:::caution[Ce qu'est la sandbox, et ce qu'elle n'est pas]
La garantie d'isolation d'exécution est le **module sandbox** — les sondes de
red-team ne s'exécutent que là, jamais contre le control plane en service ni contre
les agents de production. La plateforme **détecte** les schémas d'exécution de code et
d'exfiltration et **teste le refus** ; ce n'est pas une sandbox d'OS à usage général
enveloppant le portable de chaque étudiant. Faites correspondre la revendication à la
capacité.
:::

### 3. Produire des rapports d'attribution

Lorsque quelque chose tourne mal — une plainte sur le traitement des données, une
revue de conformité d'un financement, un signalement d'abus — la question est toujours
*qui a fait quoi, avec quel système, quand*. Le control plane y répond à partir du
registre **append-only, chaîné par hachage (hash-chained), signé Ed25519**, avec une
[confiance d'attribution](/fr/reference/glossary/#attribution-confiance) par arête et
une vérification hors machine. Les rapports d'attribution sont dérivés d'une activité
réellement enregistrée, et le rapport lui-même permet de détecter les altérations — ce qui compte
lorsque le constat a des conséquences pour une personne.

## Pourquoi l'auto-hébergement est ici le facteur décisif

- **Aucun cloud fournisseur dans le chemin des données.** Les collecteurs s'exécutent sur
  l'infrastructure propre de l'établissement ; la carte d'accès ne stocke que la
  *relation* (agent → ressource, lecture/écriture) avec une source et une confiance —
  **pas de charges utiles, pas de PII, aucun contenu d'étudiant ni de recherche**.
  Rien n'a besoin de traverser le cloud d'un fournisseur pour être gouverné. Il n'y a
  pas de télémétrie obligatoire et, par défaut, aucune sortie du plan de contrôle. Ne
  franchit le périmètre du campus que ce que l'établissement configure à cette fin :
  les appels à ses API de modèles, les sorties SIEM/webhook qu'il raccorde et, s'il en
  provisionne un, un fournisseur externe d'embeddings.
- **Fédéré par nature.** Un control plane multi-locataire, auto-hébergé et à identité
  fédérée reflète la manière dont les universités gèrent déjà l'informatique —
  autonomie par département, visibilité centrale — au lieu de tout faire passer par un
  unique locataire SaaS.
- **Des options d'air-gap et de souveraineté** conviennent aux enclaves de recherche
  sécurisées et aux données résidentes dans l'UE, avec une attestation de résidence
  (`GET /v1/m/compliance/residency`).
- **AGPL, source-available, aucun coût plancher pour démarrer.** Un ingénieur de
  plateforme ou une équipe de calcul scientifique peut le déployer et lire chaque
  ligne — la voie d'adoption ascendante (bottom-up) que le secteur emploie réellement,
  et non un contrat SaaS verrouillé par les achats.

## Voir aussi

- [Preuves de l'EU AI Act à partir des données d'exécution](/fr/explanation/eu-ai-act-evidence/)
  — pour les institutions de l'UE soumises au règlement.
- [Où s'insère Olivares AI avec votre IdP](/fr/explanation/architecture/where-it-fits-with-your-idp/)
  — fédérer l'identité du campus et l'identité des agents.
- [Auto-héberger le control plane](/fr/how-to/self-hosting/) — pour démarrer.
