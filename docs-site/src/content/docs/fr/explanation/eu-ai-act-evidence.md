---
title: Preuves pour le règlement IA de l'UE à partir des données d'exécution
description: >-
  Comment un control plane auto-hébergé transforme le comportement en direct de
  votre estate IA en preuves techniques dont un dossier au titre du règlement IA
  de l'UE a besoin — au format Annexe IV, générées depuis les données
  d'exécution et stockées par le control plane que vous exploitez vous-même. Pour les acheteurs
  réglementés de l'UE qui ne peuvent pas placer un control plane SaaS américain
  dans leur chaîne de conformité.
---

La plupart des outils de gouvernance IA produisent des preuves comme un diaporama
produit des faits : quelqu'un les écrit, et vous faites confiance au fait que
c'était vrai. Au titre du **règlement (UE) 2024/1689 (le règlement IA de l'UE)**,
cela ne suffit pas. Le fournisseur d'un système à haut risque doit établir la
**documentation technique de l'Annexe IV** *avant* de mettre le système sur le
marché et la **tenir à jour** tout au long du cycle de vie (art. 11), et le plan de
surveillance après commercialisation (art. 72) doit être alimenté par ce que le
système fait réellement en production.

Cette page explique comment Olivares AI vous permet de **générer** ces preuves à
partir du **comportement d'exécution de votre estate** au lieu de les curer à la
main — et pourquoi un **control plane AGPL auto-hébergé** est la forme qui survit à
l'examen d'un acheteur réglementé de l'UE là où un control plane SaaS américain
échoue.

:::note[Qui est le « fournisseur » ici]
Olivares AI est un **outil de gouvernance au-dessus de systèmes IA, et n'est pas
lui-même un système à haut risque de l'Annexe III** dans l'usage typique. Savoir si
*votre* système IA est à haut risque, et qui en est le fournisseur ou le déployeur,
est une détermination juridique qui vous revient — pas à nous. Ce que nous faisons,
c'est rendre les **obligations de documentation technique et de surveillance peu
coûteuses à satisfaire avec de vraies preuves**. Voir [Honnêteté et limites](/fr/start/honesty-and-limits/)
pour ce que la plateforme affirme et n'affirme pas.
:::

## Pourquoi « à partir des données d'exécution » est tout l'enjeu

Les devoirs de documentation du règlement IA de l'UE ne sont pas ponctuels.
L'Annexe IV demande l'architecture du système, ses **ressources de calcul**, ses
caractéristiques de surveillance et de contrôle, ses métriques de performance, son
système de gestion des risques et un relevé des **changements du cycle de vie** — et
l'art. 72 exige un plan de surveillance après commercialisation que vous exécutez
réellement. Un document Word statique se périme dès qu'un modèle est remplacé ou
qu'un agent acquiert un nouvel outil.

Olivares AI observe déjà l'estate pour construire sa
[read/write access map](/fr/explanation/#laccess-map--read-first-minimal-data-permitted-vs-observed)
et son [audit ledger](/fr/reference/glossary/#audit-ledger) append-only, hash-chained,
signé Ed25519. Le module compliance transforme ces mêmes observations en **paquets
de preuves consommables par un auditeur** : scellés, ancrés au ledger, exportables
en JSON, CSV ou **OSCAL**, avec une preuve d'intégrité en direct. Le document est
*dérivé de ce qui s'est passé*, pas asserté sur ce qui était prévu.

Deux règles d'honnêteté sont câblées dans le produit et se transmettent directement
aux preuves :

- Un contrôle dont le seul fondement est architectural rapporte **`by_design`**,
  jamais `satisfied`. « Satisfied » exige des preuves de tenant réelles et liées.
- Le catalogue de frameworks est **épinglé en version à sa source primaire** avec
  une date `verified_on`, et chaque framework porte un avertissement « ceci est un
  mapping technique, pas une certification ».

## Le crosswalk Annexe IV, en bref

Le module compliance fait correspondre les articles du règlement IA de l'UE qu'il
peut documenter — **art. 5, 6, 9, 10, 11, 12, 13, 14, 15, 50 et 72** — aux
capacités que le control plane produit déjà. Ci-dessous figure la vue par section de
l'Annexe IV ; le modèle complet ligne par ligne (avec les endpoints exacts et les
lacunes explicites) est livré dans le package trust & procurement sous le nom
`eu-ai-act-annex-iv.md`.

| Thème Annexe IV | Ce que fournit le control plane | Tiré de |
|---|---|---|
| **1.** Description générale (finalité, fournisseur, versions, livraison) | Inventaire des modèles + versions ; **model card** (JSON/Markdown ; les champs inconnus sont explicitement `not_recorded`, jamais inventés) | `GET /v1/m/models/owned-models/{id}/model-card` |
| **2.** Processus de développement, architecture, **ressources de calcul**, provenance des données, supervision, V&V | Architecture de référence ; **comptabilité compute/coût** par inférence (le côté *opérationnel* du 2(c) — les chiffres au moment de l'entraînement ne sont **pas** documentés, et le catalogue le dit) ; registre de jeux de données + **AIBOM** scellé (CycloneDX 1.6) et **SPDX 3.0.1 AI Profile** ; config approvals/HITL ; résultats d'éval + red-team | échantillons de coûts FinOps ; `GET /v1/m/models/owned-models/{id}/aibom?format=spdx` ; module evals |
| **3.** Surveillance, fonctionnement & contrôle | Preuves d'opération en direct : findings guardrail/anomalie, access map + **Permitted-vs-Observed drift**, chronologies de sessions, état du kill-switch | findings ; `GET /v1/m/accessmap/drift` |
| **4.** Métriques de performance | Méthodologie d'éval + résultats (calibrage du LLM-judge, gates de régression bloquantes) | module evals |
| **5.** Système de gestion des risques (art. 9) | Classification de risque par agent (palier UE × fonction NIST), revue gouvernée en dual-control, export du registre des risques | `GET /v1/m/compliance/risk` ; `GET /v1/m/compliance/dora` |
| **6.** Changements du cycle de vie | Ledger des changements/déploiements ; historique d'admission des modèles ; cycle de vie des versions | enregistrements de déploiement ; `GET /v1/m/models/model-admissions` |
| **7.** Normes appliquées | Le **catalogue de 26 frameworks**, épinglé en version, avec `verified_on` | `GET /v1/m/compliance/frameworks` |
| **8.** Déclaration UE de conformité (art. 47) | **Non générée** — un acte juridique du fournisseur ; la plateforme se contente de la stocker/lier | fourni par le fournisseur |
| **9.** Plan de surveillance après commercialisation (art. 72) | Preuves continues que le plan peut citer : findings, SLOs, communications d'incident, ledger + export SIEM | docs production-readiness + status/incident |

### Les lacunes honnêtes, énoncées d'emblée

Les inscrire dans le dossier le *renforce* — un évaluateur fait confiance à un
document qui nomme ses propres frontières.

- Le **compute au moment de l'entraînement, la qualité/biais statistique des jeux
  de données et la justification de conception** ne sont pas documentés par le
  control plane. Ce sont des éléments rédigés par le fournisseur.
- Les **devoirs de transparence de l'art. 50** (notices d'interaction, marquage du
  contenu IA) sont une lacune honnête de la plateforme elle-même, consignée comme
  telle dans le catalogue.
- Le control plane documente la moitié **opérationnelle** de l'Annexe IV — ce que
  fait votre estate, attribuable et à altération détectable. Il n'écrit **pas** le récit de
  conception du fournisseur ni ne signe la déclaration de conformité.

### Ne codez pas les dates en dur — servez-les

Les calendriers des applications à haut risque sont **en flux** (l'accord
provisoire Digital Omnibus du 2026-05-07 en a déplacé plusieurs). Copier des dates
dans un fichier statique est la façon dont les documents de conformité se périment et
deviennent faux. Le control plane sert le calendrier réglementaire **sous forme de
données** — chaque entrée avec sa source et son `verified_on` :

```http
GET /v1/m/compliance/calendar
```

Votre pipeline GRC lit le calendrier en direct ; votre package de preuves le
référence. Personne ne re-saisit une date.

## Flux de mise en package

1. Pour chaque système IA dans le périmètre, récupérez : la model card
   (`?format=md`), l'AIBOM (`?format=spdx`), la classification de risque, les
   résumés d'éval, le snapshot de drift et l'extrait de calendrier.
2. **Scellez** le bundle en tant que package de preuves de conformité — append-only,
   ancré au ledger :
   `POST /v1/m/compliance/frameworks/eu_ai_act/evidence` →
   `GET /v1/m/compliance/evidence/{id}/export?format=oscal`.
3. Attachez les sections rédigées par le fournisseur (choix de conception, le récit
   de l'art. 9, la déclaration de l'art. 47). La plateforme ne fabrique pas ce que
   seul le fournisseur connaît.

Le résultat est un dossier Annexe IV dont les sections opérationnelles sont
**reproductibles depuis le ledger** et **re-vérifiables hors machine** — une
propriété qu'un document curé à la main ne peut offrir.

## Pourquoi la souveraineté est le facteur décisif pour les acheteurs réglementés de l'UE

Pour une banque, un hôpital, un ministère ou une université sous supervision de
l'UE, *où vivent les preuves* n'est pas un détail — c'est souvent le verrou.

- **Le data plane ne quitte jamais votre périmètre.** Les collecteurs s'exécutent
  sur **votre** infrastructure ; l'access map ne stocke que la *relation* (agent →
  ressource, lecture ou écriture) avec une source et un niveau de confiance —
  **pas de payloads, pas de secrets, pas de PII**. Les preuves de conformité sont
  construites à partir de données qui n'ont jamais eu à transiter par le cloud d'un
  fournisseur.
- **Le control plane peut être entièrement auto-hébergé, ou air-gapped** avec zéro
  egress et une licence hors ligne. Il n'y a aucun fournisseur dans votre chaîne de
  conformité à ajouter comme sous-traitant, à évaluer au titre d'un mécanisme de
  transfert, ou dont dépendre pour la conservation de *vos* preuves réglementaires.
- **AGPL-3.0, source-available.** Votre équipe sécurité peut lire chaque ligne qui
  produit les preuves. La preuve d'intégrité est vérifiable **hors machine** avec
  `audit verify`, de sorte que vous ne faites pas confiance à notre affirmation que
  le ledger est intact — vous le vérifiez. La dépendance à un fournisseur unique est
  atténuée structurellement, pas promise (voir la note de viabilité fournisseur du
  package trust).
- **La résidence est attestée, pas supposée.** `GET /v1/m/compliance/residency`
  produit une attestation de résidence ; les déploiements multi-région sont
  region-scoped et deny-closed par conception.

Un **control plane SaaS américain** inverse tout cela : les preuves comportementales
de votre estate IA — le relevé même qu'un régulateur de l'UE peut demander — sont
générées, traitées et conservées dans le cloud d'un tiers, sous un modèle de
responsabilité partagée que vous ne contrôlez pas, fréquemment hors de l'UE. C'est
précisément l'arrangement que de nombreux acheteurs réglementés de l'UE se voient
dire qu'ils ne peuvent pas conclure. **Le self-hosting n'est pas ici une préférence
de déploiement ; c'est la posture de conformité.**

:::caution[Nous concevons pour l'audit ; nous ne certifions pas]
Rien de ce qui précède ne vous rend, ou ne nous rend, « conforme au règlement IA de
l'UE » — la conformité est une conclusion juridique sur un système spécifique, tirée
par son fournisseur avec conseil juridique. Ce que le control plane vous donne, ce
sont des **preuves sur lesquelles vous pouvez vous appuyer**, générées à partir de
données d'exécution réelles, conservées là où votre régulateur l'attend. Le
[catalogue de frameworks](/fr/reference/modules/xiii-compliance/) porte l'avertissement
« pas une certification » sur chaque entrée, par conception.
:::

## Liens connexes

- [Preuves lisibles par machine](/fr/reference/modules/xiii-compliance/) — la surface
  d'API des preuves, la validation continue de type KSI.
- [Modèle de sécurité](/fr/explanation/security/security-model/) — pourquoi le ledger
  permet de détecter les altérations et comment fonctionne la vérification hors machine.
- [Contexte de marché & sources](/fr/explanation/positioning/market-context-and-sources/)
  — les statistiques vérifiées derrière l'argument de la dette de gouvernance.
