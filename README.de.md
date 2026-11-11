# Vördr

<img src="custom_components/vordr/brand/logo.png" alt="Vördr: Schild mit der Rune Algiz" height="64">

[English](README.md) · Deutsch

Vördr hält Rechner und Dienste aktuell und zeigt in Home Assistant, wo das nicht gelingt.

Der Name ist das altnordische *vörðr*, in der nordischen Überlieferung der Schutzgeist, der einen Menschen ein Leben
lang begleitet; hier begleitet jeden Rechner sein eigener Agent. Das Zeichen ist ein Schild mit der Rune Algiz (ᛉ), der
Rune des Schutzes. Quellen und Erzeugung: `assets/brand/` (`python assets/brand/render.py`).

Ein kleiner Agent (`vordr-agent`) auf jedem Rechner meldet ausstehende Updates, spielt sie auf Anfrage oder im
Wartungsfenster ein und meldet jedes Ergebnis als festen Zustand. Eine Leitstelle (`vordr-hub`) nimmt die Agenten an,
signiert ihre Aufträge und gibt ihren Zustand an Home Assistant weiter: Die Integration `custom_components/vordr`
([ha/](ha/README.de.md)) verbindet sich als freigegebener Client mit der Leitstelle und legt ein Gerät pro Rechner an,
darunter die Update-Quellen und wichtigen Programme mit ihrem Stand.

**Was gebaut ist:**

- Quellen: apt (Debian, Ubuntu, Raspberry Pi OS, Proxmox VE und Backup Server), Windows Update und winget.
- Prüfen, Installieren und Neustart als signierte Aufträge, mit lokaler Richtlinie, Wartungsfenster, Laufjournal und
  Prüfungen nach dem Lauf; auf Wunsch automatisch in Ringen mit Kanarienvögeln.
- Auf Wunsch eine Backup-Prüfung vor jedem Install oder ein frisches Backup des Gastes direkt davor (erste Umsetzung:
  Proxmox VE mit Proxmox Backup Server).
- Signiertes Selbst-Update der Agenten mit Rücksprung; signiert wird mit `vordr-release` beim Betreiber.
- Pakete von pip, pipx und npm sowie Container-Images: Bestand, bekannte Schwachstellen und Schadsoftware aus den
  OSV-Daten, veraltete Versionen, Aktualisieren per Knopf.
- Appliances ohne eigenen Agenten (Firewall, NAS) über einen Stellvertreter: Firmware melden, Konfiguration sichern.
- In Home Assistant je Host ein Gerät mit Update-Entitäten, Sensoren und Knöpfen; Probleme als Reparaturhinweis und als
  Ereignis für Automationen.

Entwürfe: [design/architecture.de.md](design/architecture.de.md),
[design/ha-integration.de.md](design/ha-integration.de.md), [design/proxy-agent.de.md](design/proxy-agent.de.md).
Entscheidungen (englisch): [docs/adr](docs/adr/README.md). Einrichten: [deploy/README.de.md](deploy/README.de.md).
Mitarbeit (englisch): [AGENTS.md](AGENTS.md). Sicherheitslücken: [SECURITY.de.md](SECURITY.de.md).

## Installieren

- **Home Assistant:** über HACS (dieses Repository als benutzerdefiniertes Repository, Typ Integration) oder von Hand;
  siehe [ha/README.de.md](ha/README.de.md).
- **Leitstelle und Agenten:** `./build.sh` baut die Binaries; Einrichtung, systemd-Units und Abschottung stehen in
  [deploy/README.de.md](deploy/README.de.md), ebenso das Signieren eigener Agent-Releases.

## Bauen und ausprobieren

```bash
./build.sh                                      # ADR-Index, Standort-Prüfung, gofmt, vet (Linux + Windows), Tests
                                                # (mit -race, wenn gcc da ist), Binaries nach dist/
./dist/vordr-agent-linux-amd64 check --no-refresh   # Bericht als JSON; Paketlisten frischt er nur als root auf
```

`build.sh` prüft auch, dass das Repo standort-neutral bleibt (ADR-0099, ADR-0134, `tools/check_neutral.py`): keine
privaten Adressen, keine echten Domains, keine Pfade ausserhalb des Repos, keine Begriffe des eigenen Standorts und
keine Formulierungen eines Betriebsprotokolls. Die Standortbegriffe stehen nicht im Repo, sondern in
`~/.config/vordr/site-terms.txt` (ein Begriff je Zeile, darf leer sein) oder in der Datei, auf die `VORDR_SITE_TERMS`
zeigt. Ohne Liste bricht der Build ab und sagt, wie es weitergeht. Wer keine eigene Liste braucht, baut mit
`VORDR_SITE_TERMS="none: <grund>" ./build.sh`; dann laufen die allgemeinen Regeln allein, und die Prüfung nennt bei
jedem Lauf den Grund.

Leitstelle und Agent verbinden (Vorlagen: `hub.example.yaml`, `agent.example.yaml`):

```bash
vordr-hub key                                   # öffentlicher Hub-Schlüssel -> agent.yaml hub.public_key
vordr-hub run                                   # Tunnel auf TLS, Verwaltung über den lokalen Socket
vordr-agent hub-check                           # auf dem Host: erreicht er die Leitstelle?
vordr-agent run                                 # auf dem Host: meldet sich an, wartet auf Freigabe
vordr-agent identity                            # auf dem Host: Fingerprint zum Abgleich
vordr-hub approve <node> --fingerprint <fp>     # an der Leitstelle: zulassen
vordr-hub list                                  # Zustand, online, letzter Bericht
vordr-hub check <node>                          # signierten check-Auftrag schicken
vordr-hub events <node> --since 24h             # Ereignisse eines Hosts der letzten 24 h
```

| Pfad | Inhalt |
|---|---|
| `cmd/vordr-agent` | Kommandozeile des Agenten |
| `cmd/vordr-hub` | Kommandozeile der Leitstelle |
| `cmd/vordr-release` | Betreiber-Schlüssel anlegen, Agent-Releases signieren (beim Betreiber, nie auf der Leitstelle) |
| `internal/wire` | Tunnel-Nachrichten, Anmelde-Signatur, Sichten für Home Assistant |
| `internal/agent` | Agent-Seite des Tunnels: Anmeldung, Heartbeat, Berichte, Aufträge, Selbst-Update |
| `internal/hub` | Leitstelle: Register mit Freigabe, Tunnel- und HA-Endpunkt, Aufträge, Ringe, Backup-Prüfung, Ereignisprotokoll, Admin-Socket |
| `internal/protocol` | Verben, Umfänge, Ergebniscodes, signierte Aufträge und Manifeste mit Prüfung und Wiederholungsschutz |
| `internal/policy` | lokale Richtlinie `agent.yaml`: strenge Vorgaben, Prüfregeln, Zeitfenster |
| `internal/source` | was jede Update-Quelle meldet |
| `internal/source/apt` | apt-Quelle: Probelauf auswerten, Sicherheitsupdates, Neustartbedarf, Installieren |
| `internal/source/winget` | Programme unter Windows über winget |
| `internal/source/winupdate` | Windows selbst über Windows Update |
| `internal/inventory` | Pakete von pip, pipx und npm je Ort, veraltete Versionen, Aktualisieren |
| `internal/images` | Container-Images: Bestand, Vergleich mit der Registry, Neubauten per Knopf |
| `internal/osv` | Abgleich mit den OSV-Daten an der Leitstelle |
| `internal/versions` | Versionen ordnen wie ihr Ökosystem (PEP 440, SemVer, Debian, Alpine) |
| `internal/target` | Treiber des Stellvertreters für Appliances (Synology DSM, pfSense) |
| `internal/runner` | externe Befehle starten; Exit-Code als Daten |
| `internal/identity` | Ed25519-Identität (Windows: DPAPI) |
| `internal/facts` | OS, Architektur, Virtualisierung |
| `custom_components/vordr` | Home-Assistant-Integration, an der Wurzel, wo HACS sie sucht ([ha/README.de.md](ha/README.de.md)) |
| `ha/` | Tests der Integration und ihre festen Abhängigkeiten |
| `deploy/` | Installationsskripte, systemd-Units, fester Befehl für pfSense ([deploy/README.de.md](deploy/README.de.md)) |
| `design/` | Entwürfe (englisch, deutsch als `*.de.md`) |
| `docs/adr/` | Architekturentscheidungen (englisch) |
| `tools/` | Standort-Prüfung (`check_neutral.py`), Export des veröffentlichten Stands (`export_public.py`), Erzeugung der Windows-Zeitzonentabelle |
| `assets/brand/` | Logo und Icons |

Eckpunkte:

- Go, statische Binaries ohne Laufzeitumgebung (Agent: Linux amd64/arm64/arm, Windows amd64; Leitstelle: Linux amd64).
- Der Agent verbindet sich nur nach aussen; auf den Rechnern ist kein Port offen.
- Geschlossene Verbmenge (`check`, `install`, `reboot`, `agent_update`, `cancel`, `backup`), keine Shell, keine
  Paketnamen von aussen.
- Die lokale Richtlinie auf dem Rechner hat das letzte Wort; die Leitstelle kann nur anfragen.
- Technische Namen (Verben, Zustände, Konfigschlüssel, Entitäts-IDs) sind englisch. Anzeigenamen in Home Assistant
  folgen dessen Systemsprache (deutsch oder englisch).

## Lizenz

MIT, siehe [LICENSE](LICENSE).
