---
title: "Module XXIII — opérations de modèles"
description: >-
  Le registre gouverné des modèles que vous POSSÉDEZ — hébergés, affinés ou
  importés — avec admission des modèles signés et déploiements d'inférence
  locaux. Il gouverne la chaîne d'approvisionnement de vos propres modèles ;
  il ne les entraîne pas, ne les sert pas et ne les soumet pas à des
  benchmarks.
---

Le module XXIII correspond au volet **modèles détenus** de la pile de modèles.
Alors que le module X (modèles et fournisseurs) gouverne le *catalogue de
référence* et le *routage* des modèles que vous consommez, ce module gouverne
ceux que vous **possédez et exploitez** : un registre versionné, le gate
d'**admission** des modèles signés qui décide quelles versions peuvent être
déployées, et les **déploiements d'inférence** locaux qui les servent. Il suit
et gouverne ; il n'entraîne jamais un modèle, n'exécute jamais une tâche de
fine-tuning et n'exécute jamais lui-même l'inférence.

La surface de console de ce module est **Model Operations** (groupe
Intelligence), avec des onglets pour les modèles détenus, les datasets, les
tâches de fine-tuning, l'admission, les déploiements et le ledger des sceaux
AIBOM. La posture GPAI des fournisseurs (par fournisseur) se trouve dans
**Models & providers**, et la chaîne d'approvisionnement des agents possède sa
propre vue — toutes deux concernent chaque fournisseur ou l'estate, et non
chaque modèle détenu.

## Ce qu'il est

Trois surfaces coopérantes, toutes deny-closed et auditées :

- **Modèles détenus et versions.** Un registre des modèles que vous possédez
  (`hosted`, `fine_tuned`, `imported`), chacun avec des **versions** immuables
  qui nomment un artefact. Une version est enregistrée, puis son artefact signé
  est admis — la ligne de version elle-même ne change jamais.
- **Admission.** Une **politique de confiance** par tenant et l'historique des
  **verdicts** enregistrés. La politique nomme les ancres de confiance —
  racines CA et/ou clés publiques, plus éventuellement les identités et
  émetteurs Sigstore — et la **méthode de signature est dérivée** de votre
  configuration (`sigstore-keyless`, `certificate-pki` ou `bare-key`) ; une
  politique vide n'admet rien. Admettre une version vérifie un **bundle** de
  signature par rapport à la politique et enregistre le verdict. Un verdict
  dont la vérification échoue est enregistré honnêtement, pas dissimulé.
- **Déploiements.** Déploiements d'inférence locaux (vLLM, Ollama, llama.cpp,
  autres). Quand le tenant **impose** les modèles signés, la création ou la
  mise à jour d'un déploiement qui référence une version revérifie
  l'admission : si la version n'a pas de verdict vérifié, ou si la racine de
  confiance qui l'a admise n'est plus dans la politique, le déploiement est
  refusé.

## Lignée et preuve

- **Datasets.** Composants de lignée minimal-data — un nom, une référence de
  contenu facultative et un hash, une classification et un label de
  gouvernance — **jamais le contenu du dataset**. Un dataset couvre tout le
  tenant ; sa référence de modèle facultative est un pointeur de lignée,
  validé deny-closed. `verified` est une **affirmation de l'opérateur** sur la
  provenance, jamais un résultat cryptographique, et la console l'indique
  comme telle.
- **Tâches de fine-tuning.** Enregistrements de travaux de fine-tuning exécutés
  à l'extérieur et de la **version** de modèle produite par chacun. Le plan ne
  démarre, n'annule ni n'exécute jamais l'entraînement et ne stocke ni poids ni
  contenu de dataset — ce sont des enregistrements d'inventaire, pas un
  lanceur d'entraînement.
- **AIBOM et model card.** À partir d'un modèle détenu, vous pouvez
  **générer** une AIBOM CycloneDX à jour (ou une sérialisation SPDX 3.0.1) et
  une model card (JSON ou Markdown), toutes en lecture seule. Un document
  généré n'est pas une preuve avant d'être **scellé** : le scellement ancre un
  engagement canonique de hash de contenu dans l'audit ledger (toujours
  CycloneDX — SPDX ne peut jamais être scellé). Le ledger ne stocke que le
  hash ; le reçu du sceau est donc l'unique occasion de sauvegarder le
  document scellé. L'onglet transversal **AIBOM seals** est le ledger durable
  et append-only de ces engagements.

## Ce qu'il impose

Lorsque `require_signed` est activé, un déploiement qui référence une version
de modèle n'est admis **que si** cette version possède un verdict d'admission
vérifié dont la racine de confiance d'ancrage est toujours configurée. Retirer
une racine de la politique a pour effet rétroactif de refuser les futures
créations/mises à jour de déploiement des versions admises uniquement par
cette racine — elles doivent d'abord être **réadmises** sous les ancres
actuelles. Il s'agit du même pin d'ancre que le moteur enregistre dans chaque
verdict (`signer_roots`) et expose afin qu'un opérateur sache exactement quelle
racine s'est portée garante d'une version.

## Ce qu'il n'est pas

- Il n'exécute **aucun** entraînement ni tâche de fine-tuning — il enregistre
  leur statut pour la lignée.
- Il ne sert **pas** l'inférence — il gouverne les enregistrements de
  déploiement qui la servent.
- Il ne décide pas qu'une version est « actuellement déployable » à partir
  d'un verdict stocké — seule la nouvelle vérification du moteur au moment du
  déploiement fait autorité. La console ne qualifie donc jamais une version de
  fiable ou déployable à partir du seul historique.

## Chaîne d'approvisionnement des agents

La vue de console distincte **Agent Artifacts** enregistre quatre classes
d'artefacts de l'estate du tenant : Agent Skills, extensions `.mcpb`,
templates `ui://` de MCP App et fichiers d'instructions `AGENTS.md`. Le
registre stocke identité, provenance, empreintes de contenu et métadonnées de
posture — jamais le corps des skills, les manifestes ou le texte des
instructions. Un grade de posture est un **résultat de scan enregistré**
provenant d'un scanner de connecteur ou d'un opérateur, pas un scan exécuté
par la console ; l'absence de grade est présentée de façon neutre comme
« non scanné ».

Sa BOM CycloneDX 1.6 de chaîne d'approvisionnement des agents est distincte
d'une AIBOM de lignée par modèle. Les sceaux ajoutent un engagement canonique
de hash de contenu au ledger séparé `models.agent_aibom`, tandis que le reçu
renvoyé reste l'unique copie du document scellé. La couverture est limitée aux
éléments enregistrés : un artefact jamais enregistré n'est pas représenté.
