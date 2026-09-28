---
title: "Gesteuerte Git-Veröffentlichung"
description: "Commits übertragen, Pull Requests öffnen und zusammenführen: mit genehmigten Git-Host-Bindungen, aktueller Berechtigung und aufgezeichneten Ergebnissen."
---

Das Modul gitpublish ermöglicht gesteuerte Veröffentlichungen auf GitHub und GitLab. Es gehört zu Community.

Jede Wirkung erfordert ein genehmigtes Ziel sowie Repository- und Zugangsdatenbindungen und eine aktuelle Autorisierung. Fehlen die erforderliche Verwaltung, das Git-Programm oder die Autorisierung, wird die Veröffentlichung abgelehnt. Ein registriertes Modul macht ein Repository noch nicht bereit zur Veröffentlichung.

Der API-Namensraum ist `/v1/m/gitpublish`. Er umfasst Ziele, Push-, Pull-Request- und Merge-Absichten, Abgleich und Beobachtungen.

Ein ungewisses entferntes Ergebnis erlaubt keinen erneuten Versuch. Anfragen, Beobachtungen und Bestätigungen werden getrennt erfasst. Eine bereits gesendete entfernte Operation kann mit einem späteren lokalen Widerruf zusammentreffen.

[Modul-API-Referenz](/de/reference/api-beta/).
