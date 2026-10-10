---
title: Olivares AI face aux passerelles LLM & à l'observabilité (LiteLLM, Langfuse)
description: >-
  Une comparaison honnête avec la stack LLM-ops auto-hébergée populaire — une
  passerelle (LiteLLM) plus une plateforme d'observabilité (Langfuse). Ce que chacune
  fait bien, en quoi Olivares AI est différent, et pourquoi c'est « et », pas « ou ».
sidebar:
  order: 3
---

Une stack auto-hébergée courante et sensée associe une **passerelle LLM** (par exemple
**LiteLLM**) à une **plateforme d'observabilité LLM** (par exemple **Langfuse**). Si
vous en avez une, vous pourriez raisonnablement vous demander si vous avez besoin d'un
control plane tout court. Cette page répond honnêtement à cette question — y compris les
cas où la réponse est **non**.

:::tip[La version courte]
LiteLLM et Langfuse concernent **les appels de modèle que votre application effectue** :
les router, les tracer, gérer les prompts, suivre le coût par appel. Olivares AI
concerne **chaque agent de votre parc et tout ce qu'il lit ou écrit** — bases de
données, stockages objet, serveurs MCP, outils, fichiers — et la conformité de cela
avec ce que la politique permet. Altitude différente. Ils se composent ; nous
**ingérons le même signal gen-ai OpenTelemetry** qu'ils émettent.
:::

## Ce que cette stack fait bien (utilisez-la pour cela)

- **LiteLLM** — une passerelle unifiée, compatible OpenAI, devant de nombreux
  fournisseurs : routage, replis, retries, clés virtuelles, budgets et limites de débit
  par clé, et comptabilité des coûts sur les appels de modèle qui la traversent.
- **Langfuse** — ingénierie et observabilité LLM : **traces** requête/réponse, gestion
  et versionnage des prompts, évaluations, datasets, et une UI orientée développeur pour
  déboguer les chaînes.

Si votre problème est *« instrumenter les appels LLM de mon application, déboguer les
prompts, et gérer l'accès aux modèles depuis un seul point d'accès »*, cette stack est
excellente et auto-hébergeable. Vous n'avez pas besoin d'un control plane pour cela, et
nous ne prétendrons pas le contraire.

## En quoi Olivares AI est structurellement différent

| Dimension | Passerelle LLM + observabilité | Olivares AI |
|---|---|---|
| **Unité de préoccupation** | Un appel de modèle (prompt → complétion) | Un agent et chaque ressource qu'il lit/écrit — BDD, stockages objet, MCP, outils, fichiers |
| **Point de vue** | **Dans le chemin de la requête** (proxy/SDK) ; voit ce que l'application envoie | **Observation hors du chemin des données** de la télémétrie, de l’audit natif et d’un backstop noyau ; **application inline** pour les actions gouvernées |
| **Source de vérité** | Ce que l'application/le proxy **rapporte** | Télémétrie auto-déclarée **corroborée par rapport au propre journal du système** — pgAudit (lecture vs écriture), CloudTrail (accès objet), backstop eBPF |
| **La question clé** | « Qu'a fait ce prompt, et combien a-t-il coûté ? » | « Cet agent utilise-t-il un accès **que personne n'a accordé** ? » — [écart Permis-vs-Observé](/fr/explanation/#laccess-map--read-first-minimal-data-permitted-vs-observed) |
| **Application** | La passerelle peut filtrer les **appels de modèle** (clés, budgets) | Gates en refus par défaut sur les **actions et l'accès aux ressources** : approbations, le [PEP via hooks Claude Code](/fr/how-to/connectors/claude-code-hooks-pep/), filtrage des outils MCP, coupe-circuits |
| **Artefact d'audit** | Traces / logs pour le débogage | Journal en ajout seul, à chaînage de hachage, **signé Ed25519**, **vérifiable hors machine**, exportable en packages de preuves **OSCAL** |
| **Posture de déploiement** | Auto-hébergeable | Auto-hébergé **ou air-gapped** ; le plan de données ne quitte jamais votre périmètre ; **AGPL**, à source disponible |

La différence porteuse est la **vérité terrain**. Une trace d'observabilité vous dit ce
que l'application *a dit* avoir fait. Elle ne peut pas vous dire qu'un agent a atteint
une table que la trace n'a jamais mentionnée. Olivares AI recoupe le signal coopératif
avec le plan de données, de sorte que « ce que l'agent a touché » est un fait corroboré,
non une auto-déclaration. Voir [Vocabulaire des analystes](/fr/explanation/positioning/analyst-vocabulary/)
pour comprendre pourquoi c'est la première de nos trois voies.

## C'est « et », pas « ou » — nous ingérons votre télémétrie

La carte d’accès consomme les spans OpenTelemetry GenAI conformes aux conventions
sémantiques de votre gateway ou outil de traçage. Les appels aux modèles n’utilisent
pas le proxy d’inférence inline d’Olivares AI par défaut ; il applique les règles
aux appels qui le traversent, aux côtés des gates MCP tools/call et de délégation
A2A sur leurs chemins respectifs.

Les sessions Claude Code gérées installent des hooks d’appel d’outil qui contactent
le PEP du moteur, exposé par défaut. Ces hooks sont inline et refusent en cas d’échec :
si le PEP est inaccessible pendant une panne ou un redémarrage du moteur, tout
appel d’outil gouverné est refusé. La disponibilité du moteur compte donc même
lorsque les appels aux modèles vont directement chez un fournisseur.

Pour utiliser l’observation aux côtés de votre stack existante :

- Conservez **LiteLLM** comme passerelle de modèles et **Langfuse** pour le traçage
  orienté développeur et le travail sur les prompts.
- Pointez le flux **OTel gen-ai** vers Olivares AI comme une source corroborante, et
  laissez la carte d'accès, la détection d'écart et le journal assurer la couche de
  gouvernance à l'échelle du parc par-dessus.

→ [Ingérer OpenTelemetry GenAI](/fr/how-to/connectors/otel-genai/) ·
[OTel d'entreprise pour Claude Code](/fr/how-to/claude-code-enterprise-otel/)

## Quand vous ne devriez *pas* recourir à Olivares AI

L'honnêteté tranche dans les deux sens. Vous n'avez probablement **pas** besoin de ce
control plane si :

- Votre seul objectif est de **tracer et déboguer les appels LLM** dans une ou deux
  applications, avec un terrain de jeu de prompts — Langfuse seul convient mieux.
- Vous avez juste besoin d'une **passerelle multi-fournisseurs** avec budgets et
  bascule — c'est le travail de LiteLLM, et nous nous intégrons à ce modèle plutôt que
  de le réimplémenter.
- Vous n'avez **aucun parc à gouverner** : un seul service, un seul modèle, aucun agent
  touchant des bases de données/stockages objet/MCP, et aucune obligation d'audit ou
  réglementaire.

Olivares AI gagne sa place lorsque les questions deviennent *à l'échelle du parc et
adversariales* : **quels agents existent, ce que chacun peut réellement atteindre, où
l'accès s'écarte de la politique, puis-je le prouver à un auditeur, et puis-je arrêter
une mauvaise action en mode refus par défaut** — le tout sans envoyer cette image au
cloud de quelqu'un d'autre.
