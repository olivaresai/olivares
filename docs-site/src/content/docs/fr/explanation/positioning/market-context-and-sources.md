---
title: Contexte de marché et sources
description: >-
  Les signaux de marché derrière Olivares AI — prolifération d'agents, pilotes en
  échec, contrôles d'accès manquants — chacun avec sa source primaire vérifiée, son
  chiffre exact et une réserve honnête. Le lieu unique d'où toutes les autres pages
  tirent leurs chiffres.
sidebar:
  order: 1
---

Cette page est la **source unique de vérité pour chaque statistique de marché**
utilisée sur l'ensemble du site web, du README et de la documentation d'Olivares AI.
Elle existe parce que le marché de la gouvernance de l'IA est inondé de chiffres dont
l'attribution a été déformée au fil des reprises — et l'analyste d'un acheteur
vérifiera. Nous préférons perdre une formule percutante plutôt que de citer un chiffre
que nous ne pouvons pas assumer.

:::note[La règle d'attribution]
Nous citons **uniquement des sources primaires**, les nommons exactement et citons le
chiffre tel que la source l'énonce. Nous ne **blanchissons pas** un chiffre via un
blog qui a perdu l'attribution, et nous ne **cumulons pas** des statistiques
d'agrégateurs (« 70 % du Fortune 100… ») qu'aucun acheteur ne peut tracer. Lorsqu'un
constat est **préliminaire ou non évalué par les pairs**, nous le précisons sur la
même ligne. Cela reflète la manière dont le produit lui-même traite les preuves : la
[confiance d'attribution](/fr/reference/glossary/#attribution-confiance) est un champ de
première classe, et un contrôle ne disposant que de preuves au stade de la conception
rapporte `by_design`, jamais `satisfied`.
:::

## Les chiffres que nous utilisons, et d'où ils viennent

| Affirmation | Chiffre (tel que la source l'énonce) | Source primaire | Réserve / comment nous l'utilisons |
|---|---|---|---|
| Les organisations victimes d'une compromission IA manquaient de contrôles d'accès | **97 %** des organisations ayant subi un incident de sécurité lié à l'IA manquaient de contrôles d'accès IA adéquats ; **13 %** des organisations ont signalé une compromission de leurs modèles ou applications d'IA | **IBM, *Cost of a Data Breach Report 2025*** (recherche menée par le **Ponemon Institute**), IBM Newsroom | L'attribution est **IBM / Ponemon — pas Forrester**, une attribution erronée qui circule largement. Nous l'utilisons pour le *déficit de contrôle d'accès*, qui est exactement ce que traitent la [carte d'accès R/RW](/fr/explanation/#laccess-map--read-first-minimal-data-permitted-vs-observed) et le diff Permis-vs-Observé. |
| Des projets agentiques seront abandonnés | **Plus de 40 %** des projets d'IA agentique seront **annulés d'ici la fin 2027**, en raison de coûts croissants, d'une valeur métier floue ou de contrôles de risque inadéquats | **Gartner**, communiqué de presse (2025) | Nous l'utilisons pour le point de la *dette de gouvernance* — les projets meurent faute de contrôles et de valeur démontrable, pas par défaut de qualité des modèles. |
| Les guardian agents deviennent un marché | Les technologies de **guardian agent** représenteront **10–15 % du marché de l'IA agentique d'ici 2030** | **Gartner**, communiqué de presse (2025) | Établit les « guardian agents » comme une catégorie reconnue par les analystes. Nous sommes explicites : nous ne sommes *pas* un agent runtime qui garde d'autres agents — voir [Vocabulaire des analystes](/fr/explanation/positioning/analyst-vocabulary/). |
| La plupart des pilotes n'ont aucun impact sur le résultat | **~95 %** des pilotes d'IA générative ne produisent **aucun impact mesurable sur le résultat (P&L)** ; les outils **achetés/partenaires** externes réussissent à peu près **deux fois plus souvent** que ceux développés en interne | **MIT Media Lab, Project NANDA — *The GenAI Divide: State of AI in Business 2025*** (rapporté via *Fortune*, août 2025) | **Préliminaire, non évalué par les pairs.** Nous le signalons toujours comme tel. Nous utilisons le constat *acheter-vs-construire* pour étayer l'argument « adopter un control plane maintenu plutôt que bricoler sa gouvernance » — jamais comme une statistique établie. |
| L'enseignement supérieur utilise l'IA plus vite qu'il ne la gouverne | Une large majorité (**~80 %**) du personnel de l'enseignement supérieur utilise des outils d'IA, tandis que **moins d'un quart (<25 %)** connaissent les politiques d'IA de leur établissement | Enquêtes **EDUCAUSE** AI Landscape / communautaires (2025–2026) | Estimations d'enquête ; vérifier l'étude/l'année exactes avant toute citation externe. Nous utilisons le *déficit de connaissance des politiques* sur la [page enseignement supérieur](/fr/explanation/positioning/higher-education-and-research/). |

## Preuves qualitatives sur lesquelles nous nous appuyons

Ce ne sont pas des pourcentages ; ce sont des positions issues de sources nommées et
citables qui cadrent *pourquoi la catégorie existe*.

- **Bessemer Venture Partners** (*Atlas — « Securing AI Agents: the defining
  cybersecurity challenge of 2026 »*) : l'intervention en vol, chirurgicale, sur le
  comportement des agents est **« là où le marché est le plus sous-développé et où se
  trouve l'opportunité d'infrastructure la plus claire »**, et **« la plupart des
  entreprises n'ont pas d'inventaire précis des agents opérant dans leur
  environnement »**. C'est l'énoncé externe du déficit que comble notre
  [carte d'accès](/fr/explanation/).
- **Anthropic** (publications d'ingénierie sur le sandboxing de Claude Code et les
  Managed Agents) : les sandboxes auto-hébergées déplacent l'exécution vers une
  infrastructure que le client contrôle, mais Anthropic **attribue au client la
  journalisation d'audit, la politique/RBAC, l'orchestration multi-hôtes et
  l'inspection du trafic**. Cette responsabilité déléguée est la jointure que comble
  Olivares AI — voir [vs control towers](/fr/explanation/positioning/vs-control-towers/).

## Signaux d'enquête (à titre indicatif — vérifier avant toute citation externe)

Les enquêtes indépendantes et communautaires rapportent systématiquement la même
forme : les agents prolifèrent plus vite que les organisations ne peuvent les
inventorier ou les attribuer. Nous traitons les pourcentages spécifiques ci-dessous
comme un **contexte indicatif** synthétisé à partir d'enquêtes nommées ; ils ne font
**pas** partie de notre ensemble de sources primaires vérifiées ci-dessus et devraient
être revérifiés par rapport à l'instrument original avant tout usage externe.

- Les enquêtes de la Cloud Security Alliance / Token Security (n≈418), de Protiviti et
  d'Optro rapportent, diversement : une large part des organisations ont des
  **agents inconnus/non gérés** dans leur environnement, seule une minorité tient un
  **inventaire en temps réel**, une majorité a connu un **incident lié à un agent** au
  cours de l'année précédente, et seule une minorité peut **remonter une action
  d'agent jusqu'à un humain ou un système**.

Le point que ces enquêtes établissent globalement est la seule chose que nous
affirmons publiquement : **les organisations perdent la trace de leurs agents et ne
peuvent attribuer ce que ces agents font.** C'est une affirmation que notre produit
est conçu pour rendre fausse pour ses utilisateurs — et c'est le cœur honnête de
chaque page de positionnement ici.

## Ce que nous ne revendiquons délibérément **pas**

- Pas de décomptes de clients, de murs de logos, ni de « approuvé par N entreprises »
  — le produit est pré-1.0 et pré-lancement (voir
  [Honnêteté et limites](/fr/start/honesty-and-limits/)).
- Aucune certification ou attestation que nous ne détenons pas (SOC 2, ISO
  27001/42001 sont en **état de préparation (readiness)**, pas des certificats — voir
  le dossier de confiance et d'achat fourni avec les sources).
- Pas de benchmarks inventés, de revendications de débit ni de chiffres de précision.
  Les chiffres de capacité ne proviennent que du harnais de benchmark reproductible,
  avec la provenance matérielle.
