> Traduction automatique. La version anglaise fait foi.

# ADR-0014: Publication publique et CI sur GitHub Actions + Docker

- **Status:** accepted
- **Date:** 2026-06-04
- **Décideurs :** Fran Olivares (décision de démarrage)
- **Références :** décisions de démarrage de la feuille de route (Release/CI)

## Contexte et énoncé du problème

Le développement se déroule dans un dépôt privé ; la chaîne d'approvisionnement
publique et vérifiable a besoin d'une surface de CI/publication largement reconnue
et transparente (pour les identités de signature keyless, la provenance SLSA et la
distribution publique des artefacts).

## Facteurs de décision

- Une identité de publication publique et vérifiable (OIDC) et un journal de transparence pour la signature.
- Une distribution de conteneurs standard et largement reconnue.
- Garder le développement quotidien privé jusqu'à ce qu'une publication soit organisée et publiée.

## Options envisagées

- **GitHub Actions + Docker pour tous les artefacts publics ; un dépôt de développement privé.**
- **CI auto-hébergée également pour les publications publiques.**

## Résultat de la décision

Option retenue : **GitHub Actions + Docker pour tout ce qui est public, toujours** ; **le
développement se déroule dans un dépôt privé**. L'identité OIDC GitHub Actions du
workflow de publication est ce qu'attestent les signatures keyless et la provenance
SLSA, et les images/charts sont publiées vers un registre OCI public.

### Conséquences

- **Avantages :** les signatures et la provenance se rattachent à une identité publique
  et vérifiable ; distribution standard ; le développement reste privé jusqu'à une
  publication intentionnelle.
- **Inconvénients / compromis :** le dépôt public est un export organisé du dépôt de
  développement privé, et non un miroir en temps réel.
- **Neutre :** la publication du dépôt public est une action délibérée et soumise à validation.

## Pourquoi les alternatives ont été rejetées

- **CI auto-hébergée pour les publications publiques** — une identité de signature
  auto-hébergée est bien plus difficile à vérifier pour des tiers qu'une identité OIDC
  GitHub Actions publique assortie d'un journal de transparence.
