---
title: "Module XII — qualité, évaluations et tests"
description: >-
  Mesure de la qualité : notation des sorties candidates par rapport à des suites
  de référence versionnées avec des scorers enfichables (dont un juge LLM fail-closed),
  et transformation du résultat en preuve canonique inter-modules que les autres modules consomment.
---

Le module XII répond à une seule question — *mon agent fait-il toujours ce qu'il faut ?* — en
**notant** les sorties candidates par rapport à des **suites de référence versionnées** et en émettant le
résultat sous forme de preuve canonique, inter-modules. C'est un module de la couche Intelligence : il
**mesure**, il n'exécute pas le sujet et n'agit pas sur l'infrastructure. Cette
page est la référence de ce que fait aujourd'hui le module d'évaluations, et de ses limites honnêtes.

## Ce qu'il mesure (et ce qu'il n'exécute jamais)

XII est une couche de mesure, pas une couche d'exécution. Une sortie candidate lui parvient
déjà produite — depuis le bac à sable de test (module XVII), depuis la CI, en ligne dans la
requête, ou comme signal échantillonné d'une session réelle — et XII la note par rapport aux cas
d'une suite. **Le seul modèle que XII invoque jamais est le juge** (pour le scorer `llm_judge`) ;
il n'exécute jamais l'agent ou le modèle sujet lui-même. Produire des sorties est le rôle du bac à sable,
pas celui de XII.

L'ensemble des scorers est **enfichable**. Des primitives déterministes et pures couvrent les contrats
courants — `exact`, `contains`, `not_contains`, `regex`, `json_valid`, `json_equal`
et `numeric_range`. À leurs côtés se trouve un scorer **`llm_judge`** qui invoque un modèle
via le port Judge pour noter par rapport à un barème.

## Suites, exécutions et l'artefact canonique

Une **suite** est un jeu de données de référence versionné : elle contient ses cas, un scorer par
défaut, un seuil de réussite et un seuil de régression. Les cas sont **en ajout seul et immuables par
version** — corriger un cas génère un nouveau `suite_version`, jamais une édition en place, de sorte
que le jeu de données ayant produit n'importe quel verdict passé est toujours reconstructible.

Une **exécution** note chaque cas d'une suite, agrège un `score` et un `pass_rate`, et
persiste trois choses : une preuve par cas en ajout seul, un agrégat d'exécution mutable, et un
**`EvalResult`** central — l'artefact canonique (`Suite`, `SubjectKind`, `SubjectID`,
`Score`, `Passed`, `OccurredAt`, `Metrics`) que la conformité (XIII) et l'interface lisent
**sans connaître les propres tables de XII**. Les exécutions s'effectuent de façon synchrone ; le flux SSE d'une
exécution *rejoue l'exécution persistée* (trames par cas, puis un résumé), il n'actionne rien.
Une régression par rapport à une référence positionne `regressed` et écrit un **`Finding`** central
(`Kind = eval_regression`), émis au mieux sur le bus sous la forme
[`finding.reported`](/fr/reference/events/) pour que les modules de diffusion (santé/notifications) le
routent. Côté lecture, les **tableaux de bord (scorecards)** agrègent le taux de réussite, le score moyen et la tendance par
sujet et s'exportent en CSV/JSON.

## Données minimales, par construction

La sortie candidate n'est **jamais persistée** — quelle qu'en soit la source. Un résultat par cas ne stocke
qu'un hachage à sens unique du détail et un libellé écrêté et nettoyé pour l'interface ; l'expurgation est faite
par le handler avant le stockage, jamais supposée du store. Le **moniteur** note des
*signaux comportementaux* d'une session réelle — son état, le nombre de findings, la sévérité maximale et
les chiffres de jetons/coûts (tirés des signaux centraux `Session`, `Finding` et `CostRecord`)
— et **jamais le texte de sortie brut**, que la plateforme ne persiste pas du tout. Les
fixtures de référence sont la seule exception bornée : autorisées par l'opérateur, en opt-in, contenu
non-production, écrêté par le handler avant écriture pour qu'une suite puisse réellement être exécutée.

## Calibration du juge, atténuation des biais et la barrière de régression CI

Les verdicts du juge ne sont **fiables qu'après avoir été mesurés**. Un jeu de calibration étiqueté
par un humain (constitué avec la session guidée `olivares evals label`) alimente une
**exécution de calibration** qui mesure le juge par rapport à la référence humaine : pourcentage
d'accord avec son intervalle de Wilson à 95 %, le **kappa de Cohen** (l'accord seul n'est pas
défendable en cas de déséquilibre des classes), sensibilité/spécificité avec leurs dénominateurs, et
une corrélation de biais de verbosité. Le rapport est une preuve en ajout seul ; la cible —
accord ≥ 0,85 **et** un kappa défini ≥ 0,6 — peut être relevée par exécution mais jamais
abaissée. Un jeu dont les étiquettes humaines sont toutes en réussite ne peut mesurer l'accord
corrigé du hasard et ne certifie rien.

L'atténuation des biais est intégrée et *mesurée* : le prompt du juge force le raisonnement **avant**
le verdict (l'analyse est écartée à la volée — données minimales) et instruit de ne pas
récompenser la longueur ; le mode pairwise en opt-in de la comparaison A/B juge chaque cas partagé
deux fois avec l'ordre de présentation inversé, déclare un vainqueur **uniquement lorsque les deux ordres
concordent**, et rapporte le taux de `position_consistency` mesuré.

La **barrière de régression** (`POST /gate`, CLI `evals gate`) transforme tout cela en un
verdict CI bloquant : une régression par rapport à la référence, un taux de réussite inférieur au seuil de la suite, ou un
**juge non calibré** font échouer la barrière (sortie 1) ; un identifiant de juge manquant se dégrade
en un avertissement *déclaré*, jamais en une réussite silencieuse. Le coût du juge en CI est contrôlé par un
échantillon de cas déterministe à graine fixe, un cache de verdicts indexé par contenu + épinglage du modèle juge +
version du prompt, et un contrôle préalable de budget FinOps qui refuse de dépenser au-delà d'un plafond. La
seule échappatoire à une barrière échouée est le **contournement gouverné** — niveau admin, raison
écrite, audité — qui change le verdict *effectif* que la CI revérifie, jamais celui qui est
enregistré. Chaque taux rapporté est livré avec son dénominateur et son intervalle à 95 % ; voir
`docs/EVAL-METHODOLOGY.md` dans le dépôt pour la méthodologie complète et les sources.

:::caution[Limites honnêtes]
- **`llm_judge` est fail-closed, jamais une fausse réussite.** L'invocation du modèle est une jointure
  déclarée : sans juge câblé, le scorer `llm_judge` retourne `skipped` (exclu du
  dénominateur), jamais une réussite silencieuse. La racine de composition injecte l'adaptateur de juge
  réel ; jusque-là, les cas jugés sont honnêtement rapportés comme non évalués.
- **La barrière bloque les merges, pas l'infrastructure.** La barrière de régression retourne un
  verdict qu'un pipeline CI mappe sur son code de sortie ; XII ne déploie toujours rien et ne déclenche
  rien. Un juge non calibré ne peut pas passer sa propre barrière — la calibration est mesurée
  par rapport à des étiquettes humaines, jamais supposée.
- **XII n'exécute pas le sujet.** Il note les sorties qui lui sont remises ; il n'exécute jamais
  l'agent ou le modèle testé. Le seul appel de modèle qu'il fait est celui du juge.
- **La surveillance porte sur des signaux, pas sur du texte.** La surveillance de session réelle note des
  signaux de résultat en données minimales — jamais la sortie brute, qui n'est jamais persistée. L'absence d'un
  signal surveillé n'est pas une preuve du comportement.
- **Aucune surface d'actionnement.** XII gouverne et observe la qualité ; il ne déploie rien, ne déclenche
  rien et ne met de barrière sur aucune infrastructure. Le *verdict* pré/post-déploiement qu'il fournit est
  une preuve sur laquelle le module de déploiement agit — voir [Honnêteté & limites](/fr/start/honesty-and-limits/).
:::

## Liens connexes

- [Catalogue des modules](/fr/reference/modules/overview/) — où se situe XII et la séparation Gouverner/Actionner.
- [Référence du bus d'événements](/fr/reference/events/) — l'événement `finding.reported` qu'une régression émet.
- [Vue d'ensemble de l'architecture](/fr/explanation/architecture/overview/) — la couche Intelligence.
- [Gouverner et approuver](/fr/how-to/govern-and-approve/) — agir sur un finding de régression.
- [Honnêteté & limites](/fr/start/honesty-and-limits/) — les jointures deny-closed à travers le produit.
