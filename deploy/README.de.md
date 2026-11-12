# Einrichten

[English](README.md) · Deutsch

Leitstelle und Agenten laufen als systemd-Dienste. Beide Installationsskripte prüfen alles, bevor sie etwas ändern,
und lassen eine vorhandene Konfiguration nur stehen oder durch eine identische ersetzen: Eine lokale Richtlinie zu
ändern ist eine Entscheidung, keine Nebenwirkung eines Updates.

## Leitstelle

Voraussetzung: ein TLS-Zertifikat für den Namen, unter dem Agenten und Home Assistant die Leitstelle erreichen.

```bash
./build.sh                                               # dist/vordr-hub-linux-amd64
sudo deploy/install-hub.sh dist/vordr-hub-linux-amd64 hub.yaml
```

`install-hub.sh` legt den Systembenutzer `vordr-hub` an und prüft mit `check-config` **als dieser Benutzer**, ob er
`hub.yaml`, Zertifikat und Schlüssel lesen kann. Danach startet der Dienst; beim ersten Start erzeugt er
den Hub-Schlüssel (`/var/lib/vordr-hub/hub.key`, gehört `vordr-hub`). Das Skript zeigt zum Schluss den öffentlichen
Schlüssel für `hub.public_key` in jeder `agent.yaml`. `vordr-hub key` legt nie selbst einen Schlüssel an: Ein von root
erzeugter Schlüssel gehörte dem falschen Benutzer.

Ein erneuertes Zertifikat lädt die Leitstelle beim nächsten Verbindungsaufbau selbst nach; kein Neustart nötig.

Eine geänderte `hub.yaml` ersetzt `install-hub.sh` nur mit Begründung: `--replace-config "<grund>"`. Die alte Datei
bleibt als `hub.yaml.bak-<zeit>` liegen, die Abweichung wird angezeigt und der Grund im Systemlog vermerkt.

## Home Assistant

Integration über HACS installieren (benutzerdefiniertes Repository, Typ Integration; [ha/README.de.md](../ha/README.de.md))
oder `custom_components/vordr` nach `/config/custom_components/vordr` kopieren, HA neu starten, Integration „Vördr“
hinzufügen (Adresse `wss://<leitstelle>:8443/v1/ha`). Sie zeigt ihren Fingerprint; an der Leitstelle freigeben:

```bash
vordr-hub clients                                        # wartende und freigegebene HA-Clients
vordr-hub approve-client <name> --fingerprint <fp>       # nach Abgleich mit dem, was HA zeigt
```

## Agent

```bash
sudo deploy/install-agent.sh dist/vordr-agent-linux-<arch> agent.yaml
```

`install-agent.sh` prüft vorher `check-config` und mit `vordr-agent hub-check`, ob dieser Host die Leitstelle erreicht:
Namensauflösung, Verbindung, TLS-Kette, Tunnel, Challenge. Scheitert eine Stufe, wird nichts installiert. Zum Schluss
gibt es den Freigabe-Befehl für die Leitstelle aus:

```bash
vordr-hub approve <node> --fingerprint <fp>              # an der Leitstelle, nach Abgleich mit dem Host
```

## Agent unter Windows (Windows Update und winget)

Ein Windows-PC meldet die Programme, die winget aktualisieren würde, und aktualisiert sie auf Knopfdruck
(Design 11.1). Beispiel `agent.yaml`:

```yaml
node: pc-a
hub: {url: "wss://hub.example.org:8443/v1/tunnel", public_key: "<vordr-hub key>"}
apps:
  - {id: firefox, name: Firefox, source: winget-package, package: Mozilla.Firefox}
install:
  allowed_scopes: [security, all, "app:firefox"]
  default_scope: all
  timeout_min: 60
```

In einer PowerShell **als Administrator**:

```powershell
.\vordr-agent-windows-amd64.exe check-config --config .\agent.yaml
.\vordr-agent-windows-amd64.exe check --config .\agent.yaml       # was Windows Update und winget anbieten, als JSON
.\vordr-agent-windows-amd64.exe install-service --config .\agent.yaml
```

`install-service` prüft zuerst `agent.yaml`, die Leitstelle und eine schon installierte `agent.yaml`; bis dahin ändert
es nichts. Danach erledigt es alles Weitere in einem Schritt:
Binary nach `%ProgramFiles%\Vordr`, `agent.yaml` nach `%ProgramData%\vordr` (nur SYSTEM und Administratoren),
PowerShell 7 und das PowerShell-Modul `Microsoft.WinGet.Client` für alle Benutzer, Dienst `vordr-agent` unter
LocalSystem. PowerShell 7 braucht der Dienst, weil das Modul als LocalSystem nur dort läuft; die Store-Fassung im
Benutzerprofil zählt nicht, `install-service` installiert deshalb das MSI über winget. Eine andere
`agent.yaml`, die dort schon liegt, ersetzt es nur mit Grund, wie `install-agent.sh`:
`install-service --config .\agent.yaml --replace-config "<grund>"`. Die alte Datei bleibt als
`agent.yaml.bak-<zeit>` liegen, die Abweichung wird angezeigt und der Grund im `agent.log` vermerkt. Scheitert ein
Schritt, nachdem der Dienst angehalten ist, läuft er danach wieder mit der vorigen Binary und `agent.yaml` (Exit 3).
Entfernen mit `vordr-agent uninstall-service`; Identität und `agent.yaml` bleiben.

Selbst-Update (ADR-0084): Mit `agent_update` in `agent.yaml` (wie unter Linux) holt sich der Dienst neue Versionen
über die Leitstelle. `install-service` legt dafür `rollback.cmd` neben die Binary und richtet die Wiederherstellung
des Dienstes ein: Neustart nach 10 s und 1 min, beim dritten Fehlschlag `rollback.cmd`. Das legt die alte Binary nur
zurück, solange ein Selbst-Update offen ist, und startet den Dienst wieder. Eine Installation von Hand entfernt eine
liegengebliebene Update-Markierung, wie `install-agent.sh`.

Angeheftete Pakete (`winget pin add --id <id> --blocking`) lässt der Agent aus. So bleibt z. B. ein Programm auf der
Version, die ein anderes Werkzeug verwaltet.

Windows Update (ADR-0086): Der Agent meldet auch die Software-Updates von Windows Update, ohne Treiber, Funktions-,
Definitions- (Defender holt sie selbst) und optionale Updates; Sicherheitsupdates zählen wie unter Linux. Der Knopf
spielt mit `security` nur die Sicherheitsupdates von Windows ein, mit `all` erst Windows Update, dann winget; dafür
gehört `security` in `allowed_scopes` wie im Beispiel. Automatisch spielt der Agent unter Windows nichts ein
(`install.auto: hub` lehnt er ab), und Neustarts bleiben von Hand. Die automatischen Updates von Windows laufen
weiter. `check` zeigt den Stand der Suche in den Fakten `windows_update_*`.

## Stellvertreter für Appliances (ADR-0089)

Ein Gerät ohne eigenen Agenten (NAS, Firewall, …) bekommt eine **Instanz** des Agenten im Container des
Stellvertreters, je Gerät eine: systemd-Vorlage `vordr-agent@<gerät>`, Richtlinie `/etc/vordr/targets/<gerät>.yaml`,
Zugangsdatei `/etc/vordr/targets/<gerät>.cred` (`KEY=value`, 0600), Zustand `/var/lib/vordr/<gerät>` (Identität,
Journal, Konfigurationssicherungen). Der Knotenname ist der des Geräts.

```yaml
node: nas-a
state_dir: /var/lib/vordr/nas-a
hub: {url: "wss://hub.example.org:8443/v1/tunnel", public_key: "<vordr-hub key>"}
target:
  driver: synology-dsm
  address: https://192.0.2.10:5001
  server_name: nas-a.example.org            # oder fingerprint: <sha256 des Blattes>
  credentials: /etc/vordr/targets/nas-a.cred
config_backup: {keep: 30, max_age_h: 26}
```

```bash
sudo install -m 0644 deploy/systemd/vordr-agent@.service /etc/systemd/system/
vordr-agent check-config --config /etc/vordr/targets/nas-a.yaml
sudo systemctl daemon-reload && sudo systemctl enable --now vordr-agent@nas-a
vordr-agent identity --config /etc/vordr/targets/nas-a.yaml       # Fingerabdruck für vordr-hub approve
```

Im Zielmodus lehnt `check-config` alles ab, was den Container statt das Gerät meinen würde: Installieren, Neustart,
Selbst-Update, Apps, Pakete und andere Gesundheitsprüfungen als http. Alle Instanzen teilen die Binary des Containers:
Sie aktualisiert der gewöhnliche Agent des Containers (`vordr-agent.service` mit `agent_update`, eingerichtet mit
`install-agent.sh`) per Selbst-Update, und nach seiner Bestätigung startet er die laufenden Instanzen neu.
`install-agent.sh` tut dasselbe, wenn es die Binary von Hand ersetzt: Es prüft zuerst die Richtlinie jeder laufenden
Instanz mit der neuen Binary und startet die Instanzen neu, sobald der neue Agent oben bleibt
(Design [Stellvertreter 4.3](../design/proxy-agent.de.md#43-selbst-update-der-instanzen)).

## Agent-Releases signieren

Agenten aktualisieren sich nur mit einem Release, das der Betreiber-Schlüssel aus ihrer eigenen `agent.yaml` signiert
hat ([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md),
[ADR-0139](../docs/adr/0139-the-release-signer-is-part-of-the-repository.md)). Signiert wird auf dem Rechner des
Betreibers, nie auf der Leitstelle:

```bash
vordr-release keygen operator.key                   # einmal: Schlüssel anlegen (0600), zeigt den öffentlichen Schlüssel
vordr-release public-key operator.key               # -> agent_update.public_key in agent.yaml und hub.yaml
./build.sh 1.2.3                                    # Binaries mit genau dieser Version
vordr-release sign operator.key 1.2.3 dist release-1.2.3   # Binaries, manifest.sig, manifest.json
```

Den Inhalt von `release-1.2.3` nach `<state_dir>/agent` der Leitstelle kopieren, `manifest.json` zuletzt. Eine Sicherung
des Schlüssels gehört getrennt von der Leitstelle aufbewahrt; ein Ersatzschlüssel (`backup_public_key`) getrennt vom
Hauptschlüssel ([ADR-0035](../docs/adr/0035-backup-operator-key.md)). Ein vorhandener Schlüssel wird nie ersetzt.

## Abschottung der Dienste

| Dienst | Benutzer | Abschottung | `systemd-analyze security` |
|---|---|---|---|
| `vordr-hub` | `vordr-hub` | ohne Capabilities, System schreibgeschützt, nur eigenes State- und Runtime-Verzeichnis, Systemaufrufe auf `@system-service` begrenzt | 1,3 OK |
| `vordr-agent` | root | nur, was nie einen Paketlauf stört (eigenes `/tmp`, Adressfamilien, Echtzeit, Architektur) | 8,5 – bewusst |
| `vordr-agent@<gerät>` | root ohne Capabilities | System schreibgeschützt, kein `/home`, nur das eigene State-Verzeichnis, keine Geräte, kein W^X-Bruch, keine neuen Rechte | 3,7 OK |

Die Stellvertreter-Instanz installiert nichts auf ihrem Container; sie spricht nur mit ihrem Gerät und der Leitstelle.
Darum gilt für sie die enge Abschottung, die dem Host-Agenten Paketläufe brechen würde.

Der Agent läuft als root, weil apt es braucht: Er installiert Pakete und ersetzt beim Selbst-Update sich selbst.
`ProtectSystem`, `ProtectHome` oder `ProtectKernelModules` würden Paket-Installationen brechen: Maintainer-Skripte
schreiben nach `/usr`, `/etc`, `/home` und `/usr/lib/modules`. Seine Grenzen sind die lokale Richtlinie und die
geschlossene Verbmenge, nicht die Sandbox.

## Befehle aus Home Assistant

Befehle kommen nur über die authentifizierte Verbindung eines freigegebenen HA-Clients. Die Leitstelle prüft sie gegen
Verben und Richtlinie des Hosts, bevor sie signiert, und der Agent prüft jeden Auftrag noch einmal. Ein Client kann
keine Hosts freigeben und nichts an ihren Richtlinien ändern; `vordr-hub reject-client` / `forget-client` widerrufen ihn
und trennen seine Verbindung sofort.
