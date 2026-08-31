---
title: "Live-ingest — le producteur observe en cours de processus"
description: >-
  L'un des 30 modules : le producteur « live-tap » qui publie les événements
  détectifs qu'un connecteur hors processus ne peut émettre. Fermé par défaut et à
  données minimales : il ne déplace aucun contenu brut, et chaque moitié observe
  qu'il possède est honnêtement vide plutôt que simulée. Partiel — il est à activer
  explicitement et conditionné par variable d'environnement.
---

Live-ingest (`modules/liveingest`) est l'un des 30 modules câblés — un **producteur en cours de
processus** plutôt qu'un emplacement de capacité. Il ne fait pas partie de la carte
numérotée historique I–XXIII. Il existe pour une seule raison
architecturale : un `SourceConnector` hors processus ne peut diffuser que la somme
d'observation scellée (arête / coût / finding) via son contrat gRPC,
qui n'a ni RPC d'événement ni champ texte — il **ne peut donc pas publier d'événement
détectif**. Seul un module en cours de processus détient la capacité de publication sur le
bus, aussi live-ingest est la moitié « live-tap » qui émet ces événements pour les modules
qui les consomment déjà.

## Ce que c'est

Le connecteur de télémétrie Claude du control plane s'exécute hors processus en tant que
plugin embarqué ; son flux `Gather` ne porte que le `oneof` `Observation` figé. Ce contrat
de fil est délibérément figé (vérifié contre les changements cassants ; voir la
[politique de stabilité de l'API](/fr/reference/api-stability/)) et ne porte ni extrait ni
surface texte. Live-ingest est le producteur en cours de processus qui fournit les deux
événements que le connecteur ne peut structurellement pas : `guardrail.observed` pour le
[module IX](/fr/reference/modules/ix-security/)
et `voice.telemetry.observed` pour le module XVI. Il ne possède ni entités ni surface
REST ; c'est un publieur sur le [bus d'événements](/fr/reference/events/).

## Ce qu'il produit — `guardrail.observed`

C'est le producteur manquant pour la chaîne de détecteurs de sécurité qui consomme déjà
[`guardrail.observed`](/fr/reference/events/). Il est **fermé par défaut et à activer
explicitement** :

- **Par défaut (inspection désactivée).** Le module ne s'abonne à rien, ne publie rien, et
  journalise sa moitié vide de façon visible — jamais un no-op silencieux.
- **Avec l'activation explicite de l'opérateur.** Il s'abonne à `edge.observed` et, pour une
  arête dont la ressource est une référence d'outil résolue, dérive un extrait `tool_args`
  **borné et déjà caviardé** et le publie comme un `ObservedText` ne portant que des champs
  de référence non sensibles. L'extrait est l'*identifiant* de ressource que le connecteur a
  déjà caviardé à la source (un chemin assaini, un hôte+chemin sans requête ni
  identifiants, un nom de programme Bash dont les arguments sont supprimés, une référence
  d'outil MCP). Live-ingest le borne et la chaîne de sécurité le réduit de nouveau — triple
  défense. Le **contenu de l'argument est rejeté au connecteur et n'atteint jamais le bus.**

La chaîne de détecteurs émet alors automatiquement un finding par détection, sur du trafic
réel.

## Ce qu'il produit — `voice.telemetry.observed`

Un producteur en cours de processus câblé pour les seules métadonnées de tour vocal/temps
réel autorisées par liste — jamais l'audio et jamais le texte de transcription. La charge
utile est une valeur typée qui, par construction, ne peut porter ni audio, ni
transcription, ni PII, et le consommateur rejette tout échantillon avec une clé hors
liste d'autorisation ou une référence de session/agent manquante. Sans backend vocal temps
réel dans ce build, **rien ne l'appelle** : la moitié observe est honnêtement dormante et ne
fabrique aucune télémétrie tant qu'un backend ne l'alimente pas.

:::caution[Limites honnêtes]
- **Fermé par défaut.** `guardrail.observed` ne publie rien sauf si l'opérateur active
  explicitement l'option ; la moitié vide est journalisée, pas masquée.
- **La couverture de détection est étroite, et c'est dit comme tel.** Comme seules les
  *références* d'arguments déjà caviardées sont disponibles en cours de processus, les
  détections réalistes sur cette surface sont un PII ou un secret intégré dans une
  référence, et les motifs de ressource anormaux/sensibles. **L'injection de prompt et le
  jailbreak sont hors de portée** — ils nécessitent le *contenu* de l'argument, que le
  connecteur rejette. Les surfaces `input` / `output` / `tool_result` exigent une source de
  contenu en cours de processus que ce build ne possède pas sous le transport hors processus
  et le fil figé.
- **La télémétrie vocale est dormante.** Aucun backend temps réel n'existe dans ce build,
  aussi cette moitié ne produit rien plutôt que d'inventer des échantillons.
- **Il ne déplace jamais de contenu brut et n'élargit jamais la capture du connecteur.** Les
  données minimales sont une propriété du fil lui-même, pas un réglage superposé par-dessus.
:::

## Liens connexes

- [Référence du bus d'événements](/fr/reference/events/) — la charge utile `guardrail.observed` / `ObservedText`
  (un extrait caviardé sur un repli JSON, pas la somme scellée) et `edge.observed`.
- [Module IX — sécurité, garde-fous et audit](/fr/reference/modules/ix-security/) — la
  chaîne de détecteurs qui consomme le flux `guardrail.observed` que ce module publie.
- [Module XVI — agents vocaux et temps réel](/fr/reference/modules/xvi-voice/) — le consommateur
  de la moitié `voice.telemetry.observed` (dormante).
- [Module II — opération en direct et sessions](/fr/reference/modules/ii-sessions/) — dérive ses
  propres `goal` / `agent_ref` / `summary` directement à partir des signaux qu'il consomme déjà,
  plutôt que via un événement live-ingest.
- [Catalogue des modules](/fr/reference/modules/overview/) — les 30 modules et la
  répartition honnête Gouverner/Observer-vs-Actionner que soutient ce producteur en cours de
  processus.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — où se situent les modules en
  cours de processus et les connecteurs hors processus.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — pourquoi les moitiés vides sont déclarées, pas simulées.
