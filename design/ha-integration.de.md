# Vördr – eigene Home-Assistant-Integration statt MQTT

[English](ha-integration.md) · Deutsch

> **Stand:** Gebaut (Abschnitt 10). Home Assistant spricht nur noch über die Integration mit der Leitstelle; MQTT gibt
> es seit Leitstelle 0.6 nicht mehr ([ADR-0026](../docs/adr/0026-remove-mqtt.md)). Einzelheiten aus dem Bau stehen in
> den Abschnitten 5a, 6a und 6b; die Updates von Home Assistant selbst unter den Regeln von Vördr, geplant und,
> sobald eingeschaltet, installiert, in 6c. Ergänzt [architecture.de.md](architecture.de.md), Abschnitt 8 (HA-Entitäten). Technische
> Namen englisch, Anzeigenamen über die Übersetzungen der Integration (de/en, folgen der Systemsprache von HA).

## 1. Anlass

Leitstellen bis 0.5 meldeten an Home Assistant über MQTT-Discovery. Dabei zeigte sich
([ADR-0023](../docs/adr/0023-own-ha-integration.md)):

1. **Die Geräte stehen unter „MQTT“** zwischen allen anderen MQTT-Geräten statt unter einer eigenen Integration.
   Gleichnamige Geräte anderer Integrationen liessen sich nur mit einem Präfix im Namen auseinanderhalten.
2. **Eine MQTT-update-Entität kann „unbekannt“ nicht darstellen.** HA prüft `latest_version` als Text und verwirft
   die ganze Nachricht bei `null`. Der Umweg ist „nicht verfügbar“. Eine eigene `UpdateEntity` kennt
   `latest_version = None` und zeigt „unbekannt“.
3. **Befehle über MQTT sind nicht authentifiziert.** Ohne Broker-ACL kann jedes Konto des Brokers auf
   `vordr/host/+/cmd` schreiben. Leitstelle und Agent prüfen zwar jeden Befehl gegen Verben und Richtlinie, aber
   sobald Vördr installiert, wäre eine Broker-ACL Pflicht – und die wirkt auf alle Konten des Brokers.

Kein Grund ist die Firewall: Agenten sprechen kein MQTT, ein Host braucht nur den Weg zur Leitstelle.

## 2. Entscheidung: Die Leitstelle bleibt ein eigener Dienst

Begründung als ADR: [ADR-0022](../docs/adr/0022-hub-stays-own-service.md).

| | A: Integration als Client der Leitstelle (gewählt) | B: Leitstelle als Teil von HA |
|---|---|---|
| Signierschlüssel | bleibt in der Leitstelle; HA kann nur anfragen | läge in HA; ein übernommenes HA signiert Aufträge an alle Hosts |
| Erreichbarkeit | Agenten erreichen nur die Leitstelle | jedes Netz bräuchte einen Weg zu HA |
| HA-Neustart (Updates!) | trennt nur die Integration; Agenten bleiben verbunden | trennt alle Tunnel |
| Freigabe neuer Hosts | an der Leitstelle; in HA nur, wo die Leitstelle es erlaubt (5c) | in HA |
| Aufwand | Endpunkt in der Leitstelle + Integration | Leitstelle in Python neu bauen |

## 3. Überblick

```
vordr-agent ──wss /v1/tunnel──▶ vordr-hub ◀──wss /v1/ha── HA-Integration "vordr" (in Home Assistant)
 (je Host)                      (Leitstelle,              Geräte "Vördr <node>", Entitäten,
                                 signiert)                Knöpfe -> Befehle
```

Beide Endpunkte laufen auf demselben Port (8443) mit demselben Zertifikat. Die Verbindung geht von HA aus; die
Leitstelle braucht keinen Weg zu HA. MQTT ist ganz entfallen (Abschnitt 8).

## 4. Vertrauen und Rechte

- Die Integration erzeugt beim Einrichten ein eigenes Ed25519-Schlüsselpaar (gespeichert im Config-Entry). Bis der
  Entry besteht, wartet der Schlüssel in der Speicherdatei `vordr.pending_keys` der Integration (Modus 0600), je
  Leitstelle und Name des Clients: Erneutes Einrichten nach Zeitablauf, geschlossenem Dialog oder Neustart nimmt
  denselben Schlüssel und setzt so die Anfrage fort, die die Leitstelle behält; der Entry übernimmt den Schlüssel, und
  die Datei vergisst diese Leitstelle
  ([ADR-0156](../docs/adr/0156-the-config-flow-keeps-its-key-until-the-client-is-set-up.md)).
- Freigegeben wird sie **nur an der Leitstelle**, nie durch einen anderen Client: `vordr-hub clients` zeigt wartende
  Clients,
  `vordr-hub approve-client <name> --fingerprint <fp>` gibt frei, `reject-client` / `forget-client` widerrufen.
  Der Config-Flow zeigt Fingerprint und Befehl und wartet, bis die Freigabe da ist.
- Ein freigegebener HA-Client darf:
  - den Zustand aller freigegebenen Hosts und die Liste wartender Hosts (Name, Fingerprint, OS) lesen,
  - Befehle aus der festen Liste anfragen (`check`, `install`, `install_security`, `install_app:<id>`, `reboot`,
    `agent_update`); die Leitstelle prüft sie wie heute gegen Verben und Richtlinie des Hosts und signiert erst dann,
  - mit Rolle `admin`: die signierte Agent-Release holen, um einen neuen Host einzurichten (`agent_download`, 5b),
  - mit Rolle `admin`, wo `hub.yaml` `ha_approves_hosts` setzt: einen Host freigeben, der auf Freigabe wartet, mit dem
    Fingerprint, den die Leitstelle für ihn führt (`approve_host`, 5c).
- Er darf nicht: Hosts ablehnen oder vergessen, einen ohne Erlaubnis der Leitstelle freigeben oder eine Ablehnung
  aufheben, Richtlinien ändern, selbst signieren, andere Clients sehen, den Betreiberschlüssel von der Leitstelle
  beziehen (er hat ihn von der Person, die ihn einrichtet, 5b).
- Mehrere HA-Clients sind möglich (z. B. ein Test-HA), jeder einzeln freigegeben und widerrufbar.

Änderung am Bedrohungsmodell (architecture.de.md, 5): Ein übernommenes HA kann wie bisher nur erlaubte Aufträge anstossen.
Der Weg über ein beliebiges MQTT-Konto entfällt; die Leitstelle braucht kein Konto am Broker mehr.

## 5. Protokoll `/v1/ha`

JSON-Nachrichten über WebSocket, wie der Agenten-Tunnel; `protocol: 1` im Hello.

| Richtung | Nachricht | Inhalt |
|---|---|---|
| Hub → HA | `challenge` | Nonce |
| HA → Hub | `hello` | `role: "ha"`, Name, öffentlicher Schlüssel, Signatur (Kontext `vordr/ha-hello/v1`), Integrationsversion, `protocol` |
| Hub → HA | `status` | `pending_approval` / `approved` / `rejected` mit Grund; erneut bei Änderung und nach einem Rollenwechsel; `features`: was die Leitstelle über das Grundprotokoll hinaus annimmt (`agent_download`; `approve_host` nur, wo 5c es erlaubt) |
| Hub → HA | `snapshot` | Leitstelle (Version, `protocol`, wartende Hosts) und alle Hosts als `HostView`; nach jeder Freigabe und jedem Verbindungsaufbau |
| Hub → HA | `host` / `host_removed` | ein `HostView` bei jeder Änderung / Host vergessen oder abgelehnt |
| Hub → HA | `hub` | Leitstellen-Zustand bei Änderung (wartende Hosts) |
| HA → Hub | `command` | `request_id`, `node`, `command`; `reason` für einen Lauf ausserhalb des Fensters (architecture.de.md 6.4) |
| Hub → HA | `command_result` | `request_id`, `code` (`ok` = Auftrag signiert und gesendet, wartet auf den laufenden Lauf, oder ist dieser Lauf schon, dann mit dessen `job_id`; sonst fester Grund wie `verb_unsupported_by_agent`, `not_allowed_by_host_policy`, `not_connected`), `job_id`; das Ergebnis des Laufs kommt später im `HostView` (`last_run`) |
| HA → Hub | `agent_download` | `request_id`, wahlweise `os` und `arch` eines neuen Hosts (5b); nur an eine Leitstelle, deren `status` `agent_download` nannte |
| Hub → HA | `agent_download_result` | `request_id`, `code`; mit `ok` das signierte Manifest, seine Signatur, der Schlüssel der Leitstelle und je Binary ein Download-Pfad (5b) |
| HA → Hub | `approve_host` | `request_id`, `node`, `fingerprint` eines Hosts, der auf Freigabe wartet (5c); nur an eine Leitstelle, deren `status` `approve_host` genannt hat |
| Hub → HA | `approve_host_result` | `request_id`, `code` (`ok` = freigegeben, sonst warum nicht, 5c) |
| beide | `heartbeat` | alle 30 s; 90 s Stille = Verbindung tot |

`HostView` ist die transportneutrale Fassung des früheren MQTT-Zustands: Gerät (Name, Modell, Agent-Version),
`online`, `problem` (Gründe, nächster Schritt), `pending`/`security`, `system` (installiert, neueste Version oder
`null`), Neustartbedarf (`true`/`false`/`null` mit Grund, Paketen, beantworteten Signalen), `last_report`, `last_run`
und `capabilities` (welche Knöpfe es geben darf: Verb angekündigt **und** von der Richtlinie erlaubt). Hier darf
`null` stehen – es ist unser Protokoll, die Integration macht daraus `None`.

Verträgt die Integration die `protocol`-Version der Leitstelle nicht, verbindet sie sich nicht und meldet das als
Reparaturhinweis („Leitstelle zu alt/zu neu“) statt halb zu funktionieren.

### 5a. Festgelegt im Bau von H1

- **Typen** in `internal/wire/ha.go` (Nachrichten) und `internal/wire/view.go` (`HostView`, `HubView`). Berechnet
  werden die Sichten an einer Stelle (`internal/hub/view.go`).
- **Abgewiesen vor dem Register** (`status` `rejected`, Verbindung zu): Hello mit falscher Signatur, `role` ≠ `ha`,
  Name nicht `^[a-z0-9][a-z0-9.-]{0,62}$` (`bad_hello`); andere `protocol`-Version (`protocol_mismatch`). Ein
  Agenten-Hello auf `/v1/ha` und ein HA-Hello auf `/v1/tunnel` scheitern beide als `bad_hello`.
- **Version der Integration** ([ADR-0195](../docs/adr/0195-the-hub-bounds-reports-inventories-and-the-home-assistant-hello.md)):
  leer oder eine Version, wie ein signiertes Manifest sie trägt (bis 64 Buchstaben, Ziffern, `.`, `+`, `-`, beginnend
  mit einem Buchstaben oder einer Ziffer). Sonst lässt die Leitstelle sie weg, und der Client verbindet sich; bei einem
  freigegebenen Client nennt das Ereignis `ha_hello_fields_dropped` sie mit ihrem Code und dem `fingerprint` des
  Schlüssels (nicht bei einem wartenden, der das Protokoll mit jeder neuen Verbindung verdoppeln könnte).
- **Im Register abgewiesen:** anderer Schlüssel unter bekanntem Namen (`key_mismatch`), abgelehnter Client
  (`rejected`), mehr als 5 wartende Clients (`too_many_pending`).
- **Wartender Client:** bekommt nur seinen `status` und Antworten auf Heartbeats. Ein `command` von ihm wird mit
  `client_not_approved` beantwortet. Entschieden wird bei jedem Befehl nach dem Register, wie es gerade ist (Zustand
  und Schlüssel), nicht nach dem Stand bei der Anmeldung.
- **Freigabe während der Verbindung:** `status` `approved`, danach `snapshot`. Ablehnen schickt `status`
  `rejected` und trennt; Vergessen trennt.
- **`command`:** `request_id` `^[A-Za-z0-9_-]{1,64}$`, `node` wie ein Host-Name, `command` höchstens 80 Zeichen.
  Sonst trennt die Leitstelle (Protokollverletzung, im Ereignisprotokoll), statt zu raten. Antwortcodes:
  `ok` (mit `job_id`), `client_not_approved`, `unknown_node`, `not_approved`, `bad_request`,
  `verb_unsupported_by_agent`, `not_allowed_by_host_policy`, `not_connected`, `send_failed`. Mit `reason`
  ([ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md)) zusätzlich `bad_reason` (leer, über 200 Byte,
  Steuerzeichen) und `outside_window_unsupported` (der Agent kennt es noch nicht); `bad_request`, wenn das Verb kein
  Fenster hat (`check`, `agent_update`). Ein ungültiger Grund trennt nicht, er ist eine Eingabe von Menschen.
  `outside_window`: ein Install ohne Grund bei geschlossenem Fenster für einen Agenten, der keine Gründe annimmt; ohne
  Backup und ohne Auftrag abgelehnt. Sonst gibt die Leitstelle einem Install ohne Grund ausserhalb des Fensters
  selbst einen mit ([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)).
  `rate_limited`: ein Install der Systempakete oder einer App, solange der Mindestabstand des Agenten seit seinem
  letzten Lauf läuft; ohne Backup und ohne Auftrag abgelehnt. `HostView.next_install` sagt, ab wann es wieder geht
  ([ADR-0110](../docs/adr/0110-the-hub-checks-the-minimum-interval-before-the-backup.md)).
- **Pushes:** bei jeder Änderung und spätestens alle 30 s (Offline-Frist und Alter ausstehender Updates laufen ohne
  Ereignis ab). Gesendet wird nur, was sich gegenüber dem zuletzt Gesendeten geändert hat; ein Host, der nicht mehr
  freigegeben ist, kommt als `host_removed`.
- **`HostView`:** `last_report` `null` heisst „noch nie gemeldet“. Ob es Paket-Entitäten gibt, ist dann offen
  (weder anlegen noch entfernen). Mit Meldung gibt es sie genau dann, wenn `system` gesetzt ist.
  `capabilities` bietet genau, was `Resolve` annimmt. Einzige Ausnahme: `install_security` hat keinen eigenen
  Knopf, wenn Sicherheit schon der Standardumfang der update-Entität ist. Ein Test prüft das für alle 96
  Kombinationen aus Verben und Richtlinien.

### 5b. Die Agent-Release für einen neuen Host

In der Leitstelle gebaut; die Integration sendet die Anfrage noch nicht
([ADR-0167](../docs/adr/0167-home-assistant-gets-one-time-downloads-of-the-signed-agent-release.md)). Gedacht ist sie
für das Einrichten eines Hosts aus Home Assistant: Home Assistant erreicht das neue Gerät einmal per SSH, holt dort den
Agenten und ruft `vordr-agent install` auf. Die Leitstelle hat mit SSH nichts zu tun und sieht die Zugangsdaten des
Geräts nie.

- **Anfrage:** `agent_download` mit `request_id` (wie bei `command`; eine ungültige beendet die Verbindung) und
  wahlweise `os` und `arch`, wie das Manifest die Plattform nennt (`linux`/`amd64`, `arm64`, `arm`;
  `windows`/`amd64`), beide oder keins. Keins: jedes Binary der Release. Nur an eine Leitstelle, deren `status`
  `agent_download` in `features` trägt; eine ältere beendet die Verbindung bei einer Nachricht, die sie nicht kennt.
- **Antwort** mit `ok` (andere Codes tragen nur `request_id` und `code`):

  ```json
  {"t": "agent_download_result", "request_id": "add-1", "code": "ok", "version": "1.4.0",
   "manifest": "<base64 von manifest.json>", "manifest_sig": "<base64 von manifest.sig>", "hub_key": "<base64>",
   "expires_at": "2030-01-01T12:15:00Z",
   "downloads": [{"file": "vordr-agent-linux-arm64",
                  "path": "/v1/agent-download/vordr-agent-linux-arm64?token=<hex>"}]}
  ```

  `manifest` und `manifest_sig` sind genau die Bytes, die der Betreiber signiert hat. `hub_key` ist der öffentliche
  Schlüssel der Leitstelle, der jeden Auftrag signiert, in der Form von `hub.public_key` in `agent.yaml` und von
  `--hub-key`. Jeder Pfad öffnet seine Datei einmal, bis `expires_at` (15 Minuten).
- **Codes:** `ok`, `client_not_approved` (und die Leitstelle beendet die Verbindung), `role_forbids` (nur Rolle
  `admin`), `bad_request` (nur eins von `os`/`arch`, oder nicht `[a-z0-9]{1,16}`), `no_release`,
  `no_binary_for_platform`, `release_incomplete` (ein Binary des Manifests fehlt auf der Leitstelle, hat eine andere
  Grösse oder andere Bytes als sein Hash), `too_many_downloads` (8 Pfade bestehen, oder 4 dieses Clients; ein Pfad
  zählt, bis er abläuft, benutzt oder nicht).
- **Was die Integration mit der Antwort tut**, bevor etwas den neuen Host erreicht:
  1. Sie prüft `manifest_sig` über `vordr/agent-manifest/v1`, ein Null-Byte und `manifest` mit dem Betreiberschlüssel
     (und dem Ersatzschlüssel), den die Person eingegeben hat. Nie mit einem Schlüssel der Leitstelle: Die sendet
     keinen. Ohne einen solchen Schlüssel keine Installation.
  2. Plattform, `sha256` und `size` liest sie nur aus dem geprüften Manifest; aus `downloads` nimmt sie nur den `path`
     dieser `file`.
  3. Auf dem neuen Host holt sie die Adresse der Leitstelle (Schema `https`, Host und Port ihrer `wss://`-Adresse),
     gefolgt von `path`, einmal, vergleicht Grösse und SHA-256, bevor das Binary läuft, und ruft dann
     `vordr-agent install --hub wss://<host>:<port>/v1/tunnel --hub-key <hub_key> --operator-key <Schlüssel der Person>`
     auf.
  4. Die Pfade hält sie aus Logs, Diagnose und Config-Entry heraus: Jeder trägt sein Token.
- **Download** `GET /v1/agent-download/<file>?token=<hex>`: das Binary als `application/octet-stream` mit seiner
  Länge. Jede Ablehnung hat einen festen Status und ihren Code als Inhalt und lässt das Token, wie es war:

  | Code | Status | Wann |
  |---|---|---|
  | `bad_method` | 405 | kein GET |
  | `unknown_token` | 403 | kein Token, ein ungültiges oder unbekanntes, oder eines, das vor über einer Stunde ablief |
  | `token_expired` | 410 | die 15 Minuten sind vorbei |
  | `token_used` | 409 | sein Download hat schon begonnen |
  | `wrong_file` | 404 | das Token öffnet eine andere Datei |
  | `client_revoked` | 403 | der Client, für den der Pfad ausgestellt wurde, ist nicht mehr mit diesem Schlüssel und Rolle `admin` freigegeben |
  | `release_changed` | 410 | die aktuelle Release nennt die Datei nicht mehr mit dem Hash, für den das Token ausgestellt wurde |
  | `binary_unreadable` | 500 | die Datei auf der Leitstelle ist nicht lesbar oder hat eine andere Grösse |
  | `binary_mismatch` | 500 | die Datei auf der Leitstelle hat andere Bytes als der Hash im Manifest |

  Bei `token_used` oder `release_changed` fragt Home Assistant neu; ein abgebrochener Download, oder einer, der
  abgeschnitten wurde, weil er langsamer als 64 KiB/s las, hat sein Token verbraucht.
- **Ereignisprotokoll:** `agent_download_issued`, `agent_downloaded`, `agent_download_refused`, mit `download_id`
  statt des Tokens (architecture.de.md 10.4).

### 5c. Einen wartenden Host freigeben

Aus, solange `hub.yaml` nicht `ha_approves_hosts: true` setzt; `check-config` nennt es in jedem Fall
([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)).

- **Merkmal:** Die Leitstelle nennt `approve_host` im `status` eines Clients, der mit seinem Schlüssel und der Rolle
  `admin` freigegeben ist, so wie das Client-Register es hat, wenn der Status hinausgeht: bei der Anmeldung, bei der
  Freigabe des Clients und erneut nach `vordr-hub client-role`, damit es während der Verbindung kommt oder geht. Die
  Sitzung wird eingetragen, bevor ihr Status der Anmeldung hinausgeht, und jeder dieser Status liest das Register unter
  der Sperre der Sitzung: Von zweien zugleich nennt der spätere den Zustand, wie er dann ist. Wie bei jedem Merkmal
  ([ADR-0067](../docs/adr/0067-hub-announces-features.md)) sendet ein Client die Nachricht nur, solange sein letzter
  Status sie genannt hat.
- **Anfrage:** `approve_host` mit `request_id` und `node` wie bei `command` (fehlerhaft: Protokollverletzung, die
  Verbindung endet) und `fingerprint`.
- **Entscheidung**, auf den Registern, wie sie in diesem Moment sind: Ein Client, der nicht (oder nicht mit diesem
  Schlüssel) freigegeben ist, bekommt `client_not_approved`, und die Verbindung endet wie bei `agent_download`; das
  kommt zuerst, damit ein wartender Client das Ereignisprotokoll nicht vollschreiben kann. Dann `ha_approval_off` (der
  Schalter), `role_forbids` (nicht `admin`), `bad_request` (der Fingerprint ist nicht 64 kleine Hex-Ziffern). Knoten
  und Schlüssel werden in einem Schritt unter der Sperre des Host-Registers entschieden: `unknown_node`, `not_waiting`
  (freigegeben oder abgelehnt: Eine Ablehnung an der Leitstelle wird nie aus Home Assistant aufgehoben),
  `fingerprint_mismatch` (nicht der Schlüssel, den die Leitstelle für den Knoten führt); `store_failed`, wenn das
  Register nicht geschrieben werden kann, und nichts ändert sich.
- **Mit `ok`** ist der Host freigegeben wie durch `vordr-hub approve`: Ein verbundener Agent bekommt seinen `status`,
  die Clients bekommen die neuen Sichten (der Host verlässt `waiting` und kommt als `host`).
- **Ereignisprotokoll:** `approve` mit dem Namen des Clients und `code`, so wie `vordr-hub approve` es ohne ihn
  protokolliert; `approve_refused` mit dem Client, seinem `code` und, bei `role_forbids`, der Rolle.
- Ablehnen und Vergessen von Hosts und alles zu Clients bleiben auf der Kommandozeile der Leitstelle.

## 6. Die Integration `custom_components/vordr`

- **Config-Flow:** Adresse (z. B. `wss://vordr-hub.example:8443/v1/ha`), optional eigenes CA-Zertifikat (PEM),
  Name des Clients. Erzeugt den Schlüssel, zeigt Fingerprint und Freigabe-Befehl, wartet als Fortschrittsschritt
  (höchstens eine Stunde). Prüft vorher selbst die Erreichbarkeit (die Stufen von `vordr-agent hub-check` bis zur
  Challenge; den Hub-Schlüssel kennt sie nicht, eine Stufe `hub_key` gibt es darum nicht) und nennt die scheiternde
  Stufe. Ein späterer Versuch für dieselbe Leitstelle und denselben Namen nimmt den behaltenen Schlüssel (Abschnitt 4).
  Eine Ablehnung nennt den Client und den Fingerprint dieses Home Assistant; `key_mismatch` nennt dazu
  `vordr-hub forget-client <name>` als Ausweg, für einen Namen, der keinem anderen Home Assistant gehört.
- **Verbindung:** ein Hintergrund-Task je Config-Entry, Backoff 1–30 s, Push statt Polling
  (`DataUpdateCoordinator` ohne Intervall, `async_set_updated_data`). Ohne Verbindung sind alle Entitäten nicht
  verfügbar.
- **Geräte:** Hub-Gerät „Vördr-Leitstelle“, je Host „Vördr <node>“ mit `via_device` auf das Hub-Gerät; „Vördr Home
  Assistant“ für die Updates von Home Assistant selbst (6c), ohne `via_device`, weil es die Leitstelle nicht braucht.
- **Entitäten** (gleiche Bedeutung wie architecture.de.md 8.1/8.2, jetzt nativ):

| Entität | Plattform | Neu gegenüber MQTT |
|---|---|---|
| `update.vordr_<id>_system` | `UpdateEntity` | `latest_version = None` → **unbekannt**; `INSTALL` nur mit Fähigkeit; `in_progress`, Fortschritt |
| `sensor.vordr_<id>_pending_updates`, `_security_updates` | `SensorEntity` | – |
| `sensor.vordr_<id>_last_report` | `SensorEntity` (timestamp) | – |
| `sensor.vordr_<id>_last_run` | `SensorEntity` (enum) | – |
| `binary_sensor.vordr_<id>_online`, `_problem`, `_reboot_required` | `BinarySensorEntity` | `None` → unbekannt ohne Template-Umweg. Ein PC, der angekündigt hat, dass er schläft (`power` in der Host-Sicht, Leitstelle ab 0.15.0), ist nicht online und trotzdem kein Problem; `power` zeigt die Integration noch nicht als eigenen Zustand |
| `button.vordr_<id>_check`, `_install_security`, `_reboot` | `ButtonEntity` | Befehl authentifiziert über die eigene Verbindung |
| `sensor.vordr_hub_*`, `binary_sensor.vordr_hub_online` | Hub-Gerät | – |

- **Entitäts-IDs bleiben** `*.vordr_<id>_<object>`, damit Dashboards, Automationen und Blueprints, die
  `update.vordr_*` nennen (etwa ein Auto-Update-Blueprint, der sie auslassen soll,
  [ADR-0028](../docs/adr/0028-site-decision.md)), weiter passen. Weg: die `entity_id` vor dem Hinzufügen
  als Vorschlag setzen, per Test gegen die HA-Version belegt (6a).
- **Entitäts-Kategorien** nach Rolle ([ADR-0202](../docs/adr/0202-entities-carry-a-category-by-their-role.md)):
  Was die Wartung eines Hosts ausmacht, hat keine (die Updates für Systempakete, pip/npm-Pakete und Container-Images,
  Update-Problem, Neustart nötig, letzter Lauf, die Zähler, die Knöpfe für Sicherheitsupdates, Neustart und Apps; die
  Zähler der Leitstelle). Wie Vördr einen Host oder die Leitstelle sieht, ist `diagnostic` („Verbunden“, „Letzte
  Meldung“, „Letztes Backup“; bei der Leitstelle „Leitstelle“, „Verbindung“, „Schwachstellendaten“). Wie Vördr selbst
  arbeitet, ist `config` (der Knopf „Jetzt prüfen“, das Update „Agent“, der Schalter „Home-Assistant-Updates
  pausieren“). Die Update-Entitäten setzen ihre Kategorie selbst, damit keine mit einer Fähigkeit wandert.
- **Reparaturhinweise** (Abschnitt 11): je Host mit Problem ein Hinweis mit Grund und nächstem Schritt; er
  verschwindet, sobald das Problem weg ist.
- **Diagnose-Download:** Verbindung, Versionen, Hostliste – ohne Schlüssel und ohne Hinweis auf den Standort, weil er
  an öffentliche Issues angehängt wird: Die Adresse der Leitstelle wird `hub.invalid`, Hostnamen und der Name dieses
  Home Assistant an der Leitstelle werden Platzhalter (`host-1`, `host-2` … in der Reihenfolge der Namen, `client`),
  ebenso die IDs der pip/npm-Orte (`location-1` …); Pfade, Image-Repositories, Log-Auszüge, Log-Dateien und freier
  Text (`outside_window`, `exempt`) werden geschwärzt, Feldnamen und Codes bleiben. Die Reparaturhinweise, die Home
  Assistant selbst beilegt, nennen die Hosts weiter in ihren IDs ([ADR-0189](../docs/adr/0189-the-diagnostics-download-names-no-site.md)).
- **Übersetzungen:** en und de für Config-Flow, Entitäten und Hinweise.
- **Abhängigkeiten:** was HA mitbringt (`aiohttp` für den WebSocket, `cryptography` für Ed25519), und `asyncssh` für
  das Installieren des Agenten über SSH (6d), als Bereich (`>=2.24.0,<3`), der die Version enthält, die HA für seine
  eigene Integration `sftp_storage` festlegt. Nur der Installationspunkt importiert es; alles andere lädt ohne.

### 6a. Festgelegt im Bau von H2

- **Entitäts-IDs** setzt die Integration ausdrücklich (`self.entity_id` vor dem Hinzufügen); HA 2026.10 übernimmt sie,
  solange sie frei sind. Belegt im Test (`test_entity_ids_as_mqtt_had_them`).
- **Unique IDs** `<node>:<key>` bzw. `hub:<key>`; Geräte `("vordr", "host:<node>")` unter dem Hub-Gerät
  (`via_device_id`, seit HA 2026.10; `via_device` und `async_get_device(identifiers=…)` sind dort veraltet).
- **Entitäten kommen und gehen mit der Sicht:** entschieden wird nur nach einem Snapshot (vollständige Sicht). Ohne
  Meldung (`last_report` null) bleiben Paket-Entitäten offen; Knöpfe folgen `capabilities`; Entfernen geht über das
  Entity-Register, damit auch Reste von vor einem Neustart verschwinden. Hosts, die die Leitstelle nicht mehr hat,
  verlieren ihr Gerät.
- **Zusätzliche Entität** `sensor.vordr_hub_connection` (Diagnose): `connecting`, `unreachable`, `pending_approval`,
  `approved`, `rejected` – der Zustand als Fakt, nicht aus Fehlern geraten.
- **Ereignisse für Automationen:** `vordr_problem` `{node, active, reason, reasons, check_failed_code, message}` bei
  neuem, geändertem oder beendetem Problem (nach einem HA-Neustart einmal für jedes noch bestehende);
  `vordr_run_finished` `{node, job_id, verb, scope, state, message}` (seit H5, siehe 6b).
- **Reparaturhinweise** `problem_<node>` je Host-Problem, `protocol_mismatch` und `client_rejected` für die Verbindung.
- **Neustart als Reparaturhinweis** `reboot_<node>` ab der ersten Meldung „Neustart nötig“ (nur ein klares Ja), bis der
  Host keinen mehr braucht ([ADR-0132](../docs/adr/0132-a-needed-reboot-is-a-repair-from-the-first-report.md)):
  - mit „Beheben“, wo die Leitstelle den Neustart anbietet (Fähigkeit `reboot`): Bestätigen, dann derselbe Befehl wie
    der Neustart-Knopf; beim Bestätigen erneut geprüft (Verbindung, Host, Bedarf, Fähigkeit), jede Absage mit eigenem
    Grund; sonst „von Hand neu starten“ (`reboot_required_manual`);
  - der Text sagt den Grund, wo die Host-Sicht hat, was er nennt
    ([ADR-0201](../docs/adr/0201-the-reboot-repair-names-its-reason-and-the-services-to-restart.md)): `_libraries`
    nennt die Dienste, die noch ersetzte Bibliotheken ausführen (`reboot.services`), und dass deren Neustart genügen
    würde, `_kernel` den laufenden und den neuesten Kernel, wo sie sich unterscheiden, `_windows_update` (nur von Hand),
    dass Windows Update wartet; jeder andere Fall behält den allgemeinen Text. Der Dialog nennt dasselbe, wie es der
    Host zuletzt gemeldet hat; ändern sich Grund oder Dienste, entsteht der Hinweis neu;
  - Warnung, ab `reboot_pending` der Leitstelle Fehler; besteht das Problem nur aus `reboot_pending`, entfällt dafür
    `problem_<node>` (Problem-Sensor und `vordr_problem` bleiben);
  - nach einem angenommenen „Beheben“ bleibt er weg, bis der Neustart-Lauf endet, höchstens 30 Minuten;
  - solange das Neustart-Fenster des Hosts zu ist (`HostView.next_reboot`), schickt „Beheben“ nichts: Der Dialog
    endet beim Öffnen und erneut beim Bestätigen mit der Uhrzeit, ab der der Neustart läuft, und mit `window_only`
    damit, dass auch ein Grund das Fenster nicht öffnet
    ([ADR-0187](../docs/adr/0187-the-agent-reports-its-reboot-window-and-home-assistant-says-when-a-reboot-can-run.md)).
- **Erneut installieren als Reparaturhinweis** `install_again_<node>`, solange die Leitstelle einen gescheiterten Install
  des Hosts festhält (`failed_install`, ADR-0124) und den Install anbietet (Fähigkeit `install`)
  ([ADR-0148](../docs/adr/0148-a-kept-failed-install-is-a-repair-that-installs-again.md)):
  - sein Dialog zum Beheben nennt Code und Detail des Laufs, aus dem Stand des Hosts beim Öffnen (ein behebbarer Hinweis
    trägt seinen Text im Dialog, nicht als eigene Beschreibung: hassfest erlaubt eines von beiden); „Beheben“ bestätigt
    und schickt dann denselben Befehl wie der Installieren-Knopf; beim Bestätigen erneut geprüft (Verbindung, Host,
    festgehaltener Fehlschlag, Fähigkeit), jede Absage mit eigenem Grund;
  - ein Lauf, der durchläuft, löscht den Fehlschlag in der Leitstelle, und der Hinweis verschwindet mit ihm; scheitert er
    wieder, bleibt beides;
  - besteht das Problem nur aus dem festgehaltenen Install, entfällt dafür `problem_<node>` (Problem-Sensor und
    `vordr_problem` bleiben); ohne angebotenen Install bleibt `problem_<node>` wie bisher.
- **Zu lange ausstehende Updates aus ihrem Reparaturhinweis installieren:** `problem_<node>` ist behebbar, mit dem Text
  `stale_install`, solange sein Problem allein `stale` ist, die Leitstelle `installable` über null schickt und ein Knopf
  sie nimmt: der Installieren-Knopf (Fähigkeit `install`) oder, wo er der einzige ist, der Knopf für Sicherheitsupdates
  (`install_security`) ([ADR-0203](../docs/adr/0203-the-stale-repair-installs-what-a-button-takes.md)):
  - sein Dialog zum Beheben nennt, wie viele der ausstehenden Updates der Knopf nimmt, aus dem Stand des Hosts beim
    Öffnen; „Beheben“ bestätigt und schickt dann den Befehl dieses Knopfs (`install` oder `install_security`), ohne
    Grund, wie der Knopf; beim Bestätigen erneut geprüft (Verbindung, Host, Problem, Knopf und Zahl), jede Absage mit
    eigenem Grund, und die Absagen der Leitstelle haben die Texte des Installieren-Knopfs;
  - ein Lauf, der sie installiert, beendet das Problem, und der Hinweis verschwindet mit ihm; Updates, die kein Knopf
    nimmt, halten ihn;
  - `next_step` des Problem-Sensors und `vordr_problem` behalten den Text von `stale`; ohne Zahl (eine ältere
    Leitstelle), mit einem weiteren Grund neben `stale` oder wo kein Knopf die Updates nimmt, behält der Hinweis seinen
    Text ohne Beheben.
- **Wartender Host als Reparaturhinweis** `waiting_<node>`, solange die Leitstelle den Host als wartend auf Freigabe
  führt ([ADR-0157](../docs/adr/0157-a-waiting-host-is-a-repair-that-names-the-approval-command.md)):
  - eine Warnung: Ein neuer Host ist eine Nachricht, kein Alarm;
  - der Text nennt den Knoten, das System, das der Host meldet, den Fingerprint, den die Leitstelle gesehen hat, und
    `vordr-hub approve <node> --fingerprint <fp>`, und verlangt zuerst den Vergleich mit `vordr-agent identity` auf dem
    Host selbst; weichen sie ab, nicht freigeben (`vordr-hub reject <node>`);
  - nicht behebbar, ausser der letzte `status` der Leitstelle nennt `approve_host` (5c,
    [ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)). Dann
    ist er behebbar, mit eigenem Text (`waiting_host_approve`: auf dem Host vergleichen, dann hier beheben oder den
    Befehl an der Leitstelle ausführen) und den Daten `kind: approve_host`. Die Hinweise folgen jedem `status`: Kommt
    oder geht das Merkmal oder endet die Verbindung, werden sie sofort neu angelegt, nicht erst mit der nächsten
    Änderung der Sicht der Leitstelle;
  - sein Beheben (`ApproveHostFlow` in `repairs.py`) fragt nach dem Fingerprint, den `vordr-agent identity` auf dem
    Host ausgegeben hat, ohne Leerzeichen und in Kleinbuchstaben; alles ausser 64 Hex-Ziffern ist ein Formularfehler,
    ebenso ein Fingerprint, der nicht der in der Warteliste der Leitstelle ist, dessen Text sagt, nicht freizugeben und
    wie an der Leitstelle abzulehnen ist; dann wird nichts gesendet. Beim Öffnen und vor dem Senden prüft er erneut:
    Leitstelle nicht verbunden, Host wartet nicht mehr, Merkmal weg beenden ihn je mit eigenem Grund. Ein passender
    Fingerprint geht als `approve_host` an die Leitstelle; jeder Code aus 5c und `no_answer` hat einen eigenen Text,
    jeder andere wird genannt, wie er ist;
  - er folgt der eigenen Sicht der Leitstelle (Snapshot und `hub`): weg, sobald der Host freigegeben, abgelehnt oder
    vergessen ist;
  - nur für einen Knoten, der ein Host-Name ist, und einen Fingerprint aus 64 Hex-Ziffern; das System ist das eigene
    Wort des Hosts und erscheint als eine Zeile Code, höchstens 64 Zeichen, ohne Backticks.
    `sensor.vordr_hub_pending_approvals` führt weiterhin jeden wartenden Host so, wie die Leitstelle ihn geschickt hat.
- **Knöpfe** heben eine Ablehnung der Leitstelle als Fehler ins UI; die Dialoge der Reparaturhinweise für Neustart
  und erneutes Installieren enden mit demselben Text. Ein Code, den die Integration kennt, sagt den Grund:
  `role_forbids`, `outside_window`, `rate_limited` mit der Uhrzeit aus `HostView.next_install`, die Backup-Prüfung mit
  dem, was fehlt. Jeder andere nennt seinen Code (`command_refused`). Ein Dialog sagt den Grund nur für die Codes, die
  die Leitstelle für seinen Befehl schicken kann, der Neustart nur für `role_forbids`. Die update-Entität hat `INSTALL`
  nur mit der Fähigkeit `install`.
- **Neustart-Fenster** ([ADR-0187](../docs/adr/0187-the-agent-reports-its-reboot-window-and-home-assistant-says-when-a-reboot-can-run.md)): Der Neustart-Knopf
  schickt nichts, solange `HostView.next_reboot` vor uns liegt, und meldet `reboot_window_closed` oder, bei einem Host
  mit `window_only`, `reboot_window_only`, mit der Uhrzeit in der lokalen Zeit von Home Assistant.
  `vordr.press_outside_window` hält einen Neustart nur mit `window_only` zurück. Der Knopf hat die Attribute
  `reboot_window` (`days`, `start`, `end`, `tz`), `window_only` und `next_reboot`. Ohne die Felder (eine ältere
  Leitstelle oder ein älterer Agent) bleibt alles wie bisher.
- **Bekannte Ausnutzung** ([ADR-0168](../docs/adr/0168-known-exploitation-comes-from-the-kev-catalog-fetched-whole-at-the-hub.md)):
  Die KEV-Felder jedes Funds (`known_exploited`, `kev_cves`, `kev_added`, `kev_ransomware`) gehen im Attribut
  `findings` von `_vulnerable_packages` und `_malicious_packages` durch; `_vulnerable_packages` hat die Zahl des Hosts
  als `known_exploited`, `sensor.vordr_hub_vulnerability_data` den Zustand des Katalogs als `kev`. Jedes fehlt, solange
  die Leitstelle es nicht schickt (kein Abschnitt `kev:` oder noch kein Katalog). Die Integration entscheidet daraus
  nichts: keine Reparatur, kein Ereignis, kein neuer Text.

### 6b. Sprachen (H5, Protokoll 2)

Vor H5 schickte die Leitstelle fertige deutsche Texte (`next_step`, `latest_version` „n ausstehend …“, die
Paketliste). Ein Reparaturhinweis mischte deshalb Sprachen: Rahmen in der Profilsprache, Grund in der Systemsprache,
nächster Schritt deutsch. Seit H5 schickt die Leitstelle nur Codes und Zahlen, und die Integration schreibt jeden Text
aus ihren Übersetzungsdateien ([ADR-0027](../docs/adr/0027-protocol-2-codes-and-translations.md)):

- **Protokoll 2:** `problem` ohne `next_step`; `last_run` ohne `next_step`; `system` mit `packages` (höchstens 30,
  Reihenfolge des Berichts) statt `latest_version`/`release_summary`; `hub.limits` mit den Schwellen
  (`offline_after_minutes`, `stale_after_days`, `reboot_pending_after_days`, `no_report_after_hours`). Eine
  Integration mit Protokoll 1 lehnt die Leitstelle ab (Reparaturhinweis `protocol_mismatch`).
- **Was das Frontend übersetzt** (Profilsprache): Reparaturhinweise – je Problemschlüssel ein eigener Text
  `problem_<schlüssel>` mit den Schwellen als Platzhalter –, Zustände und die Attributwerte von `_problem`
  (`reason`, `next_step`, `check_failed_code`; alle drei Codes).
- **Was das Backend schreibt** (Systemsprache, wie die Namen): `latest_version` und `release_summary` der
  update-Entität, der Gerätename der Leitstelle. Ihre Texte stehen im Abschnitt `common` der Übersetzungsdateien,
  den Home Assistant für eigene Texte der Integration vorsieht
  ([ADR-0145](../docs/adr/0145-the-integrations-own-texts-live-in-the-common-section.md)).
- **Problemschlüssel:** der erste Grund; bei `check_failed` und gescheiterten Läufen der Ergebniscode, weil der
  nächste Schritt an ihm hängt; ein unbekannter Code wird `unknown`. Der Schlüssel nennt nur einen Weg, den es auf dem
  Host gibt:
  - `reboot_pending` und `ok_reboot_required` werden zu `…_manual`, wo die Leitstelle keinen Neustart anbietet
    (Fähigkeit `reboot`);
  - `stale` bleibt, wo der Installieren-Knopf einen Teil der ausstehenden Updates nimmt (`installable` über null, oder
    keine Zahl von der Leitstelle); sonst wird es `stale_policy`, wo der Host Installationen über Vördr annimmt, seine
    Richtlinie aber keines dieser Updates, und `stale_manual`, wo er keine annimmt
    ([ADR-0197](../docs/adr/0197-the-stale-repair-names-only-a-way-that-exists.md)); vor beiden `stale_kept_back`, wo
    der Host jedes ausstehende Update zurückhält (`kept_back` der Leitstelle gleich `pending`): ein Release, das ein Gerät
    nur in einem anderen Zweig anbietet, oder der Installationsmodus `upgrade` von apt; sein nächster Schritt ist, den
    Update-Zweig des Geräts zu wechseln, `install.mode: full-upgrade` zu setzen oder von Hand zu installieren
    ([ADR-0204](../docs/adr/0204-the-firewall-reports-what-its-own-updater-offers.md));
  - die Reparatur folgt einem geänderten Schlüssel auch, wenn das Problem dasselbe bleibt; `vordr_problem` feuert
    dafür nicht.
- **Ereignisse** tragen `message` in jeder mitgelieferten Sprache, damit eine Automation die Sprache ihres
  Empfängers wählt, unabhängig von der Systemsprache.
- `tests/test_texts.py` hält die Dateien zusammen: jede Sprache hat jeden Text, jeder Hinweistext enthält den
  nächsten Schritt seines Schlüssels, ein Code heisst überall gleich. Er prüft auch, was hassfest, die Prüfung von
  Home Assistant selbst, verlangt: nur Abschnitte, die Home Assistant kennt, und kein Text, der wie ein HTML-Tag
  aussieht.

### 6c. Updates von Home Assistant selbst unter den Regeln von Vördr

Home Assistant bietet Update-Entitäten für Core, OS, Supervisor, Apps, HACS, ESPHome und Geräte-Firmware an und
installiert sie ohne eigene Regeln. Die Integration plant sie unter den Regeln von Vördr ([ADR-0158](../docs/adr/0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md)) und
installiert, was bereit ist, sobald ihre Optionen das einschalten ([ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)).

- **Regeln in den Optionen der Integration** (Options-Flow als Menü, Punkt „Home-Assistant-Updates“, Schlüssel
  `ha_updates`): erlaubte Arten (`core`, `os`, `supervisor`, `app`, `hacs`, `esphome`, `firmware`, `radio`, `other`)
  und einzelne Update-Entitäten; ein Fenster (Tage, Beginn, Ende in der Zeitzone von Home Assistant, über Mitternacht
  wie das `install.window` des Agenten); ein Mindestalter in Tagen (Standard 7, 1–90; Geräte-Firmware und Funkadapter
  mindestens 14, siehe unten); „Automatisch installieren“
  (`auto_install`, standardmässig aus; Optionen, die ohne es gespeichert sind, gelten als aus). Standardmässig
  geschlossen: nichts gewählt, nichts erlaubt, nichts installiert. Mit Text abgelehnt: etwas erlaubt ohne Tag, Beginn gleich Ende, ein Mindestalter, das
  keine ganze Zahl ist; unbekannte Auswahlen, andere Domänen und die Entitäten von Vördr selbst lehnt schon das
  Formular ab. Die Leitstelle hat nichts zu sagen: kein Feld, keine Nachricht.
- **Arten aus den Registern,** nie aus Entitäts-IDs: Core, OS und Supervisor an ihrem Gerät der Supervisor-Integration,
  deren übrige Geräte sind Apps; HACS und ESPHome an ihrer Integration; die Firmware der Funkadapter an deren
  Integration (`radio`, eine eigene Art); andere Firmware an der Geräteklasse `firmware`; der Rest ist `other`.
- **Der Plan** (`custom_components/vordr/ha_updates.py`, reine Funktionen): je Update-Entität mit offener oder
  übersprungener Version ein Code, in dieser Reihenfolge: `in_progress`, `self_updating` (`auto_update` der Entität:
  gemeldet, nie installiert), `no_install` (ohne Installationsfunktion), `not_allowed`, `major`
  ([ADR-0160](../docs/adr/0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md)), `last_failed`
  (die Installation dieser Version durch Vördr ist gescheitert und gesperrt, [ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)), `too_new`
  ([ADR-0159](../docs/adr/0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md)),
  `outside_window`, `ready`; `skipped` für eine in Home Assistant übersprungene Version. `too_new` und `outside_window`
  tragen den frühesten Zeitpunkt, an dem es bereit wäre. Die Update-Entitäten von Vördr selbst fehlen, erkannt an der
  Plattform `vordr` im Register. Nicht verfügbare oder unbekannte Entitäten stehen nicht in der Liste.
- **Erste Sicht:** Home Assistant nennt kein Erscheinungsdatum, also zählt das Alter ab dem Zeitpunkt, an dem die
  Integration die Version zum ersten Mal gesehen hat, je Update-Entität in `.storage/vordr.ha_updates.<entry_id>`
  (geschrieben 10 s nach einer Änderung und beim Entladen, entfernt mit dem Eintrag). Eine neuere Version beginnt ihr
  eigenes Alter; mit einer installierten Version oder einer aus dem Register verschwundenen Entität fällt die erste
  Sicht weg; eine nicht verfügbare Entität behält sie.
- **Linie** ([ADR-0179](../docs/adr/0179-a-newer-release-of-a-version-line-inherits-the-lines-age-with-two-days-of-its-own.md)):
  Haupt- und Nebennummer einer Version aus Zahlen mit Punkten nach einem optionalen `v` (`v1.4.2` → `1.4`, Core
  `2026.11.1` → `2026.11`); Firmware-Nummern, Commits, Vorabversionen und Build-Zusätze haben keine. Der Store hält je
  Entität zusätzlich, wann jede Linie, die sie meldet, installiert oder angeboten, zum ersten Mal gesehen wurde
  (`lines`); eine Linie bleibt, solange die Entität eine Version davon meldet, auch nach der Installation. Eine Version
  einer Linie, die vor ihr gesehen wurde, zählt ihr Alter ab der ersten Sicht der Linie, muss aber selbst 2 Tage alt
  sein (nie mehr als das Mindestalter); die erste Version einer Linie, eine neue Linie und eine Version ohne Linie
  warten ihr volles eigenes Alter.
- **Mindestalter je Art** ([ADR-0180](../docs/adr/0180-device-firmware-and-the-radio-adapter-wait-at-least-14-days.md)):
  `firmware` und `radio` warten mindestens 14 Tage, oder das Mindestalter der Optionen, wenn es länger ist; eine feste
  Regel, keine Option, die das Formular in seiner Beschreibung nennt.
- **Wann gerechnet wird:** bei jeder Zustandsänderung einer Update-Entität, beim Speichern der Optionen (ohne
  Neuladen) und einmal pro Minute, weil sich Fenster öffnen und Versionen alt werden, ohne dass sich etwas ändert.
  Unabhängig von der Verbindung zur Leitstelle.
- **Sensor** `sensor.vordr_ha_updates_ready` auf einem eigenen Gerät „Vördr Home Assistant“ (ohne `via_device`, nicht
  von Hand entfernbar, ohne Leitstelle verfügbar): Zustand = Anzahl `ready`; Attribute: je Code eine Anzahl (ihre Namen
  sind die Texte der Codes), `min_age_days`, `min_age_days_firmware` und `updates`, je Entität `entity_id`, `kind`,
  `code`, `installed_version`, `latest_version`, `first_seen`, `line_first_seen` (wo die Version das Alter ihrer Linie
  erbt), `earliest` (Zeiten in der Zeitzone von Home Assistant). `updates` bleibt aus dem Recorder
  ([ADR-0101](../docs/adr/0101-package-lists-stay-out-of-the-recorder.md)).
- **Installieren** (`custom_components/vordr/ha_updates_installer.py`): nur, solange „Automatisch installieren“ an ist,
  der Schalter `switch.vordr_ha_updates_paused` „Home-Assistant-Updates pausieren“ aus ist (über Neustarts behalten)
  und kein anderer aktivierter Vördr-Eintrag das Installieren an hat; einen zweiten lehnen die Optionen ab
  (`auto_install_other_entry`) ([ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)). Das Attribut `installer` des Sensors „Letzter Lauf“ sagt, was gilt:
  `off`, `paused`, `other_entry`, `idle`, `running`.
- **Eines nach dem anderen, das System zuletzt** ([ADR-0164](../docs/adr/0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)): Ein Lauf beginnt nur, wenn keiner läuft, überhaupt
  keine Update-Entität `in_progress` meldet und der Backup-Manager von Home Assistant `idle` ist. Reihenfolge: alle
  anderen Arten nach Entitäts-ID, dann `supervisor`, `core`, `os`; ein System-Update nur, wenn sonst nichts `ready`
  ist, und danach beginnt in dieser Öffnung des Fensters nichts mehr. Die Entitäten von Vördr selbst nie. Ein Lauf:

| Schritt | Was geschieht | Endet mit |
|---|---|---|
| Backup ([ADR-0163](../docs/adr/0163-a-backup-before-every-automatic-home-assistant-update.md)) | eine Entität mit der Fähigkeit `BACKUP` sichert sich selbst (`backup: true` unten); Geräte-Firmware und Funkadapter ohne sie brauchen keines (`exempt`, [ADR-0181](../docs/adr/0181-device-firmware-and-the-radio-adapter-install-without-a-backup-of-home-assistant.md)); für die übrigen das letzte erfolgreiche automatische Backup von Home Assistant (Sensor der Backup-Integration, gefunden über Plattform und Unique-ID) höchstens 26 h alt, sonst `backup.create_automatic`, und der Sensor muss das neue innert 5 min zeigen | `backup_failed` mit `backup_unavailable`, `failed` oder `timeout` |
| Nochmals prüfen | der Plan neu gerechnet aus den Zuständen, wie sie jetzt sind: Installieren an, nicht pausiert, kein anderer Eintrag, noch `ready` für dieselbe Version, nichts beschäftigt | `skipped_paused`; `skipped_not_ready` mit `target_changed`, `gone`, `auto_install_off`, `other_entry`, `busy` oder dem Code des Plans |
| Festhalten | der Lauf in `.storage/vordr.ha_updates_runs.<entry_id>` geschrieben | – |
| Installieren | `update.install`, mit `backup: true` bei `BACKUP`, mit `version` (der geprüften) bei `SPECIFIC_VERSION`; seine Fehler nur hier gefangen | `failed`; `timeout` nach 2 h |
| Prüfen | nicht mehr in Arbeit und meldet die Zielversion (oder ist aktuell mit einer anderen als der, von der es ausging) | `ok`; nach 30 min, Geräte-Firmware und Funkadapter nach 2 h ([ADR-0183](../docs/adr/0183-device-firmware-and-the-radio-adapter-get-two-hours-to-report-their-version.md)), `timeout`, solange noch in Arbeit, sonst `not_reached` |

- **Neustart:** Beim Start wird vor allem anderen ein festgehaltener Lauf aus dem Zustand der Entität geprüft, sobald
  Home Assistant gestartet ist (`restarted: true`); bis er entschieden ist, beginnt nichts anderes. Ein Eintrag, der
  entladen wird, lässt einen laufenden Lauf weiterlaufen (eine Installation kann halb fertig sein); der nächste Start
  entscheidet ihn aus dem Store.
- **Nach einem gescheiterten Backup:** 24 h kein neues Backup und keine Installation einer Entität, die das Backup von
  Home Assistant braucht (ohne `BACKUP`, nicht Geräte-Firmware oder Funkadapter), bis Home Assistant ein neueres
  automatisches Backup zeigt; bis dahin bleibt der Reparaturhinweis `ha_updates_backup_failed`.
- **Bericht:** das Ereignis `vordr_ha_update_finished` mit `entity_id`, `kind`, `from_version`, `to_version`,
  `installed_version`, `result`, `reason`, `backup` (`entity`, `existing`, `created`, `exempt`), `started`, `finished`,
  `restarted`. Der Sensor `sensor.vordr_ha_updates_last_run` auf dem Gerät „Vördr Home Assistant“: die Phase, solange
  ein Lauf geht (`backing_up`, `installing`, `verifying`), danach sein Ergebnis; die Felder des Laufs als Attribute,
  `installer` und `queue` (die bereiten Updates in der Reihenfolge, in der sie drankämen, aus dem Recorder
  ausgenommen); über Neustarts behalten.
- **Fehlschläge** ([ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)): `failed`, `not_reached` und `timeout` werden je Entität im selben Store festgehalten;
  die Version ist gesperrt (`last_failed`); ein behebbarer Reparaturhinweis `ha_update_failed_<object id>` mit einem
  Text je Ergebnis. Weg, sobald die Entität die Version meldet oder aktuell ist, sobald eine Installation von ihr `ok`
  endet, oder sobald sie aus dem Register verschwindet.
- **Die Behebung installiert sofort** ([ADR-0182](../docs/adr/0182-the-repairs-fix-installs-a-failed-home-assistant-update-at-once.md)):
  zuerst geprüft, startet sie den Lauf der Entität jetzt, ausserhalb des Fensters, aber unter allen anderen Regeln, mit
  dem Backup und der Prüfung direkt vor dem Handeln; oder sie bricht mit einem festen Code ab und stellt nichts in eine
  Warteschlange: `busy` (eine andere Installation läuft, oder der Backup-Manager arbeitet), `paused`,
  `auto_install_off`, `other_entry`, `not_ready` (der Plan lässt sie abgesehen vom Fenster nicht durch),
  `backup_held`, `no_longer_needed`, `entry_not_loaded`. Der Reparaturhinweis verschwindet, solange der Lauf geht; ein
  festgehaltenes Ergebnis bringt ihn zurück, ebenso ein Lauf, der nicht zum Installieren kommt (die Version ist wieder
  gesperrt).
- **Noch nicht:** zuerst eine Entität je Modell (eine Kanarie über mehrere Geräte desselben Modells), ein Neustart von
  Home Assistant nach HACS-Updates, Läufe an die Leitstelle melden.

### 6d. Den Agenten über SSH auf einem neuen Linux-Host installieren

Home Assistant erreicht einen neuen Host einmal über SSH, lässt ihn das signierte Agenten-Release holen und prüfen und
führt dort `vordr-agent install` aus. Vom SSH-Zugang behält es nichts; danach wartet der Host wie jeder neue Host auf
die Freigabe an der Leitstelle
([ADR-0161](../docs/adr/0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md), mit dem
Bedrohungsvergleich), ausser die Leitstelle lässt dieses Home Assistant Hosts freigeben: Dann gibt der Flow ihn frei
([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)). Nur Linux
mit systemd und apt.

- **Betreiberschlüssel**, ein Punkt des Options-Menüs (Schlüssel `operator_keys`): der Betreiberschlüssel und ein
  optionaler Ersatz, Base64-Ed25519, wie `vordr-release public-key` sie ausgibt. Geprüft (32 Bytes, der Ersatz ungleich
  dem Schlüssel und nur mit ihm), gespeichert wie jede Option: Sie sind öffentlich. Leer entfernt sie. Nie von der
  Leitstelle übernommen.
- **Der Knopf „Host hinzufügen“** auf der Seite der Integration startet die Installation
  ([ADR-0185](../docs/adr/0185-the-agent-install-starts-from-an-add-host-button-and-stores-no-subentry.md)): Der
  Config-Flow nennt den Subentry-Typ `host` (`async_get_supported_subentry_types`), und der Knopf öffnet dessen
  Subentry-Flow (`AddHostFlow` in `config_flow.py`), den nur Administratoren starten können. Jeder Eintrag bietet ihn
  an. Der Flow speichert nichts, keine Option und keinen Subentry: Das Register der Leitstelle bleibt die eine Liste der
  Hosts. Er endet mit einem Abbruch, dessen Code in `config_subentries.host.abort` einen Text hat, auch beim Erfolg;
  was die Person korrigieren kann (Host, Port, Benutzer, Knoten, die Anmeldung, sudo), zeigt das Formular als Fehler
  (`config_subentries.host.error`). Das Options-Menü bietet keine Installation an.
- **Schritte** (in Klammern die Schritt-IDs des Subentry-Flows):

| Schritt | Was er tut | Endet mit |
|---|---|---|
| Start (`user`) | die SSH-Bibliothek installiert, Eintrag geladen, Betreiberschlüssel gespeichert, Leitstelle verbunden, ihr Status nennt `agent_download`; jedes geprüft, bevor das Formular erscheint. Das Formular nennt die Rolle, die die Leitstelle verlangt (`vordr-hub client-role <client> --role admin`): Der Status der Leitstelle sagt einem Client seine Rolle nicht | `install_needs_ssh_library`, `entry_not_loaded`, `install_needs_operator_key` (nennt Konfigurieren → Betreiberschlüssel), `hub_not_connected`, `hub_without_download` |
| Ziel (`user`) | Host, SSH-Port, Benutzer (`root`), Knoten (leer: der eigene Name des Hosts) | Formularfehler; `ssh_unreachable`, `ssh_timeout`, `ssh_failed` |
| Host-Schlüssel (`host_key`) | ohne Anmeldung geholt (`asyncssh.get_server_host_key`), gefragt nach ED25519, dann ECDSA, dann RSA mit SHA-2-Signaturen, nie `ssh-rsa` mit SHA-1-Signaturen ([ADR-0186](../docs/adr/0186-the-ssh-install-asks-for-the-ed25519-host-key-first-and-never-for-sha-1-signatures.md)); gezeigt als Algorithmus und SHA256-Fingerprint mit der Datei des Hosts zum Vergleich (`ssh-keygen -lf /etc/ssh/ssh_host_<typ>_key.pub`); die Anmeldung fragt nach denselben Typen und nimmt nur den bestätigten Schlüssel an; ein Menü: gleich (`login`), oder abbrechen (`host_key_rejected`) | `host_key_rejected` |
| Anmeldung (`login`) | Passwort oder eingefügter privater Schlüssel mit Passphrase, Passwort für sudo (leer: das der Anmeldung); die Anmeldung nimmt nur den bestätigten Schlüssel an, in keine `known_hosts`-Datei wird geschrieben | Formularfehler `ssh_auth_failed`, `ssh_key_invalid`, `sudo_failed`, `sudo_needs_password`; `ssh_host_key_changed` |
| Erkundung | als Benutzer, Fakten als `key=value`: `uname -s`, `uname -m`, `id -u`, systemd, `apt-get`, `curl`/`wget`, `sha256sum`, `base64`, `mktemp`, `timeout`, `systemd-run`, `sudo`; dann `sudo -k -n true`, sonst `sudo -k -S` mit dem Passwort auf stdin | `not_linux`, `unknown_arch`, `no_systemd`, `no_apt`, `no_download_tool`, `missing_tools`, `no_sudo`, `probe_failed` |
| Release | `agent_download` für `linux/<arch>` (`x86_64` → `amd64`, `aarch64` → `arm64`, `armv7l`/`armv8l` → `arm`); die Signatur des Betreibers über `vordr/agent-manifest/v1\0` + Manifest mit den gespeicherten Schlüsseln; das Manifest so streng gelesen wie `VerifyManifest` des Agenten; Datei, SHA-256 und Grösse nur daraus; der Download-Pfad muss genau diese Datei öffnen; nicht älter als das neuste Release, das eine Installation dieses Eintrags genommen hat (gehalten in `.storage/vordr.agent_release.<entry_id>`, verglichen wie `NewerRelease` des Agenten) | die Codes der Leitstelle (`role_forbids` mit `vordr-hub client-role <client> --role admin`, `no_release`, `too_many_downloads`, …), `hub_refused`, `manifest_signature_invalid`, `manifest_invalid`, `no_binary_for_platform`, `release_too_old`, `download_invalid` |
| Installation (`install`, Fortschritt) | als root, als eigener vorübergehender Dienst (`systemd-run --pipe --wait --collect`), ausserhalb der Anmeldesitzung; ein Skript, ohne `${…}` und `$$` in ihm und seinen Werten, da systemd sie in der Befehlszeile eines Dienstes ersetzt; ein privater temporärer Ordner (`mktemp -d`, umask 077, abgelehnt, wenn daraus nichts laufen darf); Download über HTTPS von Host und Port der Integration, mit der CA der Leitstelle, falls sie eine hat, die URL aus einer Datei in diesem Ordner gelesen (`curl -K`, `wget -i`), begrenzt auf die signierte Grösse (`curl --max-filesize`, `ulimit -f` für beide) und in der Zeit (`curl --max-time 900`, `timeout 900 wget`); beide geben eine Verbindung nach 20 s auf (`--connect-timeout`, wget mit `--tries=1`); curl ohne zustande gekommene Verbindung (`%{num_connects}` ist 0) endet mit `download_unreachable`, jeder andere Fehler und jeder Fehler von wget (sein Exit 4 unterscheidet eine abgewiesene Verbindung nicht von einem gescheiterten TLS-Handshake) mit `download_failed`; Grösse und SHA-256 verglichen, bevor etwas läuft; die CA der Leitstelle nach `/etc/vordr/hub-ca.pem` (verglichen, wenn sie schon da ist, nach einer Ablehnung wieder entfernt); `vordr-agent install --hub wss://<host>:<port>/v1/tunnel --hub-key <aus der Antwort> --operator-key … [--backup-operator-key] [--node] [--ca-file]`; der Ordner in jedem Fall per trap entfernt | `size_mismatch`, `hash_mismatch`, `download_failed`, `download_unreachable`, `download_refused`, `download_too_large`, `ca_differs`, `ca_failed`, `temp_noexec`, `temp_failed`, `bad_input`; die Codes des Agenten als `agent_<code>`, andere als `agent_refused`/`agent_failed`; `agent_no_result`, `install_no_result`, `ssh_lost`, `ssh_timeout` |
| Freigabe (`install`, Fortschritt) | nur nach `agent_installed` und solange der Status der Leitstelle `approve_host` nennt (5c), und wo die Person den Knoten genannt hat, nur wenn der Agent diesen Knoten meldet; die SSH-Verbindung und alles Geheime sind dann schon weg. Wartet bis zu 60 s und schaut jede Sekunde in die Sicht der Leitstelle, bis deren Warteliste den Knoten mit genau dem Fingerprint hat, den die Installation auf dem Host gelesen hat, dann sendet es `approve_host`. Dieser Fingerprint kam über die Verbindung, deren Host-Schlüssel die Person bestätigt hat: Der Vergleich ist durch den Ablauf gemacht | `agent_installed_approved`; `agent_installed_not_approved` mit dem Code der Leitstelle; ohne das Merkmal, für einen anderen als den genannten Knoten oder wenn der Host nicht rechtzeitig erscheint, `agent_installed` |
| Ende (`result`) | die letzte Zeile des Agenten `result=installed node=<n> fingerprint=<hex>`; ein Abbruch, auch beim Erfolg: Kein Subentry wird gespeichert | `agent_installed` mit Knoten, Fingerprint und Version; der Reparaturhinweis `waiting_<node>` nennt dann den Befehl zur Freigabe, und nach der Freigabe ist der Host ein Gerät des Eintrags; `agent_installed_approved`: Der Host ist freigegeben und erscheint in Kürze als Gerät |

- **Was vom Host gelesen wird:** nur `key=value`-Felder: die Zeilen der Erkundung, die eigene letzte Zeile des Skripts
  (`step=<step> code=<code>`) und nach `step=agent exit=<n>` die letzte Zeile des Agenten. Prosa wird nie gelesen;
  eine Zeile, die nicht nur aus Feldern besteht, gilt als kein Ergebnis. Von jedem Befehl werden höchstens 64 KiB von
  stdout gelesen und stderr gar nicht; ein Host, der mehr ausgibt, beendet den Flow mit `host_output_too_long`.
- **Zugangsdaten und Tokens** leben nur im Objekt des Flows (`AgentInstall`): Passwort und Schlüssel der Anmeldung bis
  zur Anmeldung, das Passwort für sudo, bis der Installationsbefehl gesendet ist, die Verbindung, bis die Installation
  endet. asyncssh bekommt Passwort und Schlüssel einmal, wenn es danach fragt; die offene Verbindung behält keines von
  beiden. Das Passwort für sudo und der Download-Pfad (er trägt das Einmal-Token) gehen über stdin, nie auf eine
  Befehlszeile; auf dem Host liest das Download-Werkzeug den Pfad aus einer Datei im privaten Ordner. Nichts davon
  steht im Config-Eintrag, in seinen Optionen, im Speicher von Home Assistant, in den Logs (auch nicht im Debug-Log von
  asyncssh), in der Diagnose, als Vorgabe in einem Formular oder in einer Exception. Der Flow verwirft es, wie immer er
  endet. Wird der Dialog während der Anmeldung geschlossen, schliesst sich die Verbindung sofort und der wartende
  Schritt endet mit `install_cancelled`: Die Leitstelle wird nicht gefragt, und auf dem Host läuft nichts. Wird er
  während der Installation geschlossen, bricht die Aufgabe ab und schliesst die Verbindung; der Host macht von sich
  aus fertig (die Installation läuft ausserhalb der Anmeldesitzung, auch wo logind die Prozesse einer Sitzung mit
  `KillUserProcesses=yes` beendet) und entfernt seinen temporären Ordner.
- **Grenze:** `custom_components/vordr/ssh.py` ist die einzige Stelle, die SSH spricht; sie bildet jeden Fehler nach
  seinem Typ auf einen Code ab, liest nichts aus `~/.ssh`, verwendet keinen SSH-Agenten und keine Standardschlüssel.
  `agent_install.py` enthält die reinen Teile (Prüfungen, Skripte, Lesen), `agent_installer.py` die Schritte und was
  sie halten, `config_flow.py` den Subentry-Flow von „Host hinzufügen“.
- **Der Client** behält die `features` des letzten Status der Leitstelle und sendet `agent_download` und
  `approve_host` nur an eine Leitstelle, die sie genannt hat ([ADR-0067](../docs/adr/0067-hub-announces-features.md));
  die Antwort geht an den, der gefragt hat, nie an den Koordinator oder eine Entität.

### 6e. KI-Erklärungen gescheiterter Läufe (freiwillig)

Scheitert ein Lauf, kann eine AI-Task-Entität von Home Assistant, die die Person gewählt hat, ihn erklären: ein kurzer
Text, eine wahrscheinliche Ursache und nächste Schritte aus festen Listen und ein Zitat aus dem Log des Laufs als
Beleg. Nur ein Rat: es ändert nichts, was Vördr entscheidet, und die Leitstelle weiss nichts davon
([ADR-0166](../docs/adr/0166-optional-ai-explanations-of-failed-runs-go-through-home-assistants-ai-task-and-only-advise.md)).
`custom_components/vordr/ai_explain.py` enthält die reinen Teile (Optionen, Ersetzungen, Fakten, die Prüfung der
Antwort) und `Explainer`, der fragt; der Koordinator gibt ihm jeden beendeten Lauf.

- **Punkt „KI-Erklärungen“** des Optionsmenüs (Schlüssel `ai_explanations`): `entity_id`, eine `ai_task`-Entität,
  und `data_ack`, die Bestätigung, dass Paketnamen und Logauszüge gescheiterter Läufe an ihren Anbieter gehen. Leer:
  aus. Das Formular lehnt ab: `ai_entity_invalid` (keine `ai_task`-Entität), `ai_data_ack_missing`,
  `ai_entity_missing` (nicht da oder nicht verfügbar) und `ai_entity_unsupported` (kann keine Daten erzeugen, oder die
  Aktion von AI Task fehlt). Die Entität leeren entfernt die Optionen des Punkts; die der anderen Punkte bleiben.
- **Welche Läufe:** die Ergebniscodes in `EXPLAINED`, Fehlschläge, deren Log die Ursache trägt. Nie `ok`, eine
  Ablehnung (`rejected_*`), eine Entscheidung der Regeln von Vördr (`precheck_version_flagged`,
  `precheck_version_too_new`, `precheck_vetting_unavailable`, `precheck_not_from_registry`) oder ein
  Sicherheitssignal (`precheck_hash_mismatch`, `failed_target_tls`, `failed_target_hostkey`). Ein Test hält jeden Code
  in einer der beiden Mengen.
- **Schritte:**

| Schritt | Was geschieht | Code, wenn nicht |
|---|---|---|
| Optionen | eine Entität gewählt und bestätigt; ohne sie ist es aus: nichts wird gesendet, kein Ereignis, kein Attribut, und der Lauf gilt als genommen, damit späteres Einschalten ihn nicht erklärt ([ADR-0184](../docs/adr/0184-with-ai-explanations-off-nothing-is-sent-and-no-event-is-fired.md)); gespeicherte Optionen, die das Formular ablehnen würde, senden nichts und werden einmal geloggt | `not_configured` (aus hat keinen Code) |
| Auszug | der Lauf hat einen Logauszug | `no_excerpt` |
| Entität | im Zustandsautomaten und nicht `unavailable`, `supported_features` mit Daten erzeugen (1), die Aktion `ai_task.generate_data` vorhanden; direkt vor dem Aufruf nochmals geprüft | `entity_missing`, `unsupported` |
| Aufruf | `ai_task.generate_data` über `hass.services.async_call` mit `task_name` `vordr_failed_run`, `entity_id`, `instructions` und `structure`, `return_response`; höchstens 60 s; eine Frage nach der anderen | `no_answer`: Zeitüberschreitung oder was immer der Anbieter wirft (nur dort gefangen) |
| Antwort | `data` mit `explanation` (Text), `likely_cause` (ein Wert der Liste), `next_steps` (Werte der Liste), `quote` (Text) | `invalid_answer` |
| Zitat | Teil des Auszugs, wie er gesendet wurde, Zeichen für Zeichen, Folgen von Leerraum als ein Leerzeichen, 8 bis 300 Zeichen | `unverified`: nichts von der Antwort wird gezeigt |

- **Was gesendet wird:** Anweisungen auf Englisch, die die Platzhalter nennen, sagen, dass die Fakten Daten sind, und
  die Erklärung in der Sprache von Home Assistant verlangen (ihr Kürzel, z. B. `de`), dann zwischen den Zeilen
  `BEGIN RUN FACTS` und `END RUN FACTS`: der Ergebniscode, das Verb, die Paketnamen der Zusammenfassung des Laufs
  (höchstens 50) und das Ende des Logauszugs (höchstens 2000 Zeichen). Eine Markierungszeile in den Fakten wird
  herausgenommen; Steuerzeichen fallen weg.
- **Vor dem Senden ersetzt** (`redact`), in dieser Reihenfolge: URL und Host der Leitstelle → `<hub>`; der Host des
  Laufs und jeder andere Host, den die Leitstelle kennt, ohne Rücksicht auf Gross- und Kleinschreibung und mit der
  Domain dahinter → `<host>`, `<other-host>`; Namen in der Domain der Leitstelle, wenn sie zwei oder mehr Teile hat
  (`*.site.example` bei `hub.site.example`) → `<server>`; der Host jeder anderen URL, mit Benutzer und Passwort →
  `<server>`; IPv4-Adressen (vier Zahlen bis 255) und IPv6-Adressen (acht Gruppen, oder weniger mit einem `::`) →
  `<ip>`, nicht eine Version wie `10.0.26100.1`, eine Uhrzeit oder eine MAC-Adresse. Zuerst ersetzt, dann gekürzt.
  Andere Wörter gehen unverändert.
- **Die verlangten Felder:** `likely_cause` eines von `package_conflict`, `package_source`, `disk_space`,
  `package_lock`, `network`, `configuration`, `service`, `credentials`, `interrupted`, `other`, `unknown`;
  `next_steps` bis zu drei von `retry_later`, `free_disk_space`, `check_package_sources`, `repair_package_manager`,
  `resolve_conflict_by_hand`, `check_service`, `check_configuration`, `check_credentials`, `check_network`,
  `read_run_log`. Bei `ok` wird die Erklärung auf eine Zeile gebracht und auf 600 Zeichen gekürzt, wiederholte
  Schritte fallen weg.
- **Ergebnis:** das Ereignis `vordr_ai_explanation` `{node, job_id, state, code, entity_id}`, bei `ok` dazu
  `likely_cause`, `next_steps`, `explanation` und `quote`; wenn an, für jeden Lauf aus `EXPLAINED`, auch wenn nicht
  gefragt werden kann; wenn aus, nie. `sensor.vordr_<id>_last_run` des Hosts zeigt `ai_code`, `ai_entity_id` und bei `ok`
  `ai_likely_cause`, `ai_next_steps`, `ai_explanation`, `ai_quote`, solange er diesen Lauf zeigt, ausserhalb des
  Recorders; nichts, wenn aus. Ihre Werte werden wie Zustände übersetzt (`state_attributes` von `last_run`).
- **Grenzen:** zu einem Lauf wird einmal gefragt (Node und Job-ID; die letzten 256 bleiben gemerkt); eine Antwort `ok`
  wird nach einem Hash von Entität und gesendetem Text behalten (die letzten 32), so wird derselbe Fehlschlag auf
  mehreren Hosts einmal gefragt.
- **Nie:** ein Fenster, ein Mindestalter, ein Umfang, die Richtlinie, ein Knopf, ein Reparaturhinweis oder ein
  Befehl. Nichts liest den freien Text.
- **Noch nicht:** Release-Notes und Changelogs, ein Tageslimit, die Erklärung in einem Push (das kann eine
  Automation auf das Ereignis).

### 6f. Blueprints für Benachrichtigungen

Vier Automations-Blueprints in `blueprints/automation/vordr/` machen aus den Ereignissen und Entitäten oben
Benachrichtigungen
([ADR-0173](../docs/adr/0173-notifications-are-shipped-as-blueprints-the-user-imports.md)). Der Benutzer importiert
sie (`ha/README.de.md`); die Integration installiert sie nicht, und ihre Version ändert sich nicht mit ihnen.

| Blueprint | Startet bei | Tut |
|---|---|---|
| `problem.yaml` | `vordr_problem` | `message.<Sprache>` des Problems an einen Benachrichtigungsdienst; optional Wartezeit (ein Problem, das in der Wartezeit endet oder sich ändert, entfällt), ignorierte Gründe, ein Hinweis beim Ende (nur ohne Wartezeit) |
| `run_failed.yaml` | `vordr_run_finished`, `state` nicht `ok` | `message.<Sprache>` des Ergebnisses; Filter nach Art des Laufs und nach ignorierten Ergebnissen (standardmässig `ok_reboot_required`) |
| `reboot_offer.yaml` | `vordr_run_finished` mit `ok_reboot_required`; `vordr_problem` mit `reboot_pending` | der Text und die Aktion „Jetzt neu starten“, wo der Host einen Neustart-Knopf hat; ein Tipp drückt diesen Knopf oder sagt bei geschlossenem Neustart-Fenster, ab wann der Neustart läuft (ADR-0187) |
| `hub_offline.yaml` | Online-Entität der Leitstelle seit N Minuten aus | ein Hinweis und einer, wenn die Verbindung wieder steht |

- Eingaben sind Selektoren: der Benachrichtigungsdienst (Text), Hosts (Geräteauswahl dieser Integration, leer heisst
  jeder Host), die Sprache (`en`, `de`, freier Text; eine Sprache, die die Integration nicht mitliefert, fällt auf
  Englisch zurück). Keine Entitäts-ID steht in einem Blueprint; das Neustart-Angebot findet Neustart-Knopf und
  Neustart-Sensor des Hosts über dessen Gerät.
- Die Aktion erscheint nur, wo der Host den Neustart anbietet (dann gibt es den Knopf). Ein Tipp drückt den Knopf nur,
  wenn der Sensor noch sagt, dass der Host den Neustart braucht, und der Knopf verfügbar ist; sonst sagt er, dass
  nichts getan wurde. Jedes Angebot hat eine eigene Aktions-ID und verfällt nach den gewählten Stunden. Ein Blueprint
  installiert oder startet nie von sich aus neu.
- `mode: parallel` für das Problem (Wartezeit je Host) und das Neustart-Angebot (ein offener Tipp je Host), `queued` für
  die übrigen.
- `ha/tests/test_blueprints.py` lädt jeden Blueprint mit dem Blueprint-Schema von Home Assistant, prüft Ereignisse,
  Ereignisfelder, Gründe, Ergebniscodes und Eingaben gegen die Integration und lässt die Blueprints mit Ereignissen der
  Test-Leitstelle und einem nachgebildeten Benachrichtigungsdienst laufen.

## 7. Änderungen an der Leitstelle

- Endpunkt `/v1/ha` neben `/v1/tunnel`; Client-Register `clients.json` (0600) getrennt vom Host-Register.
- CLI: `clients`, `approve-client`, `reject-client`, `forget-client`, `client-role`; Admin-Codes wie bei den Hosts.
- `HostView` an einer Stelle berechnet (`internal/hub/view.go`, 5a); `/v1/ha` ist der einzige Weg zu HA.
- Befehle laufen durch dasselbe `Resolve` wie früher die MQTT-Befehle.
- `agent_download` und der Download-Pfad `/v1/agent-download/` für das Einrichten eines neuen Hosts (5b).
- `approve_host`, wo `hub.yaml` `ha_approves_hosts` setzt, für Clients mit Rolle `admin` (5c).
- Für den Umstieg gab es bis Leitstelle 0.5 `mqtt.mode: remove`: einmal verbinden, alle eigenen retained Themen
  löschen, trennen.

## 8. Umstieg

Gilt nur für eine Installation, die noch eine Leitstelle bis 0.5 mit MQTT betreibt. Eine neue Installation richtet
gleich die Integration ein (`ha/README.de.md`).

1. Leitstelle mit `/v1/ha` (0.5) ausrollen; MQTT läuft weiter.
2. Integration nach `/config/custom_components/vordr` bringen, HA neu starten.
3. `mqtt.mode: remove`: die MQTT-Entitäten verschwinden, ihre IDs werden frei.
4. Integration einrichten, an der Leitstelle freigeben: Geräte und Entitäten erscheinen unter denselben IDs.
   Lücke: wenige Minuten ohne vordr-Entitäten.
5. Leitstelle 0.6 oder neuer ohne `mqtt`-Abschnitt in `hub.yaml`; das MQTT-Konto der Leitstelle im Broker und seine
   Passwortdatei entfernen.
6. Gegenprobe: alle IDs vorhanden, Knopf „Jetzt prüfen“ ergibt einen Lauf.

## 9. Prüfen

- Leitstelle (Go): Freigabe des Clients, Client kann keine Hosts freigeben, ausser die Leitstelle erlaubt es und er
  hat die Rolle `admin` (5c: jeder Code, das Merkmal mit Schalter und Rolle und erneut nach einem Rollenwechsel),
  Snapshot und Push, Befehle mit allen Ablehnungsgründen, Widerruf trennt; Mutationsprobe auf die Rechteprüfung.
- Integration (Python): `pytest-homeassistant-custom-component` unter Linux, mit der HA-Version, die
  `ha/requirements_test.txt` festlegt. Config-Flow samt Warten auf Freigabe, alle Entitäten, `None` → unbekannt,
  Verfügbarkeit, Knöpfe → Befehle, Entitäts-IDs, Reparaturhinweise. Die Installation über SSH (6d) gegen eine
  nachgebildete Leitstelle und einen echten SSH-Server, dessen Befehle die Skripte der Integration in einer Sandbox
  ausführen (`ha/tests/fake_ssh_host.py`). Das Installieren der Updates von Home Assistant selbst (6c) gegen
  Update-Entitäten von Integrationen, die der Test spielt, und eine gespielte Backup-Integration, samt Neustart
  mittendrin. Die KI-Erklärungen (6e) gegen eine gespielte AI-Task-Entität und Aktion, ohne Anbieter. Ruff und
  Pyright.
- Ende-zu-Ende: der Python-Test startet das echte `vordr-hub`-Binary mit Testzertifikat.

## 10. Ausbaustufen

| Stufe | Inhalt | Fertig, wenn |
|---|---|---|
| H1 (gebaut) | Leitstelle: `/v1/ha`, Client-Register und CLI, `HostView`, Tests | ein Testclient bekommt Snapshot, Pushes und Befehlsergebnisse; ein nicht freigegebener nichts |
| H2 (gebaut) | Integration: Config-Flow mit Freigabe, Verbindung, Entitäten, de/en, Tests | alle Entitäten aus Abschnitt 6 in einem HA-Test, IDs belegt |
| H3 (gebaut) | Reparaturhinweise, Diagnose, `mqtt.mode: remove`, Umstieg (Abschnitt 8) | die Leitstelle läuft ohne MQTT, alle IDs unverändert |
| H5 (gebaut) | Protokoll 2: Codes statt Texte, Übersetzungen (6b) | jede Sprache hat jeden Text (`tests/test_texts.py`) |
| H6 (gebaut als Probebetrieb) | Updates von Home Assistant selbst unter den Regeln von Vördr: Optionen, Plan, erste Sicht, Sensor (6c) | jeder Code getestet; nichts wird installiert |
| H7 (gebaut, Linux) | Den Agenten über SSH auf einem neuen Host installieren: Betreiberschlüssel, Host-Schlüssel, Anmeldung, Erkundung, geprüftes Release, Installation (6d) | jedes Ende getestet gegen eine nachgebildete Leitstelle und einen echten SSH-Server; nirgends bleibt ein Zugangsdatum |
| H8 (gebaut) | Updates von Home Assistant selbst nach dem Plan installieren: Schalter, Pause, Backup, eines nach dem anderen mit dem System zuletzt, Prüfung vor dem Handeln, Prüfung der Version, Neustart, Ereignis, letzter Lauf, Reparaturhinweis (6c) | jedes Ergebnis im Testgerüst von Home Assistant getestet; nichts wird installiert, bevor es eingeschaltet ist |
| H9 (gebaut) | Freiwillige KI-Erklärungen gescheiterter Läufe über AI Task: Punkt, Prüfungen, Ersetzungen, geprüftes Zitat, Ereignis, Attribute des Sensors (6e) | jeder Code gegen ein gespieltes AI Task getestet; nichts gesendet, bevor es eingeschaltet ist, sonst ändert sich nichts |

## 11. Entscheidungen

1. Reparaturhinweise je Host-Problem: **ja** ([ADR-0024](../docs/adr/0024-repair-issues.md)).
2. Gerätename „Vördr <node>“ auch unter eigener Integration: **ja**, die Geräteliste ist global
   ([ADR-0021](../docs/adr/0021-device-name-vordr-node.md)).
3. Verteilung: **über HACS** als `vordr.zip` an jedem Release des öffentlichen Repos; die Integration liegt in
   `custom_components/vordr/` an der Wurzel des Repos, weil HACS diesen Aufbau prüft, bevor es etwas herunterlädt
   ([ADR-0144](../docs/adr/0144-the-integration-lives-at-the-repository-root.md), löst
   [ADR-0136](../docs/adr/0136-integration-via-hacs-as-a-zip-release.md) und damit
   [ADR-0025](../docs/adr/0025-distribution-via-deploy-tool.md) ab); von Hand nach `/config/custom_components/vordr`
   kopieren geht weiter (`ha/README.de.md`).
4. MQTT nach dem Umstieg **ganz aus dem Code entfernen** ([ADR-0026](../docs/adr/0026-remove-mqtt.md)).
5. Updates von Home Assistant selbst: **Regeln in der Integration, standardmässig geschlossen, zuerst als
   Probebetrieb** ([ADR-0158](../docs/adr/0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md));
   Mindestalter ab erster Sicht, eine Patch-Version mit dem Alter ihrer Linie, Firmware und Funkadapter mindestens 14
   Tage ([ADR-0159](../docs/adr/0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md)); eine neue
   Hauptversion von Hand ([ADR-0160](../docs/adr/0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md)).
6. Einen Host aus Home Assistant einrichten: **einmal über SSH, Zugangsdaten nur im Speicher des Flows, das Release
   geprüft gegen den in Home Assistant eingegebenen Betreiberschlüssel und auf dem Host, bevor es läuft, Freigabe an
   der Leitstelle**
   ([ADR-0161](../docs/adr/0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md));
   **gestartet vom Knopf „Host hinzufügen“, der nichts speichert, auch keinen Subentry**
   ([ADR-0185](../docs/adr/0185-the-agent-install-starts-from-an-add-host-button-and-stores-no-subentry.md));
   **Freigabe in Home Assistant nur, wo `hub.yaml` es erlaubt, für die Rolle `admin`: in „Host hinzufügen“ und im
   Reparaturhinweis des wartenden Hosts**
   ([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)).
7. Updates von Home Assistant selbst installieren: **nur eingeschaltet, aus einem Eintrag, mit Pause**
   ([ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)); **vor jedem ein Backup**, das eigene der Entität oder das automatische Backup von Home Assistant,
   höchstens 26 h alt ([ADR-0163](../docs/adr/0163-a-backup-before-every-automatic-home-assistant-update.md)); **eines nach dem anderen, Core, OS und Supervisor zuletzt, vor dem Handeln
   nochmals geprüft, an der Version geprüft, über Neustarts hinweg** ([ADR-0164](../docs/adr/0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)); **ein Fehlschlag bleibt, bis
   eine Installation durchläuft** ([ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)).
8. KI: **freiwillig, in der Integration über AI Task von Home Assistant, für gescheiterte Läufe, nur als Rat**, mit
   ersetzten Namen der Site und geprüftem Zitat; die Leitstelle bleibt ohne KI und API-Schlüssel
   ([ADR-0166](../docs/adr/0166-optional-ai-explanations-of-failed-runs-go-through-home-assistants-ai-task-and-only-advise.md));
   **aus heisst still**: kein Aufruf und kein Ereignis
   ([ADR-0184](../docs/adr/0184-with-ai-explanations-off-nothing-is-sent-and-no-event-is-fired.md)).
9. Updates von Home Assistant selbst, verfeinert: **eine neuere Version einer Linie erbt das Alter der Linie, mit 2
   Tagen eigenem** ([ADR-0179](../docs/adr/0179-a-newer-release-of-a-version-line-inherits-the-lines-age-with-two-days-of-its-own.md));
   **Geräte-Firmware und Funkadapter warten mindestens 14 Tage**
   ([ADR-0180](../docs/adr/0180-device-firmware-and-the-radio-adapter-wait-at-least-14-days.md)), **installieren ohne
   Backup von Home Assistant**
   ([ADR-0181](../docs/adr/0181-device-firmware-and-the-radio-adapter-install-without-a-backup-of-home-assistant.md))
   und **haben 2 Stunden, um ihre Version zu melden**
   ([ADR-0183](../docs/adr/0183-device-firmware-and-the-radio-adapter-get-two-hours-to-report-their-version.md)); **die
   Behebung eines Reparaturhinweises installiert sofort**
   ([ADR-0182](../docs/adr/0182-the-repairs-fix-installs-a-failed-home-assistant-update-at-once.md)).

## 12. Ergebnis des Umstiegs

Nach dem Umstieg (Abschnitt 8) stehen alle Entitäts-IDs unverändert unter der Plattform `vordr`; neu ist nur
`sensor.vordr_hub_connection`, MQTT-Geräte bleiben keine übrig. Die Anzeigenamen folgen der Systemsprache von HA; unter
MQTT waren sie fest deutsch ([ADR-0075](../docs/adr/0075-display-names-follow-system-language.md)).
