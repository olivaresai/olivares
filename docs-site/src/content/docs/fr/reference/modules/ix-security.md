---
title: "Module IX — sécurité, garde-fous et audit"
description: >-
  Le control plane défensif : des garde-fous déterministes qui produisent des
  findings à données minimales, des anomalies priorisées et des chronologies
  d'incidents vérifiées par chaîne de hachage — détectif par défaut, l'application
  en ligne étant une couture optionnelle, gouvernée, désactivée par défaut.
---

Le module IX est la **couche défensive et transversale** d'Olivares AI. Il transforme
les événements de l'estate et le ledger de preuves à altération détectable en **findings**,
**anomalies priorisées** et **chronologies d'incidents reconstructibles**, afin qu'un
défenseur puisse *voir* et *prouver* ce que chaque agent a fait. Il est **détectif par
défaut** : il observe et remet des preuves, et ne se trouve jamais dans le data-path de
l'agent.

## Ce que c'est

Le module couvre trois responsabilités délimitées :

- **Garde-fous** — une chaîne de détecteurs déterministes et explicables inspecte le texte
  des agents sur les surfaces `input`, `output` et `tool_args` à la recherche de
  secrets/PII, d'injection de prompt, de jailbreak, de contenu interdit, de violations de
  schéma de sortie et du OWASP Agentic Top 10. Les détections portent des références de
  cadre (OWASP LLM Top 10 2025, OWASP Agentic Top 10 2026, MITRE ATLAS) reprises
  textuellement de sources primaires, jamais inventées. Un classifieur optionnel et
  enfichable (un garde-fou-LLM hébergé) s'exécute *derrière* les détecteurs déterministes :
  il ne peut qu'**ajouter** des détections, jamais en supprimer une, et son échec est
  journalisé puis ignoré.
- **Détection d'anomalies** — il corrèle le drift Permis-vs-Observé que calcule le
  [module III](/fr/reference/modules/iii-access-map/) avec les findings de gravité élevée,
  et joint les signaux anti-évasion côté noyau et côté coopératif : un agent qui réduit au
  silence sa propre télémétrie est traité comme un signal, pas comme un angle mort.
- **Forensique / RI** — il regroupe les preuves en un **cas** et reconstruit sa
  **chronologie** à partir du ledger en ajout seul et chaîné par hachage, en *vérifiant* la
  chaîne et ses points de contrôle signés plutôt qu'en leur faisant confiance. Un ledger
  altéré est signalé, pas masqué.
- **Enregistrement des sessions privilégiées** — un enregistrement immuable et rejouable de
  ce qu'une session d'opérateur privilégié a réellement fait sur les surfaces de modules
  les plus sensibles du produit : une trame en ajout seul par action enregistrée (qui,
  quand, forme de route, permission, cibles, résultat, empreinte de requête), chaînée par
  hachage par session et ancrée dans le ledger de preuves (ouverture → ancrages périodiques
  → scellement), de sorte que réécrire une trame brise à la fois la chaîne de session et ses
  ancrages signés au ledger. Le gate s'exécute *avant* l'action et échoue en mode fermé :
  sur une surface enregistrée, l'absence de piste de preuves en ajout signifie pas d'action
  privilégiée.

## Son contrat et ses entités

Le module IX est le **premier producteur de l'entité noyau `Finding`** ; il ne possède ni
ledger ni capture, il les consomme. Au-dessus de `Finding` il possède trois entités : un
**cas** mutable (cycle de vie `open` → `investigating` → `contained` → `closed`, avec un
instantané d'intégrité pris à l'ouverture), un **lien de cas en ajout seul** qui forme la
chaîne de custody (l'ensemble de preuves d'un incident est lui-même une preuve et ne peut
être réécrit), et une **politique d'application** par classe — où l'absence de ligne
signifie *détectif*.

Ses routes sont montées sous l'API du module et enveloppées d'authn + tenant + authz, avec
des permissions read/write/admin à espace de noms. Lire les findings est simple (un finding
est l'alerte elle-même) ; les lectures **sensibles à la reconnaissance** — la chronologie
vérifiée, l'export SIEM, la vue d'anomalies et la vérification d'intégrité autonome — sont
**privilégiées et auto-auditées** : l'acte de regarder est enregistré dans la même chaîne
qu'il inspecte. Chaque mutation (triage, cycle de vie du cas, posture d'application) est
elle aussi auto-auditée. Les exports vers WORM/SIEM (CEF, syslog, OTLP) portent des champs
d'intégrité par ligne afin que la chaîne puisse être re-vérifiée **hors ligne** par un
magasin immuable externe.

## Ce qu'il consomme et produit sur le bus

Le module IX réagit à [`finding.reported`](/fr/reference/events/) (en persistant les
findings de gravité élevée d'autres modules dans la vue sécurité du tenant) et à
[`guardrail.observed`](/fr/reference/events/), le canal d'entrée détective de texte observé
déjà caviardé. Il produit un `FindingReport` par détection sur des clés de routage à espace
de noms `security_*`, que la livraison en aval achemine vers SIEM/Slack/PagerDuty et que la
conformité mappe vers des contrôles. Le flux `guardrail.observed` en direct provient de la
couche d'ingestion runtime décrite dans la [référence du bus d'événements](/fr/reference/events/) :
il **échoue en mode fermé et est à activer explicitement** (désactivé sauf si un opérateur
l'active), et le texte inspecté est la *référence de ressource déjà caviardée* du
connecteur pour une arête `tool_args` — jamais l'argument brut.

:::caution[Limites honnêtes]
- **Détectif par défaut ; l'application est une couture à activer explicitement.** Le module
  observe et remet des preuves. L'application en ligne (bloquer une sortie ou une action)
  est **désactivée par défaut**, de niveau admin, et — lorsqu'un gate d'approbation HITL est
  câblé — gouvernée. L'activer est la seule capacité qui touche la production ; la désactiver
  (le défaut sûr) est toujours autorisée. Un garde-fou qui échoue ne doit jamais casser la
  production.
- **Le flux en direct a une vraie frontière de couverture.** Sur la surface
  `guardrail.observed` en direct, seul **un PII ou un secret intégré dans une référence de
  ressource** (et les motifs de ressource anormaux/sensibles) est détectable. L'injection de
  prompt et le jailbreak nécessitent le *contenu* de l'argument, qui est rejeté à la source
  coopérative et n'atteint jamais le bus ; les surfaces `input` / `output` / `tool_result`
  exigent une source de contenu en cours de processus que ce build ne fournit pas. C'est
  déclaré, pas simulé.
- **La vérification d'intégrité peut être indisponible, jamais simulée.** La chaîne de
  hachage est toujours vérifiée pour sa cohérence interne, mais l'attestation des *points de
  contrôle signés* nécessite la clé publique du ledger câblée ; sans elle, la vérification
  des points de contrôle est signalée **indisponible** plutôt que feinte. Un point de
  contrôle falsifié est détecté, pas approuvé.
- **La couverture hérite des niveaux de l'access map.** Les anomalies bâties sur le drift
  sont bornées par la couverture d'audit à niveaux du module III ; le catalogue de contenu
  (contenu interdit) est un ensemble de départ conservateur et non exhaustif, présenté comme
  tel.
:::

## Liens connexes

- [Référence du bus d'événements](/fr/reference/events/) — `finding.reported`, `guardrail.observed` et le canal d'ingestion runtime.
- [Live-ingest — le producteur observe en cours de processus](/fr/reference/modules/live-ingest/) — le module fermé par défaut qui publie le flux `guardrail.observed` en direct que ce module consomme.
- [Module III — la read/write access map](/fr/reference/modules/iii-access-map/) — le drift que ce module corrèle.
- [Catalogue des modules](/fr/reference/modules/overview/) — la couche du module IX et son statut d'actionnement.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur les findings et l'application.
- [Transférer l'audit vers Splunk](/fr/how-to/forward-audit-to-splunk/) — exporter des preuves vérifiables vers un magasin SIEM/WORM.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce qui est construit, observé et actionné aujourd'hui.
