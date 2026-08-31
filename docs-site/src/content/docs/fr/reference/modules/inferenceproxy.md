---
title: "Proxy d'inférence en ligne (PEP pour /v1/messages)"
description: >-
  Un point d'application des politiques (PEP) optionnel, à activer explicitement,
  qui se place devant le contrat Claude /v1/messages pour les appelants en SDK brut
  et en curl, en appliquant la résidence, l'accès aux modèles, la fenêtre de
  contexte, le DLP et le budget en ligne avant le transfert — fermant le
  contournement par ANTHROPIC_BASE_URL — avec un enregistrement à trace
  à altération détectable effectué avant le transfert par défaut, dont les exceptions sont
  énoncées sur cette page plutôt que découvertes. La configuration et la rédaction
  du DLP sont opérationnelles ; l'écouteur reste non monté tant qu'un opérateur ne
  le provisionne pas.
---

Le proxy d'inférence en ligne est le point
d'application pour le trafic d'inférence qui n'est **pas** Claude Code — les
appelants en SDK brut et en `curl` qui atteignent directement le contrat Claude
`/v1/messages`. La décision est prise à la racine de composition
(`cmd/olivares/inferenceproxy.go`) ;
`modules/inferenceproxy` possède la configuration de gouvernance par tenant et
la politique DLP de sortie d'inférence, qui servent d'entrées à cette décision,
et ne décide lui-même rien d'une requête en direct.
Les réglages gérés côté serveur ne peuvent pas atteindre ce
trafic : un `ANTHROPIC_BASE_URL` personnalisé les contourne entièrement. Le proxy
se place devant `api.anthropic.com` et exécute un pipeline gouverné **en ligne** —
résidence, [accès aux modèles](/fr/reference/modules/x-models/), DLP et gates de
contenu, puis dimensionnement de la fenêtre de contexte et budget — avant le
transfert du moindre octet sur `/v1/messages`.

L'enregistrement se fait **avant le transfert par défaut** : l'intention
autorisée est écrite au ledger à altération détectable **avant** le transfert, et sans preuve
il n'y a pas de transfert (fermé par défaut). Un tenant peut le désactiver
délibérément (`record_mandatory: false`) ; la preuve est alors ancrée **après**
le transfert, au mieux (best-effort) et sans discrétion — un ancrage en échec
est signalé, jamais masqué.

Ce défaut était l'inverse, et la différence n'est pas théorique : un tenant qui
n'a jamais ouvert la page de configuration est précisément celui auquel personne
n'a réfléchi, donc l'ancrer au mieux rendait la garantie de preuve facultative
pour tous ceux qui n'avaient rien choisi. Deux limites méritent d'être lues
plutôt que découvertes. D'abord, la posture ne gouverne **que** le moment
précédant le transfert : après, l'appel a eu lieu et aucune posture ne l'annule,
ce chemin est donc un trou bruyant par construction. Ensuite, un opérateur qui a
réglé le spool d'audit sur `degrade` a déjà dit ce qui doit arriver à
l'épuisement : pour un tenant qui n'a jamais choisi de posture de preuve, ce
`degrade` déclaré l'emporte et l'appel est transféré avec un trou enregistré. Un
tenant ayant explicitement posé `record_mandatory: true` est refusé — son propre
choix prime sur celui du spool.

Le précontrôle de dimensionnement `count_tokens` constitue lui-même une sortie
vers le fournisseur ; il ne s'exécute donc qu'**après** le passage de tous les
gates de contenu locaux : un prompt refusé par le DLP ou le pare-feu n'est jamais
transmis, même pour en compter les tokens. Le proxy est l'un des **quatre PEP qui
échouent en mode fermé** livrés par la plateforme.

## Maturité, dit sans détour

**PARTIEL.** La répartition est honnête et délibérée :

- **OPÉRATIONNEL** — la configuration de gouvernance par tenant et la politique
  DLP de sortie d'inférence : rédaction, persistance et audit. Deux magasins sous
  `/v1/m/inferenceproxy/` : un `config` singleton (bascules par gate, la posture
  de défaillance en cas de proxy indisponible, le mode DLP de réponse, le mandat
  d'enregistrement) et un ensemble `dlp/rules` (une règle par classe de
  sensibilité → `allow`|`deny`).
- **À ACTIVER EXPLICITEMENT, non monté par défaut** — l'écouteur `/v1/messages`
  proprement dit. Il se lie par défaut en **loopback** (`127.0.0.1:8448`), mais un
  opérateur peut configurer explicitement une autre adresse d'écoute ; il adopte
  par défaut une posture **fermée en cas d'échec** (un proxy incapable de décider
  ne doit pas transférer), et n'est monté que si un opérateur le provisionne.

Ce module **ne décide rien** d'une requête en direct. C'est la politique durable,
rédigeable depuis la console, que la racine de composition lit via `Policy()` ;
la décision est composée à partir de coutures existantes (`EvaluateModelAccess`,
`CheckBudget`, résidence, `ClassifySensitivity`, le contrôle de fenêtre de
contexte) à la périphérie.

## Le pipeline en ligne

Chaque gate est **activé** par défaut, et chacun reste inerte sous sa propre
activation explicite native tant qu'il n'est pas configuré — le DLP jusqu'à la
première règle, l'accès aux modèles jusqu'au premier octroi, la résidence
uniquement quand une région est épinglée, le budget jusqu'à ce qu'un budget
contraignant existe. Un tenant relâche un gate spécifique de manière explicite, et
l'audit enregistre qui a ouvert le périmètre. Rédiger la politique DLP de sortie
est de **niveau admin** : autoriser quel contenu peut sortir est un changement de
gouvernance privilégié.

## Contexte délimité

- **Données minimales par construction.** Aucune ligne qu'il persiste — config,
  règle DLP, audit — ne porte jamais un prompt, une réponse, un secret ou une
  valeur PII détectée. Les octets que le proxy inspecte en vol sont empreintés
  (SHA-256) et ancrés au ledger par la racine de composition, jamais stockés ici.
- C'est la **troisième jambe** du proxy : la coque protocolaire (analyser,
  transférer, dériver les corps) est le connecteur Apache aveugle à l'identité ;
  le décideur gouverné est le moteur. Ce module ne possède que la politique que les
  deux consultent — gardant la décision hors de la frontière du connecteur
  open-core.

## Liens connexes

- [Module X — gestion des modèles et fournisseurs](/fr/reference/modules/x-models/) — la
  politique d'accès aux modèles et de fenêtre de contexte par surface que ce proxy
  applique.
- [Exécuter Claude Code avec Olivares](/fr/how-to/run-claude-code-with-olivares/) — le
  chemin Claude Code gouverné, que le hook en cours de processus couvre ; ce proxy
  est le PEP de repli pour les appelants que ce chemin ne peut atteindre.
- [Honnêteté et limites](/fr/start/honesty-and-limits/) — ce qui est opérationnel, à
  activer explicitement ou au stade de conception sur l'ensemble de la plateforme.
