# Vördr – Stellvertreter-Agent für Appliances

[English](proxy-agent.md) · Deutsch

> **Stand:** Gebaut sind Melden und Sichern über die Treiber `synology-dsm` und `pfsense` (4.1), die Instanz-Units
> (4.7), die Konfigurationssicherung samt Prüfung an der Leitstelle (4.4) und der feste Befehl auf der Firewall (4.6).
> Installieren über einen Treiber ist noch nicht gebaut; `check-config` lehnt es ab. Begründungen:
> [ADR-0089](../docs/adr/0089-appliances-through-a-proxy-agent.md) (Stellvertreter statt Leitstelle),
> [ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md) (Konfigurationssicherung vor jedem
> Update), [ADR-0091](../docs/adr/0091-firmware-for-devices-without-internet.md) (Geräte ohne Internet),
> [ADR-0092](../docs/adr/0092-mesh-keeps-its-vendor-self-update.md) (Mesh-WLAN behält sein Selbst-Update).
> Ergänzt [architecture.de.md](architecture.de.md). Technische Namen englisch, Text deutsch. Wie die übrigen
> Design-Dokumente ohne Hostnamen und Adressen. Geräte stehen mit ihrer Klasse; ein Produkt nur, wo sein Treiber
> gebaut ist (ADR-0099).

## 1. Anlass

Hat jeder Rechner, auf dem ein Agent laufen kann, einen, bleiben Appliances mit Hersteller-Firmware übrig: Firewall,
NAS, Mesh-WLAN, Gateways, Drucker. Naheliegend wäre, sie als „virtuelle Agenten“ direkt auf der Leitstelle zu
betreiben, mit verschiedenen Wegen je Gerät (Skripte, REST, SSH).

## 2. Entscheidungen

1. **Ein Stellvertreter in einem eigenen, abgeschotteten Container**, nicht die Leitstelle. Er spricht nur mit den
   eingetragenen Geräten und mit der Leitstelle. Nebenbei löst das ein zweites Problem: Geräte in einem anderen Netz
   oder Segment, die man sonst nur über einen Sprung-Host erreicht, bekommen dort einen eigenen Stellvertreter; die
   Leitstelle braucht keinen Weg dorthin.
2. **Geräteklassen:** Firewall, Smart-Home-Gateway, NAS, Mesh-WLAN (jede Einheit); gebaut sind die Treiber für
   pfSense und Synology DSM. Weitere Geräteklassen in Abschnitt 6.
3. **Keine HA-Integrationen für Firewall und Mesh-WLAN:** Sie geben Home Assistant zu viel Zugriff auf diese Systeme;
   gerade die Firewall in HA wäre ein Risiko. Stattdessen eigene Treiber.
4. **Konfigurationssicherungen prüfen** statt `without`: Vor jedem Update braucht es eine frische Sicherung
   (Abschnitt 4.4).
5. **Geräte ohne Internet:** Geräte, die selbst kein Internet brauchen, bekommen es auch fürs Update nicht dauerhaft:
   Entweder holt der Stellvertreter die Firmware, oder ein fester Befehl gibt kurz und eng Internet frei (Abschnitt
   4.6, ADR-0091). Ein Drucker, der nur im lokalen Netz druckt, gehört dazu.
6. **Mesh-WLAN:** Das Selbst-Update des Masters bleibt; Vördr meldet und sichert (ADR-0092).
7. **Je Netz ein Stellvertreter:** Wer Geräte in einem abgetrennten Segment hat (etwa einem Verwaltungsnetz für die
   Server-Fernwartung), stellt dort einen eigenen Stellvertreter auf (Abschnitt 3 und 4.7).
8. **Je Gerät ein eigenes Dienstkonto** (Abschnitt 7).

Variante „in der Leitstelle“ und ihre Folgen: ADR-0089. Kurz: Die Leitstelle hielte sonst Admin-Zugänge zu den
Geräten und wäre damit der zentrale Server mit Fernausführung, den Vördr ersetzt
([ADR-0001](../docs/adr/0001-own-agent-instead-of-patchmon.md),
[ADR-0006](../docs/adr/0006-local-policy-decides.md)).

## 3. Überblick

```
┌────────── Stellvertreter-Container ──────────┐
│ vordr-agent@firewall ── feste Befehle ────────┼──▶ Firewall (nur melden)
│ vordr-agent@gateway  ── Web-API ──────────────┼──▶ Smart-Home-Gateway
│ vordr-agent@nas      ── DSM-Web-API ──────────┼──▶ NAS
│ vordr-agent@mesh-…   ── Web-Login ────────────┼──▶ Mesh-WLAN, je Einheit (nur melden)
│   je Instanz: Identität, Richtlinie,          │
│   Zugangsdaten (0600), eigener Zustand,       │
│   Konfigurationssicherungen                   │
└──────────────┬────────────────────────────────┘
               │ nur ausgehend, wss /v1/tunnel (wie jeder Agent)
               ▼
           vordr-hub ──▶ Home Assistant (ein Gerät je Appliance)
```

- **Eine Agent-Instanz je Gerät** (systemd-Vorlage `vordr-agent@<name>`): eigene Ed25519-Identität, eigener
  Knotenname, eigene `agent.yaml`, eigenes Zustandsverzeichnis. Für die Leitstelle ist jede Instanz ein gewöhnlicher
  Host: Freigabe per Fingerabdruck, Problemlogik, Knöpfe, Ereignisse. Leitstelle und Protokoll ändern sich nicht;
  einzig die Backup-Prüfung lernt eine zweite Art (4.4).
- **Treiber statt Paketquelle:** Mit `target:` liest die Instanz nicht die Pakete des Containers, sondern fragt über
  ihren Treiber das Gerät. Ein Treiber kann genau vier Dinge: prüfen, sichern, installieren, nachweisen. Keiner führt
  beliebige Befehle aus; wo SSH oder eine Admin-Oberfläche der Weg ist, sind die Anfragen fest im Treiber.
- **Mehrere Netze:** Ein Stellvertreter steht dort, von wo er seine Geräte erreicht. Für ein abgetrenntes Segment
  (z. B. Verwaltungsnetz der Server-Fernwartung) ein eigener Container in diesem Segment; nach aussen braucht er nur
  den Weg zur Leitstelle.

## 4. Bau

### 4.1 `agent.yaml` einer Instanz

```yaml
node: <name>                       # Gerätename, wie er in HA erscheinen soll
hub: {url: ..., public_key: ...}
target:
  driver: synology-dsm             # gebaut: synology-dsm, pfsense; weitere je Geräteklasse
  address: https://<adresse>:5001  # dorthin verbindet der Treiber; HTTP-Host ist diese Adresse
  # TLS genau eines von beiden, nie "nicht prüfen":
  server_name: <name>              # Zertifikat für diesen Namen (auch als SNI), Wurzeln des Systems oder ca_file
  # fingerprint: "<sha256>"        # oder: Gerät mit eigenem Zertifikat, festgenagelt (64 Hex-Ziffern, ":" erlaubt)
  credentials: /etc/vordr/targets/<name>.cred   # USER=/PASS=, 0600
config_backup:                     # ADR-0090; ohne Angabe diese Werte
  keep: 30                         # so viele Stände bleiben im Zustandsverzeichnis
  max_age_h: 26                    # älter: HA zeigt ein Problem (failed_config_backup)
health:
  - http: https://<gerät>/         # nach einem Update, vom Stellvertreter aus
```

- **Gebaut:** melden und sichern. `install` ist mit `target:` noch nicht erlaubt; Installieren kommt mit dem ersten
  Treiber, der es kann.
- `check-config` prüft den Abschnitt wie jeden anderen: bekannter Treiber, `https`-Adresse ohne Pfad, genau ein TLS-Weg,
  Zugangsdatei absolut und nur für ihren Besitzer lesbar. Mit `target:` sind `install`, `install.auto`,
  `reboot.allowed`, `agent_update`, `apps`, `packages` (das Inventar ist aus) und `health` ausser `http` nicht erlaubt:
  Sie meinten den Container, nicht das Gerät.
- **NAS (synology-dsm):** Hat DSM für seinen Host-Namen eine eigene Regel (z. B. Reverse Proxy), liefert es unter dem
  Namen auf Port 5001 überall 404, unter der Adresse die Web-API. Darum `address` = Adresse, `server_name` = Name.
  Ein Dienstkonto ohne Admin-Gruppe darf sich nur an einer bekannten Anwendung anmelden (Sitzung `webui`) und bekommt für Update-Prüfung und Einstellungen
  Fehler 105; das Konto braucht die Gruppe `administrators`. Export: `SYNO.Backup.Config.Backup` `start` → `status` bis
  fertig → `download`, eine xz-komprimierte `.dss`-Datei.
- **Firewall (pfsense, [ADR-0094](../docs/adr/0094-ssh-drivers-use-the-system-openssh-client.md)):** über SSH zum
  festen Befehl `vordr-gna` ([ADR-0093](../docs/adr/0093-firewall-command-unprivileged-root-job-works-a-checked-queue.md)),
  mit dem OpenSSH-Client des Systems. Statt `server_name`/`fingerprint`:

  ```yaml
  target:
    driver: pfsense
    address: ssh://vordr-gna@192.0.2.1           # Benutzer gehört zur Adresse
    host_key: "ssh-ed25519 AAAA..."              # festgenagelt, aus ssh-keyscan
    credentials: /etc/vordr/targets/fw-a.cred    # KEY=<base64 des privaten OpenSSH-Schlüssels>
  ```

  Vor `ssh` prüft der Treiber Fakten: Adresse erreichbar (sonst `precheck_repo_unreachable`), Gerät bietet genau den
  festgenagelten Host-Schlüssel (`ssh-keyscan`, sonst `failed_target_hostkey`). Ein `ssh`-Abbruch (255) danach ist die
  Anmeldung (`failed_target_auth`); jeder andere Rückgabewert die Ablehnung des festen Befehls mit dessen `reason`.
  `ssh` läuft mit `-F /dev/null`, `BatchMode`, `IdentitiesOnly`, ohne Agent, mit einer eigenen `known_hosts` nur mit
  dem festgenagelten Schlüssel; der private Schlüssel liegt nur während des Aufrufs in einer 0600-Datei. Bericht:
  Firmware aus `version` (Form `<version>-RELEASE`), ein Angebot „pfSense“, wenn der Katalog ein neueres
  `pfSense-base` hat, und je veraltetem Paket ein Angebot ohne Version; `self_update: off`. Sicherung: `config.xml` (XML-Kopf, Wurzel
  `<pfsense>`, mindestens 1 KB).
- **Fakten** kommen vom Treiber: Produkt und Firmware als `os`, der Treiber als `os_id`, Modell, Update-Kanal, ob das
  Gerät sich selbst aktualisiert, Zeit und Prüfsumme der letzten Konfigurationssicherung.

### 4.2 Treiber-Schnittstelle (Agent, Go)

| Schritt | Was der Treiber tut | Ergebnis |
|---|---|---|
| prüfen | installierte Firmware lesen, angebotene Version beim Gerät erfragen | Bericht wie bei apt: Quelle = Treiber, `pending`, ein „Paket“ je Firmware bzw. je Gerätepaket (von → nach); `security` nur, wo der Hersteller es ausweist, sonst 0 mit Fakt `security_classification: none` |
| sichern | Konfiguration exportieren, auf Form prüfen (nicht leer, lesbar, erwartete Wurzel bzw. Kopf), mit Zeit und Prüfsumme ablegen, alte Stände bis `keep` löschen | Fakt `config_backup_at`; gescheitert: `check_failed` mit eigenem Code |
| installieren | Vorprüfung (frische Sicherung, Gerät gesund), Update anstossen | Phase `installing`, dann `rebooting`, solange das Gerät nicht antwortet |
| nachweisen | warten, bis das Gerät wieder antwortet und die neue Version meldet (Frist `timeout_min`) | `ok` oder `failed_verification` mit Grund; danach `health:` |

Feste Codes wie überall ([ADR-0007](../docs/adr/0007-fixed-codes-instead-of-error-texts.md)), entschieden am Typ des
Fehlers, nie an seinem Text: Gerät nicht erreichbar → `precheck_repo_unreachable`; neu: Gerät lehnt das Dienstkonto ab
(`failed_target_auth`), Zertifikat passt nicht zum Fingerabdruck bzw. Namen (`failed_target_tls`), SSH-Host-Schlüssel
passt nicht (`failed_target_hostkey`), Sicherung
gescheitert oder älter als `max_age_h` (`failed_config_backup`). Andere Fehler des Geräts: `failed_package_manager`.

**Bericht einer Instanz:** zwei Quellen. Die Firmware unter dem Namen des Treibers (Fakten `model`, `self_update`,
gerätespezifische wie `update_check`), daneben `config-backup` mit `config_backup_at` und `config_backup_sha256`. Ein
Export wird erneuert, sobald der letzte älter ist als `max_age_h` weniger 2 Stunden; antwortet das Gerät gar nicht,
wird kein Export versucht. Scheitert die Erneuerung, solange der letzte noch jünger als `max_age_h` ist, bleibt die
Quelle in Ordnung und nennt den Grund im Auszug; danach `failed_config_backup`.

### 4.3 Selbst-Update der Instanzen

Alle Instanzen teilen eine Binary. Nur der gewöhnliche Agent des Containers (er meldet dessen eigene Debian-Pakete)
holt neue Releases. Die Instanzen haben `agent_update` aus.

- Sobald der neue Agent des Containers sein Update bestätigt hat (erste freigegebene Verbindung), startet er die
  laufenden Instanzen neu: `systemctl try-restart --no-block vordr-agent@*.service`. Erst dann, weil eine neue Binary,
  die scheitert, sonst auch die Instanzen mitnähme; mit dem Rücksprung kämen sie nicht zurück.
- Springt das Update zurück, bleiben die Instanzen unberührt. Sie laufen weiter mit der Binary, die wieder an ihrem
  Platz liegt.
- Ohne diesen Schritt liefe eine Instanz bis zu ihrem nächsten Start mit der ersetzten Datei weiter, und HA zeigte für
  sie dauerhaft ein Agent-Update an.

### 4.4 Konfigurationssicherung (ADR-0090)

- **Wer:** der Treiber selbst, täglich mit der Prüfung und noch einmal unmittelbar vor jedem Installieren. Werkzeuge
  erledigen ihren Auftrag samt Prüfung: Ein Update ohne frische, geprüfte Sicherung beginnt nicht.
- **Wo:** im Zustandsverzeichnis der Instanz (0700, root). Wird der Container wie ein Gast
  gesichert (etwa nach PBS), liegen die Stände auch ausserhalb des Geräts. Kein Herstellerkonto, keine Cloud.
- **Leitstelle:** Für Appliances steht in `backup.guests` statt PBS-Gruppe oder `without` neu `config_backup: true`.
  Die Backup-Prüfung vor einem Auftrag nimmt dann das Alter aus dem gemeldeten Fakt `config_backup_at` (gleiche
  Grenze `max_age_hours`) und lehnt wie bisher mit `backup_stale` bzw. `backup_missing` ab. Die Leitstelle braucht
  dafür keinen Zugang zu den Sicherungen. Der Agent prüft dasselbe noch einmal selbst. Massgeblich ist der letzte
  Bericht des Agenten (Quelle `config-backup`); PBS wird für diese Hosts nicht gefragt. Der Sensor „Letztes Backup“
  zeigt den gemeldeten Zeitpunkt. Ohne Eintrag bleibt die Appliance `unmapped`, und jeder Install wird abgelehnt.
- **Inhalt ist geheim:** Konfigurationen können Passwort-Hashes, VPN- und WLAN-Schlüssel enthalten. Sie verlassen den
  Container nur über dessen Backup; nie in Logs, Ereignisse oder HA.

### 4.5 Fenster und Neustart

- **Nie automatisch durch Vördr.** Firmware-Updates per Knopf; ausserhalb des Fensters mit Grund wie überall
  ([ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md)). Aktualisiert sich ein Gerät selbst
  (Mesh-Master, NAS-Sicherheits-Hotfixes, Drucker), meldet der Treiber das als Fakt (`self_update: on`).
- **Neustart** gehört zum Firmware-Update und ist kein eigenes Verb; die Instanz kündigt `reboot` nicht an.

### 4.6 Geräte ohne Internet (ADR-0091, ADR-0093)

Manche Geräte sollen bewusst kein Internet haben (etwa über eine Firewall-Regel für eine Gruppe solcher Geräte). Für
ihr Update gibt es zwei Wege, in dieser Rangfolge:

1. **Der Stellvertreter holt die Firmware**, prüft Signatur bzw. Prüfsumme des Herstellers und bietet sie dem Gerät
   an, das sie aus dem lokalen Netz zieht oder hochgeladen bekommt. Das Gerät sieht nie das Internet. Geht, wo das
   Gerät eine Datei oder ein lokales Repository annimmt: Server-Fernwartung (signierter Hersteller-Katalog als lokales Repository),
   Drucker (Firmware-Datei über die Oberfläche, zu prüfen), NAS (Update-Datei über die API, zu prüfen).
2. **Kurzzeitige Freigabe**, wo das Gerät nur selbst beim Hersteller laden kann (etwa Unterhaltungselektronik mit
   Netzwerk-Update).
   - In der Firewall eine Laufzeit-Tabelle `vordr_update_window` und eine Regel davor: Geräte in der Tabelle dürfen
     nur zu den Update-Servern ihres Herstellers (FQDN-Alias je Hersteller), sonst nichts.
   - Der Stellvertreter ändert **nicht die Firewall-Konfiguration**, sondern ruft per SSH einen festen Befehl auf dem
     Firewall-Host auf (`command=` im Schlüssel, nur für diesen Zweck). Der Befehl nimmt nur Adressen aus einer Liste,
     die auf der Firewall liegt, trägt sie in die Tabelle ein und entfernt sie nach höchstens 30 Minuten selbst.
   - Die Freigabe gehört zum Lauf: Sie beginnt nach Sicherung und Vorprüfung, endet nach dem Nachweis, und jeder Lauf
     prüft am Anfang, dass die Tabelle leer ist. Ereignis und Grund stehen im Log.
   - Der Stellvertreter bekommt so genau eine Fähigkeit dazu: gelistete Geräte kurz zu gelisteten Herstellern lassen.
     Er bekommt keinen Zugang zu Regeln, Aliasen oder Einstellungen.

**Gebaut (`deploy/pfsense/`, ADR-0093):**

| Teil | Ort auf der Firewall | läuft als | macht |
|---|---|---|---|
| fester Befehl `vordr-gna` | `/usr/local/sbin/vordr-gna` (0755) | `vordr-gna`, nur über den Schlüssel des Stellvertreters (`command=` in der Schlüsselzeile) | `version`, `backup`, `window status`, `window open <adresse> <minuten>`, `window close <adresse>`; legt Aufträge nur in die Warteschlange |
| Wurzel-Job `vordr-gna-root` | `/usr/local/sbin/vordr-gna-root` (0700), Cron jede Minute unter `lockf` | root | prüft jeden Auftrag erneut, trägt in die pf-Tabelle `vordr_update_window` ein, schliesst Abgelaufenes, gleicht Tabelle und Zustand ab, schreibt stündlich die Update-Fakten |
| Geräteliste | `/usr/local/etc/vordr-gna-allow` (0644) | – | Adressen der Geräte, die ein Fenster bekommen dürfen, eine je Zeile, z. B. `192.0.2.40`; die Liste gehört dem Standort, das Repo liefert sie leer |
| Warteschlange, Zustand | `/var/db/vordr-gna-queue` (vordr-gna, 0700), `/var/db/vordr-gna/` (root) | – | `window`: `entry=<adresse> <ende>`; `updates`: `base_installed`, `base_offered`, `packages_outdated(_count)` |

- Antworten sind `key=value`-Zeilen; eine Ablehnung sagt `state=refused` und `reason=<code>` (`unknown_call`,
  `bad_arguments`, `bad_address`, `bad_minutes`, `not_listed`, `not_ready`), der Rückgabewert die Art (2, 3, 4).
- Ein Fenster geht bis zu einer Minute nach dem Auftrag auf bzw. zu; der Treiber wartet auf `window status`.
- Die Regel „Tabelle → Update-Server des Herstellers“ gehört nicht zu `deploy/pfsense/`: Sie kommt mit dem ersten
  Treiber, der ein Fenster braucht, und nennt die Update-Server seines Herstellers. Ohne sie bewirkt ein Eintrag in
  der Tabelle nichts.
- Das Konto `vordr-gna` bekommt ein Zufallspasswort, das nirgends steht; anmelden soll es sich nur mit dem Schlüssel.
  Nimmt der SSH-Dienst der Firewall auch Passwörter an, hängt die Bindung an den festen Befehl an diesem Passwort;
  sicherer ist SSH nur mit Schlüssel.

### 4.7 Container

- **Name der Bauteile:** Die Teile auf der Firewall heissen `vordr-gna…` nach Gná, Friggs Botin, die in fremden
  Welten Aufträge für andere erledigt: Stellvertreter und Sprung in andere Netze in einem.
- **Ort:** ein kleiner, unprivilegierter Container je Netz, aus dem Geräte erreicht werden müssen (erste Umgebung:
  LXC unter Proxmox VE mit Debian), feste Adresse, gesichert wie jeder Gast. Ein Stellvertreter in einem abgetrennten
  Segment braucht nach aussen nur den Weg zur Leitstelle (8443) und kein Bein in einem anderen Netz.
- **Ausgehend gesperrt durch den Hypervisor:** Die Regeln stehen in der Firewall des Hypervisors für diesen Container
  (ausgehend standardmässig verwerfen; erlaubt: Leitstelle 8443, DNS, die Geräte auf ihren Ports). Im Container selbst
  könnte ein Angreifer mit root nftables abschalten, die Regeln des Hypervisors nicht.
  - Unter Proxmox VE stehen sie in `/etc/pve/firewall/<vmid>.fw`: erlaubt DNS, die Leitstelle, die eingetragenen
    Geräte auf ihren Ports (z. B. SSH zur Firewall), dazu 80/443 ins Internet (Pakete, Hersteller-Firmware); alles
    andere im privaten Netz wird verworfen.
  - Die Regeln greifen nur, wenn die Proxmox-Firewall auf Rechenzentrums- und Gastebene eingeschaltet ist
    (Rechenzentrum an, Regeldatei des Containers mit `enable: 1`). Gäste ohne eigene Regeldatei bleiben ungefiltert,
    solange die Richtlinie des Rechenzentrums alles erlaubt.
  - Nachweis aus dem Container: jedes erlaubte Ziel offen, ein nicht erlaubtes zu.
  - Lässt die Leitstelle Port 8443 nur bekannte Absender zu
    ([ADR-0056](../docs/adr/0056-firewall-8443.md)), kommt der Stellvertreter-Container auf diese Liste, sobald sein
    Agent aufgenommen ist.
- **Kein SSH-Dienst;** verwaltet wird vom Hypervisor aus (unter Proxmox VE `pct exec`).
- **Instanzen:** je Gerät eine systemd-Instanz `vordr-agent@<gerät>` (Vorlage `deploy/systemd/vordr-agent@.service`,
  Einrichtung in `deploy/README.de.md`). Knotenname = Gerätename, eigene Identität und eigener Zustand je Instanz. Die
  Instanz läuft als root ohne Capabilities, mit schreibgeschütztem System und nur ihrem State-Verzeichnis
  (`systemd-analyze security` 3,7).
  - **Aufnahme:** wie jeder Host an der Leitstelle freigeben, nach Abgleich des Fingerabdrucks im Container
    (`vordr-agent identity --config …`) mit dem an der Leitstelle. Danach meldet die Instanz die Firmware und legt
    Konfigurationssicherungen ab (0600).
  - **Zertifikat:** Prüft die Instanz das Zertifikat des Geräts per Name mit den Wurzeln des Systems, muss das Gerät
    bei einem Let's-Encrypt-Zertifikat die Kreuzsignatur der neuen Wurzel über ISRG Root X1 mitliefern; sonst
    `fingerprint` statt `server_name`.
  - **Zugangsdaten:** Die Zugangsdatei (`KEY=value`, 0600) kommt beim Einrichten in den Container, etwa vom Hypervisor
    aus (`pct push`), und liegt ausserhalb des Containers nirgends länger als nötig. Die Instanz liest sie beim Start.
    Vördr erneuert sie nicht selbst: Ein neues Passwort heisst, die Datei neu zu schreiben und die Instanz neu zu
    starten (`systemctl restart vordr-agent@<gerät>`).

## 5. Erste Geräteklassen

Wie ein Treiber mit der Geräteklasse spricht und worauf zu achten ist. „Zu prüfen“ heisst: vor dem Bau am Gerät
nachzuweisen, nur lesend.

| Gerät | Zugang | Update-Stand | Sichern (Treiber) | Installieren | Zu beachten |
|---|---|---|---|---|---|
| Firewall (pfSense) | Admin-Oberfläche bzw. SSH, feste Befehle | Firmware und angebotene Basis-Version, dazu Paket-Updates; ohne Treiber erscheint das nirgends in HA (Abschnitt 2, Punkt 3) | `config.xml` lesen, XML-Wurzel prüfen | nur melden | Stände auf dem Gerät (bei jeder Änderung) liegen nicht ausser Haus; die Cloud-Sicherung des Herstellers ist keine (4.4: kein Herstellerkonto) |
| Smart-Home-Gateway | Web-API, SSH | installierte Version, Update-Kanal, angebotenes Update | Sicherungsfunktion der Oberfläche (zu prüfen, welcher Weg der offizielle ist) | per Knopf | – |
| NAS (Synology DSM) | DSM-Web-API | Update-Prüfung des NAS, auch mit Hauptversionen; ob es Sicherheits-Hotfixes selbst installiert | Export über `SYNO.Backup.Config.Backup` (4.1) | per Knopf, später | Endet die Downloadliste des Herstellers für das Modell, kommt keine neue Hauptversion mehr; wie lange die letzte noch Sicherheitsupdates bekommt, sagt der Supportstatus des Herstellers |
| Mesh-WLAN | Web-Login (SSH meist aus) | Firmware je Einheit, Meldung des Herstellers; ob sich eine Einheit selbst aktualisiert (meist nur der Master) | Einstellungsdatei der Oberfläche (zu prüfen) | nur melden (ADR-0092) | oft **eine Admin-Sitzung**: eine Abfrage kann eine offene Sitzung im Browser hinauswerfen → selten abfragen, danach abmelden |
| Smart-Home-Server | Web-API mit App-Token | installierte Version; ob er ein angebotenes Update meldet, ist zu prüfen (nicht jeder tut es) | zu prüfen | nur Version melden, wo er kein Angebot meldet | – |

## 6. Weitere Geräteklassen

| Gerät | Prüfen | Update | Internet |
|---|---|---|---|
| Server-Fernwartung (BMC) | Redfish mit **Nur-Lese-Konto** (das Firmware-Inventar braucht oft ein Konto); manche BMCs prüfen auf Wunsch selbst gegen ein Hersteller-Repository und nennen die passenden Updates, ohne zu installieren | per Knopf aus einem **lokalen Repository beim Stellvertreter im Verwaltungsnetz** (Hersteller-Katalog, signiert) – BMC, BIOS, RAID-Controller, Netzwerkkarten | Verwaltungsnetz, Weg 1 aus 4.6 |
| Drucker | Version über die Geräte-API (oft ohne Anmeldung lesbar); angebotenes Update und Auto-Update oft nur in der Admin-Oberfläche (zu prüfen) | per Knopf, Firmware-Datei über die Oberfläche (zu prüfen) | Ist er beim Cloud-Dienst des Herstellers angemeldet, aktualisiert er sich womöglich selbst; besser ohne Internet, dann Weg 1 aus 4.6 |
| Unterhaltungselektronik mit Netzwerk-Update (z. B. AV-Receiver) | lokale API, oft ohne Anmeldung; eine Meldung „neue Firmware verfügbar“ kann aus der Zeit stammen, bevor das Gerät vom Internet getrennt wurde | per Knopf, Netzwerk-Update mit kurzer Freigabe (Weg 2 aus 4.6) | besser ohne, dann Weg 2 aus 4.6 |
| Bridges | lokale API mit eigenem Schlüssel | – (später) | – |
| Switches | Version oft per SSH, aber ohne Abgleich mit dem Hersteller | nicht | – |
| Geräte, deren Firmware eine HA-Integration schon meldet (Kameras, ESPHome-Geräte, Lautsprecher …) | – | bei HA lassen | – |
| Geräte, die der Hersteller über seine Cloud aktualisiert (Haushaltsgeräte, Heizung, TV-Boxen …) | – | nicht | – |

## 7. Sicherheit

- **Leitstelle:** hält keine Zugangsdaten. Übernommen kann sie Prüfungen und, wo die Richtlinie einer Instanz es
  erlaubt, ein Firmware-Update anstossen – wie bei Hosts. Das Bedrohungsmodell (architecture.de.md 5) bekommt eine Zeile:
  „den Stellvertreter übernommen → kann die Geräte bedienen, deren Zugangsdaten er hält, und ihre
  Konfigurationssicherungen lesen“.
- **Container:** nur für diesen Zweck; kein SSH-Dienst, Verwaltung über den Hypervisor. Ausgehend nur, was die
  Firewall des Hypervisors erlaubt (4.7): Leitstelle (8443), DNS, die eingetragenen Geräte auf ihren Ports, dazu
  HTTP(S) ins Internet für Pakete und Hersteller-Firmware.
- **Je Instanz eigene Zugangsdatei und eigener Zustand,** nur für root lesbar; die Unit lässt die Instanz nur in ihr
  eigenes Zustandsverzeichnis schreiben (4.7). Ein eigener Benutzer je Instanz ist noch nicht gebaut: Lesen kann eine
  Instanz auch die Dateien der anderen im selben Container.
- **Eigene Dienstkonten** auf jedem Gerät, nie ein persönliches Konto oder eines, das schon ein anderes Werkzeug
  nutzt. Nur-Lese-Rechte, wo nur gemeldet wird – soweit das Gerät sie
  kennt; manche (Mesh-WLAN, Firewall-Oberfläche) kennen nur Admin.
- **Zugangsdaten nur in der Zugangsdatei der Instanz** (4.7), nie in `agent.yaml`, Logs, Ereignissen oder HA, nie auf
  der Leitstelle. Woher der Betreiber sie bringt (Secret-Store, von Hand), liegt bei ihm; Vördr liest nur die Datei.
- **Geschlossene Treiber:** feste Anfragen bzw. Befehle; nichts davon kommt von der Leitstelle.

## 8. Schritte

1. Agent: `target:`, `config_backup:`, Treiber-Schnittstelle, Instanz-Units, Codes. Leitstelle: `config_backup: true`
   in `backup.guests`. Tests mit aufgezeichneten Antworten der Geräte; Mutationsprobe. **Gebaut.**
2. Stellvertreter-Container einrichten (Firewall des Hypervisors, Zugangsdateien, Dienstkonten auf den Geräten;
   4.7).
3. Treiber je Geräteklasse, je erst melden + sichern; wo ein Gerät kein angebotenes Update meldet, nur die Version.
   **Gebaut für pfSense und Synology DSM.**
4. Stellvertreter in einem Verwaltungsnetz, Treiber für die Server-Fernwartung (melden über ein lokales
   Hersteller-Repository).
5. Installieren per Knopf, je Geräteklasse mit Sicherung davor und Nachweis danach. Geräte ohne Internet, die nur
   selbst laden können, erst mit der kurzzeitigen Freigabe (4.6).

## 9. Entschieden und offen

Entschieden:

1. **Mesh-WLAN:** Selbst-Update des Masters bleibt (ADR-0092). Er bringt die Knoten mit; Sicherheitskorrekturen
   kommen so ohne Zutun. Vördr meldet, sichert täglich und zeigt ein Problem, wenn eine Einheit abweicht oder ein
   angebotenes Update 7 Tage liegt.
2. **NAS:** Vördr meldet, was die Update-Prüfung des NAS anbietet, auch Hauptversionen. Bekommt die Hardware keine
   neuere mehr
   (Downloadliste des Herstellers für das Modell), bleibt es bei seinem Stand mit den Sicherheits-Hotfixes des
   Herstellers (Abschnitt 5).
3. **Geräte ohne Internet:** ADR-0091; ein Drucker, der kein Internet braucht, gehört dazu.

Offen:

1. **Dienstkonten je Geräteklasse:** Nur-Lese-Konto an der Server-Fernwartung, Dienstkonto am NAS, SSH-Benutzer mit
   festem Befehl an der Firewall, App-Token am Smart-Home-Server. Mesh-WLAN, manche Drucker und Gateways kennen nur das
   eine Admin-Konto; dann hält der Stellvertreter dessen Zugangsdaten, mit den Folgen aus Abschnitt 7.
