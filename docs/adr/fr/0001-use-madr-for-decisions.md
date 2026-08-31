> Traduction automatique. La version anglaise fait foi.

# ADR-0001: Consigner les décisions d'architecture en utilisant MADR

- **Status:** accepted
- **Date:** 2026-06-07
- **Décideurs:** Olivares AI
- **Références:** session documentation/produit établissant le registre des ADR

## Contexte et énoncé du problème

Les décisions d'architecture du control plane étaient consignées dans plusieurs documents de planification et
de contrat (architecture, stack, licences, les contrats par session et les
« décisions de démarrage »). Cet historique est réel et bien séparé, mais il n'est pas sous une forme qu'un
nouveau contributeur ou évaluateur peut lire décision par décision : *ce qui* a été décidé, *pourquoi*,
et *ce qui a été rejeté*. Le contexte se perd entre les sessions quand la justification ne vit que
dans une longue prose de planification.

## Facteurs de décision

- Un registre durable, indexé par décision, qui survit entre les contributeurs.
- Un format léger qui ne devient pas un projet de documentation à part entière.
- Publiable dans le cadre de la documentation du produit.

## Options envisagées

- **MADR (Markdown Any Decision Records).** Minimal, largement adopté, natif en Markdown.
- **Un journal de décisions sur mesure.** Plus de liberté, mais aucune convention partagée.
- **Aucun ADR formel.** Garder la justification uniquement dans les documents de planification.

## Résultat de la décision

Option choisie : **MADR**. Chaque décision déjà prise est capturée comme un
`docs/adr/NNNN-*.md` numéroté avec le contexte, l'option choisie, les conséquences et les alternatives
rejetées, et est publiée dans la section *Explanation* du site de documentation.

### Conséquences

- **Bon :** les décisions sont découvrables et auto-explicatives ; les nouveaux contributeurs ne
  rejugent pas des questions tranchées.
- **Mauvais / compromis :** une petite discipline continue pour ajouter un enregistrement quand une décision est
  prise.
- **Neutre :** les documents de planification existants restent la source que les ADR citent, et non une chose que les
  ADR remplacent.

## Pourquoi les alternatives ont été rejetées

- **Journal sur mesure** — réinvente une convention déjà résolue ; plus difficile pour les contributeurs extérieurs.
- **Aucun ADR** — laisse la justification enfouie dans la prose, ce qui est la manière dont le contexte se perdait.
