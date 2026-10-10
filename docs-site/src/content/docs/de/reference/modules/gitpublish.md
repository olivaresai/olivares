---
title: "Gesteuerte Git-Veröffentlichung"
description: "Commits übertragen, Pull Requests öffnen und zusammenführen: mit genehmigten Git-Host-Bindungen, aktueller Berechtigung und aufgezeichneten Ergebnissen."
---

Das Modul gitpublish ermöglicht gesteuerte Veröffentlichungen auf GitHub, GitLab und einfachen Git-Remotes (nur Push). Es gehört zu Community.

Jede Wirkung erfordert ein genehmigtes Ziel sowie Repository- und Zugangsdatenbindungen und eine aktuelle Autorisierung. Fehlen die erforderliche Verwaltung, das Git-Programm oder die Autorisierung, wird die Veröffentlichung abgelehnt. Ein registriertes Modul macht ein Repository noch nicht bereit zur Veröffentlichung.

Der API-Namensraum ist `/v1/m/gitpublish`. Er umfasst Ziele, Push-, Pull-Request- und Merge-Absichten, Abgleich und Beobachtungen.

Ein Push kann einen Sitzungslauf nennen (`session_run`). Olivares holt den Commit dann vor der Veröffentlichung aus dem Ordner dieses Laufs in das Server-Repository, nur über das lokale Dateiprotokoll. Der Lauf muss zum Arbeitsbereich des Ziels gehören; eine Anfrage nennt nie einen Pfad.

Ein ungewisses entferntes Ergebnis erlaubt keinen erneuten Versuch. Anfragen, Beobachtungen und Bestätigungen werden getrennt erfasst. Eine bereits gesendete entfernte Operation kann mit einem späteren lokalen Widerruf zusammentreffen.

[Modul-API-Referenz](/reference/api-beta/).
