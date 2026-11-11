# Sicherheitsrichtlinie

[English](SECURITY.md) · Deutsch

Vördr spielt Updates ein und startet Hosts auf signierte Aufträge neu. Darum zählen Fehler bei der Signatur, der Freigabe
von Hosts, der lokalen Richtlinie des Agenten und seinen Verben. Bitte melde sie vertraulich.

## Eine Schwachstelle melden

Nutze die vertrauliche Schwachstellenmeldung von GitHub: im Reiter **Security** des Repositorys **Report a
vulnerability** wählen ([ADR-0138](docs/adr/0138-vulnerabilities-are-reported-privately-on-github.md)). Bitte eröffne
kein öffentliches Issue.

Hilfreich sind:

- die Komponente (`vordr-agent`, `vordr-hub`, `vordr-release`, die Home-Assistant-Integration) und ihre Version,
- was ein Angreifer braucht (Netzwerkposition, einen freigegebenen Client, Zugang zur Leitstelle, …) und was er
  gewinnt,
- Schritte oder ein Test, die es zeigen.

Du bekommst eine Antwort, sobald die Maintainerin oder der Maintainer es angesehen hat. Eine Korrektur erscheint
zusammen mit einem Sicherheitshinweis (Security Advisory), der dich nennt, es sei denn, du möchtest das nicht.

## Unterstützte Versionen

Korrekturen gehen in das neueste Release der Integration und in den aktuellen Agent und die aktuelle Leitstelle. Ältere
Versionen bekommen keine Korrekturen; aktualisiere stattdessen.

## Geltungsbereich

Der Entwurf und seine Vertrauensgrenzen stehen in `design/architecture.md` (Abschnitt 5, das Bedrohungsmodell, auf
Deutsch) und in den ADRs in `docs/adr/`. Funde in Software Dritter, über die Vördr nur berichtet (Pakete, Images,
Firmware), gehören zu deren Projekten.
