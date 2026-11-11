# Vördr – eigene Home-Assistant-Integration statt MQTT

[English](ha-integration.md) · Deutsch

> **Stand:** Gebaut (Abschnitt 10). Home Assistant spricht nur noch über die Integration mit der Leitstelle; MQTT gibt
> es seit Leitstelle 0.6 nicht mehr ([ADR-0026](../docs/adr/0026-remove-mqtt.md)). Einzelheiten aus dem Bau stehen in
> den Abschnitten 5a, 6a und 6b. Ergänzt [architecture.de.md](architecture.de.md), Abschnitt 8 (HA-Entitäten). Technische
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
| Freigabe neuer Hosts | nur an der Leitstelle, wie heute | in HA |
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

- Die Integration erzeugt beim Einrichten ein eigenes Ed25519-Schlüsselpaar (gespeichert im Config-Entry).
- Freigegeben wird sie **nur an der Leitstelle**, wie ein Host: `vordr-hub clients` zeigt wartende Clients,
  `vordr-hub approve-client <name> --fingerprint <fp>` gibt frei, `reject-client` / `forget-client` widerrufen.
  Der Config-Flow zeigt Fingerprint und Befehl und wartet, bis die Freigabe da ist.
- Ein freigegebener HA-Client darf:
  - den Zustand aller freigegebenen Hosts und die Liste wartender Hosts (Name, Fingerprint, OS) lesen,
  - Befehle aus der festen Liste anfragen (`check`, `install`, `install_security`, `install_app:<id>`, `reboot`,
    `agent_update`); die Leitstelle prüft sie wie heute gegen Verben und Richtlinie des Hosts und signiert erst dann.
- Er darf nicht: Hosts freigeben, ablehnen oder vergessen, Richtlinien ändern, selbst signieren, andere Clients sehen.
- Mehrere HA-Clients sind möglich (z. B. ein Test-HA), jeder einzeln freigegeben und widerrufbar.

Änderung am Bedrohungsmodell (architecture.de.md, 5): Ein übernommenes HA kann wie bisher nur erlaubte Aufträge anstossen.
Der Weg über ein beliebiges MQTT-Konto entfällt; die Leitstelle braucht kein Konto am Broker mehr.

## 5. Protokoll `/v1/ha`

JSON-Nachrichten über WebSocket, wie der Agenten-Tunnel; `protocol: 1` im Hello.

| Richtung | Nachricht | Inhalt |
|---|---|---|
| Hub → HA | `challenge` | Nonce |
| HA → Hub | `hello` | `role: "ha"`, Name, öffentlicher Schlüssel, Signatur (Kontext `vordr/ha-hello/v1`), Integrationsversion, `protocol` |
| Hub → HA | `status` | `pending_approval` / `approved` / `rejected` mit Grund; erneut bei Änderung |
| Hub → HA | `snapshot` | Leitstelle (Version, `protocol`, wartende Hosts) und alle Hosts als `HostView`; nach jeder Freigabe und jedem Verbindungsaufbau |
| Hub → HA | `host` / `host_removed` | ein `HostView` bei jeder Änderung / Host vergessen oder abgelehnt |
| Hub → HA | `hub` | Leitstellen-Zustand bei Änderung (wartende Hosts) |
| HA → Hub | `command` | `request_id`, `node`, `command`; `reason` für einen Lauf ausserhalb des Fensters (architecture.de.md 6.4) |
| Hub → HA | `command_result` | `request_id`, `code` (`ok` = Auftrag signiert und gesendet, wartet auf den laufenden Lauf, oder ist dieser Lauf schon, dann mit dessen `job_id`; sonst fester Grund wie `verb_unsupported_by_agent`, `not_allowed_by_host_policy`, `not_connected`), `job_id`; das Ergebnis des Laufs kommt später im `HostView` (`last_run`) |
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

## 6. Die Integration `custom_components/vordr`

- **Config-Flow:** Adresse (z. B. `wss://vordr-hub.example:8443/v1/ha`), optional eigenes CA-Zertifikat (PEM),
  Name des Clients. Erzeugt den Schlüssel, zeigt Fingerprint und Freigabe-Befehl, wartet als Fortschrittsschritt.
  Prüft vorher selbst die Erreichbarkeit (gleiche Stufen wie `vordr-agent hub-check`) und nennt die scheiternde Stufe.
- **Verbindung:** ein Hintergrund-Task je Config-Entry, Backoff 1–30 s, Push statt Polling
  (`DataUpdateCoordinator` ohne Intervall, `async_set_updated_data`). Ohne Verbindung sind alle Entitäten nicht
  verfügbar.
- **Geräte:** Hub-Gerät „Vördr-Leitstelle“, je Host „Vördr <node>“ mit `via_device` auf das Hub-Gerät.
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
- **Reparaturhinweise** (Abschnitt 11): je Host mit Problem ein Hinweis mit Grund und nächstem Schritt; er
  verschwindet, sobald das Problem weg ist.
- **Diagnose-Download:** Verbindung, Versionen, Hostliste – ohne Schlüssel.
- **Übersetzungen:** en und de für Config-Flow, Entitäten und Hinweise.
- **Abhängigkeiten:** nur, was HA mitbringt (`aiohttp` für den WebSocket, `cryptography` für Ed25519).

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
  - Warnung, ab `reboot_pending` der Leitstelle Fehler; besteht das Problem nur aus `reboot_pending`, entfällt dafür
    `problem_<node>` (Problem-Sensor und `vordr_problem` bleiben);
  - nach einem angenommenen „Beheben“ bleibt er weg, bis der Neustart-Lauf endet, höchstens 30 Minuten.
- **Knöpfe** heben eine Ablehnung der Leitstelle als Fehler mit Code ins UI (`command_refused`); die update-Entität
  hat `INSTALL` nur mit der Fähigkeit `install`.

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
  update-Entität, der Gerätename der Leitstelle.
- **Problemschlüssel:** der erste Grund; bei `check_failed` und gescheiterten Läufen der Ergebniscode, weil der
  nächste Schritt an ihm hängt; ein unbekannter Code wird `unknown`.
- **Ereignisse** tragen `message` in jeder mitgelieferten Sprache, damit eine Automation die Sprache ihres
  Empfängers wählt, unabhängig von der Systemsprache.
- `tests/test_texts.py` hält die Dateien zusammen: jede Sprache hat jeden Text, jeder Hinweistext enthält den
  nächsten Schritt seines Schlüssels, ein Code heisst überall gleich.

## 7. Änderungen an der Leitstelle

- Endpunkt `/v1/ha` neben `/v1/tunnel`; Client-Register `clients.json` (0600) getrennt vom Host-Register.
- CLI: `clients`, `approve-client`, `reject-client`, `forget-client`, `client-role`; Admin-Codes wie bei den Hosts.
- `HostView` an einer Stelle berechnet (`internal/hub/view.go`, 5a); `/v1/ha` ist der einzige Weg zu HA.
- Befehle laufen durch dasselbe `Resolve` wie früher die MQTT-Befehle.
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

- Leitstelle (Go): Freigabe des Clients, Client kann keine Hosts freigeben, Snapshot und Push, Befehle mit allen
  Ablehnungsgründen, Widerruf trennt; Mutationsprobe auf die Rechteprüfung.
- Integration (Python): `pytest-homeassistant-custom-component` unter Linux, mit der HA-Version, die
  `ha/requirements_test.txt` festlegt. Config-Flow samt Warten auf Freigabe, alle Entitäten, `None` → unbekannt,
  Verfügbarkeit, Knöpfe → Befehle, Entitäts-IDs, Reparaturhinweise. Ruff und Pyright.
- Ende-zu-Ende: der Python-Test startet das echte `vordr-hub`-Binary mit Testzertifikat.

## 10. Ausbaustufen

| Stufe | Inhalt | Fertig, wenn |
|---|---|---|
| H1 (gebaut) | Leitstelle: `/v1/ha`, Client-Register und CLI, `HostView`, Tests | ein Testclient bekommt Snapshot, Pushes und Befehlsergebnisse; ein nicht freigegebener nichts |
| H2 (gebaut) | Integration: Config-Flow mit Freigabe, Verbindung, Entitäten, de/en, Tests | alle Entitäten aus Abschnitt 6 in einem HA-Test, IDs belegt |
| H3 (gebaut) | Reparaturhinweise, Diagnose, `mqtt.mode: remove`, Umstieg (Abschnitt 8) | die Leitstelle läuft ohne MQTT, alle IDs unverändert |
| H5 (gebaut) | Protokoll 2: Codes statt Texte, Übersetzungen (6b) | jede Sprache hat jeden Text (`tests/test_texts.py`) |

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

## 12. Ergebnis des Umstiegs

Nach dem Umstieg (Abschnitt 8) stehen alle Entitäts-IDs unverändert unter der Plattform `vordr`; neu ist nur
`sensor.vordr_hub_connection`, MQTT-Geräte bleiben keine übrig. Die Anzeigenamen folgen der Systemsprache von HA; unter
MQTT waren sie fest deutsch ([ADR-0075](../docs/adr/0075-display-names-follow-system-language.md)).
