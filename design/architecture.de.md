# Vördr – Entwurf

[English](architecture.md) · Deutsch

> **Stand:** Dieser Entwurf beschreibt den gebauten Stand: Agent für Linux (apt) und Windows (Windows Update, winget),
> Leitstelle mit Freigabe, signierten Aufträgen, Installieren, Neustart und Selbst-Update in Ringen, Bestand von pip,
> pipx, npm und Container-Images mit OSV-Abgleich, Backup vor dem Install. Home Assistant spricht als freigegebener
> Client über `/v1/ha` mit der Leitstelle ([ha-integration.de.md](ha-integration.de.md)); MQTT gibt es nicht mehr
> ([ADR-0026](../docs/adr/0026-remove-mqtt.md)). Appliances ohne eigenen Agenten: [proxy-agent.de.md](proxy-agent.de.md).
> Was noch nicht gebaut ist, steht an seiner Stelle als „noch nicht gebaut“. Technische Namen (Verben, Zustände,
> Konfigschlüssel, Entitäts-IDs) sind englisch, Anzeigenamen folgen der Systemsprache von Home Assistant
> ([ADR-0075](../docs/adr/0075-display-names-follow-system-language.md)). Beispiele verwenden Platzhalter
> (`example-host`, `vordr-hub.example`), keine echten Hostnamen.
>
> **Begründungen:** Die Entscheidungen hinter diesem Entwurf stehen als ADRs in [docs/adr](../docs/adr/README.md).

Vördr hält Rechner und Dienste aktuell und macht in Home Assistant sichtbar, wo das nicht gelingt: Ein Update lässt
sich nicht einspielen, ein Lauf ist gescheitert, Updates bleiben liegen oder ein Rechner meldet sich nicht mehr.
Bauteile: ein Agent pro Rechner (`vordr-agent`) und eine Leitstelle (`vordr-hub`), beide in Go
([ADR-0003](../docs/adr/0003-go.md)). Das Muster
(Ed25519-Identität mit Freigabe, ausgehender Tunnel, signiertes Selbst-Update mit Kanarienvogel) stammt aus einem
früheren Agenten (im Folgenden: das Vorbild-Projekt); die Sicherheitsgrenzen sind hier enger gezogen, weil Aufträge
als root wirken
([ADR-0002](../docs/adr/0002-own-project-following-an-earlier-agent.md)).

## 1. Anlass und Ziel

Home Assistant kennt mit der `update`-Entität und der Seite *Einstellungen → Updates* schon eine zentrale Sicht auf
Updates, aber nur für Geräte, die eine Integration mitbringen (HA selbst, Add-ons, HACS, ESPHome, NAS, Kameras …).
Es fehlen Linux-Server, VMs und LXC-Container, Proxmox-Hosts, Raspberry Pis, Windows-PCs und die Programme darauf.
Ausserdem kennt die `update`-Entität keinen Zustand „fehlgeschlagen“: Scheitert eine Installation, erscheint kurz
eine Fehlermeldung, gespeichert wird nichts.

Geprüfte Alternativen (Stand 2026-10; [ADR-0001](../docs/adr/0001-own-agent-instead-of-patchmon.md)):

| Werkzeug | Warum nicht |
|---|---|
| PatchMon | Zentrale root-Fernausführung auf allen Hosts und ein Browser-SSH-Terminal: Ein übernommener Server bedeutet root überall. Keine HA-Anbindung. |
| updates2mqtt | Nur Docker-Container. |
| LNXlink | Zählt ausstehende Updates, führt aber keine Läufe aus und meldet keine Fehlschläge. |
| HASS.Agent | Windows-Update-Sensor nur in der alten Version; ob der aktuelle Fork ihn hat, ist offen. |

Ziele:

1. Pro Rechner ein HA-Gerät mit seinen Update-Quellen und wichtigen Programmen, jeweils mit Update-Stand.
2. Updates aus HA anstossen (Knopf) oder automatisch im Wartungsfenster einspielen.
3. Jedes Problem als **gemeldeter Zustand** in HA: abgelehnt, gescheitert, hängt, Neustart nötig, Rechner stumm.

Nicht-Ziele ([ADR-0013](../docs/adr/0013-scope-limits.md)):

- Keine allgemeine Fernverwaltung: keine Shell, keine Dateiübertragung, keine Konfigurationsverwaltung.
- Kein Metrik-Monitoring.
- Geräte mit Hersteller-Firmware (Router, Firewall, NAS, Drucker, ESPHome-Geräte) bekommen keinen Agenten. Ihren
  Update-Stand liefern die HA-Integrationen; Vördr beobachtet sie nur über den HA-Blueprint (Abschnitt 8.5).
- Kubernetes-Workloads: Dort laufen Updates als Pull Requests (z. B. Renovate) über GitOps, nicht über einen Agenten.

## 2. Grundsätze

1. **Zustand melden statt aus Fehlern ableiten.** Jedes Ergebnis ist ein fester Code (Abschnitt 7). Fehlertexte
   gehen höchstens als Auszug für Menschen mit, nie in eine Entscheidung. Muss ein Fall unterschieden werden, bekommt
   er einen eigenen Code ([ADR-0007](../docs/adr/0007-fixed-codes-instead-of-error-texts.md)).
2. **Der Agent erledigt seinen Auftrag selbst, samt Prüfung.** Vor jeder Wirkung prüft er die Vorbedingungen. Stimmt
   etwas nicht, lehnt er ab und meldet Zustand und nächsten Schritt, statt halb zu handeln. Was sich ableiten lässt,
   leitet er ab (z. B. die Zeitzone des Hosts für sein Fenster).
3. **Die lokale Richtlinie hat das letzte Wort.** Was ein Host erlaubt (Verben, Umfang, Fenster, Neustart), steht in
   seiner eigenen Konfiguration. Die Leitstelle kann nur anfragen
   ([ADR-0006](../docs/adr/0006-local-policy-decides.md)). Einzige Ausnahme: Einen Lauf, den ein Mensch mit Grund
   verlangt, nimmt der Agent auch ausserhalb seines Fensters an
   ([ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md)). Drückt ein Mensch in HA auf Installieren,
   gibt die Leitstelle diesen Grund selbst mit
   ([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)).
4. **Kleinste Angriffsfläche.** Geschlossene Verbmenge, keine Shell, keine Paketnamen von aussen, keine Weboberfläche
   in der Leitstelle. Agenten sprechen nur mit der Leitstelle, nie mit Home Assistant
   ([ADR-0005](../docs/adr/0005-closed-verbs-without-shell.md),
   [ADR-0010](../docs/adr/0010-administration-only-local.md), [ADR-0023](../docs/adr/0023-own-ha-integration.md)).
5. **Nur Entitäten, die hier etwas bedeuten**, und gelöscht wird nur auf eine sichere Auskunft hin (Lehre aus
   dem Vorbild-Projekt: sonst flackern Entitäten bei jedem Neustart).
6. **Sonderfälle nur ausdrücklich und mit Grund**, der im Protokoll und in HA sichtbar bleibt (z. B. ein Lauf
   ausserhalb des Fensters, 6.4), nie als stiller Schalter.

## 3. Überblick

```
┌───────────── je Rechner ─────────────┐
│ vordr-agent (Go, statische Binary)   │
│  ├─ Quellen: apt | winupdate | …     │
│  ├─ lokale Richtlinie (agent.yaml)   │
│  └─ Ed25519-Identität                │
└──────────────────┬───────────────────┘
                   │ nur ausgehend: WebSocket über TLS
                   ▼
      vordr-hub (Leitstelle) ◀── wss /v1/ha ── Home Assistant (Integration „vordr“)
       ├─ Register + Freigabe                   Knöpfe → Befehle (prüfen, installieren, neu starten)
       ├─ signiert Aufträge
       ├─ Rollout in Ringen
       ├─ verteilt Agent-Binaries (signiert sie nicht)
       └─ Ereignisprotokoll

Betreiber-Schlüssel: signiert die Agent-Manifeste, nie auf der Leitstelle
```

| Teil | Läuft wo | Aufgabe |
|---|---|---|
| `vordr-agent` | auf jedem verwalteten Rechner, als systemd- bzw. Windows-Dienst | Quellen abfragen, Läufe prüfen und ausführen, Ergebnis melden, sich selbst aktualisieren |
| `vordr-hub` | auf einem Linux-Host oder in einem eigenen Container ([ADR-0054](../docs/adr/0054-hub-in-own-container.md)), als systemd-Dienst | Agenten anmelden und freigeben, Aufträge signieren und zustellen, Rollout in Ringen, Agent-Binaries verteilen, Zustand an HA geben |
| Home Assistant | die vorhandene Instanz, mit der Integration `custom_components/vordr` | einzige Oberfläche: Anzeige, Knöpfe, Benachrichtigungen |
| Signieren der Releases | beim Betreiber, nie auf der Leitstelle | Betreiber-Schlüssel, signiert das Manifest jeder Agent-Version (10.4) |

Verbindungen:

- **Agent → Hub:** nur ausgehend, WebSocket über TLS (`/v1/tunnel`), Anmeldung per Ed25519-Challenge. Auf den Hosts
  ist kein Port offen, Reconnect mit Backoff 1–30 s.
- **Home Assistant → Hub:** die Integration verbindet sich als freigegebener Client über `/v1/ha`, auf demselben Port
  und mit demselben Zertifikat ([ha-integration.de.md](ha-integration.de.md),
  [ADR-0023](../docs/adr/0023-own-ha-integration.md)). Die Leitstelle braucht keinen Weg zu HA.
- **Agent-Binaries:** Der Hub liefert sie per HTTPS mit Einmal-Token (15 min) aus. Ihre Echtheit sichert die
  Betreiber-Signatur, nicht der Hub.

## 4. Vertrauen und Schlüssel

| Schlüssel | Liegt | Wofür | Wer ihn stiehlt, kann |
|---|---|---|---|
| Agent-Identität (Ed25519) | jeder Host; Linux als Datei mit 0600, Windows DPAPI-geschützt | Anmeldung am Hub | sich als dieser Host ausgeben und dessen Zustand fälschen |
| Hub-Schlüssel (Ed25519) | Hub | signiert jeden Auftrag; jeder Agent hat den öffentlichen Schlüssel fest in seiner Konfiguration | erlaubte Aufträge innerhalb der lokalen Richtlinien auslösen, mit Grund auch ausserhalb des Fensters |
| Betreiber-Schlüssel (Ed25519) | beim Betreiber, nur für ihn lesbar; nie auf dem Hub. Der Ersatzschlüssel liegt getrennt davon (10.4) | signiert das Manifest der Agent-Binaries | Code auf alle Agenten bringen – darum nie auf dem Hub, dazu ein Ersatzschlüssel (10.4) |
| Code-Signing-Zertifikat (Authenticode) | keins ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)) | erst nötig, falls Smart App Control den Tausch der Windows-Binary blockt | nichts über die Manifest-Signatur hinaus |

**Warum Aufträge signiert werden, obwohl der Tunnel authentifiziert ist:** Im Vorbild-Projekt weist sich nur der Agent aus.
Hier wirken Aufträge als root, also muss der Agent wissen, dass ein Auftrag vom Hub stammt, auch wenn jemand DNS oder
das Netz umlenkt. Ein Auftrag trägt `job_id`, `node`, `verb`, `params`, `issued_at`, `expires_at` (höchstens 5 min nach
`issued_at`) und die Signatur. Der Agent verwirft Aufträge mit fremdem `node`, abgelaufene und bereits gesehene
`job_id` (Wiederholungsschutz) und meldet das als `rejected_invalid_job`
([ADR-0002](../docs/adr/0002-own-project-following-an-earlier-agent.md)).

TLS sichert die Vertraulichkeit. Das Vertrauen in Aufträge und Binaries hängt an den Signaturen
([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md), [ADR-0035](../docs/adr/0035-backup-operator-key.md)).

## 5. Bedrohungsmodell

| Angreifer hat … | kann | kann nicht |
|---|---|---|
| einen Host übernommen | dessen Zustand fälschen | andere Hosts beeinflussen, Aufträge erzeugen |
| den Hub übernommen | den Bestand lesen; erlaubte Aufträge auslösen, mit einem Grund auch ausserhalb des Fensters (ADR-0087); die HA-Anzeige fälschen | Befehle oder Code auf Hosts bringen, Pakete wählen, Umfang erweitern, Neustartverbot oder Mindestabstand umgehen, Agent-Binaries tauschen |
| ein freigegebenes Home Assistant | Prüfen und Installieren anstossen, mit Grund auch ausserhalb des Fensters, mit Rolle `admin` auch Neustarts (12) – über den Hub, also innerhalb der Richtlinien | mehr als der Hub |
| das Netz (mitlesen, umlenken) | Verbindungen stören | Aufträge fälschen (Hub-Signatur), Binaries unterschieben (Betreiber-Signatur und SHA-256) |
| den Betreiber-Schlüssel | Code auf alle Agenten bringen | – darum nie auf dem Hub; Rotation über einen neuen öffentlichen Schlüssel in der Agent-Konfiguration |

Zum Vergleich: Bei einem zentralen Patch-Server mit Fernausführung bedeutet ein übernommener Server root auf allen
Hosts.

Weitere Bremsen im Agenten: höchstens ein Lauf gleichzeitig, Mindestabstand zwischen zwei Installationsläufen
(`install.min_interval_h`), Neustart nur wenn nötig, erlaubt und im Fenster (oder mit Grund, 6.4).

## 6. Verben

Die Verbmenge ist geschlossen. Was hier nicht steht, gibt es nicht
([ADR-0005](../docs/adr/0005-closed-verbs-without-shell.md)).

### 6.1 Aufträge vom Hub an den Agenten

| Verb | Parameter | Wirkung | Lokale Bedingung |
|---|---|---|---|
| `check` | – | Quellen neu abfragen (Paketlisten, Windows-Update-Suche) | immer erlaubt; höchstens alle 5 min |
| `install` | `scope`: `security`, `all`, `app:<id>`, `pkg:<ort>[/<name>]` (pip/npm, 11.4), `image:<projekt>/<dienst>` oder `images` (Container-Images, 11.5); `outside_window` (6.4) | Updates einspielen | Umfang steht in `install.allowed_scopes` (bei `pkg:` der Ort in `packages.update`; bei `images` jeder Dienst, den ein `image:`-Eintrag nennt), Fenster offen oder ein Grund, Vorbedingungen erfüllt |
| `reboot` | `outside_window` (6.4) | Neustart | `reboot.allowed`, Fenster offen oder ein Grund, und ein Neustart ist tatsächlich nötig |
| `agent_update` | signiertes Manifest, Einmal-Token | Agent tauscht sich selbst | `agent_update.enabled`; Manifest vom Betreiber signiert |
| `cancel` | `job_id` | Lauf abbrechen | nur bis zum Beginn der Paketinstallation; danach läuft der Paketmanager zu Ende, sonst bliebe das System halb aktualisiert |
| `backup` | `guest`: `vm/<id>` oder `ct/<id>` | nur der Agent des Hypervisors: ein Backup dieses Gastes nach PBS, direkt vor dessen Install (10.2) | `guest_backup` in der `agent.yaml`; der Gast liegt auf diesem Host; höchstens ein Backup je Gast in 10 min |

`app:<id>` verweist auf einen Eintrag der App-Liste, die der Agent selbst aus seiner Konfiguration meldet. Paketnamen
kommen nie vom Hub.

Bewusst nicht vorhanden: Befehle ausführen, Dateien übertragen, Konfiguration ändern, Pakete neu installieren oder
entfernen, beliebige Dienste neu starten.

### 6.2 Meldungen vom Agenten an den Hub

| Nachricht | Inhalt |
|---|---|
| `hello` | Name, öffentlicher Schlüssel, Signatur über die Challenge (mit Kontext `vordr/hello/v1`, Nonce, Name und Schlüssel), Fakten (OS und Version, Architektur, Virtualisierung), Agent-Version, Verben, die diese Version kann, Kurzfassung der lokalen Richtlinie (daraus entstehen die passenden HA-Knöpfe) |
| `report` | Zustand je Quelle und App (7.2) mit Fakten wie `apt_version`, `install_mode`, `unattended_upgrades`, Neustartbedarf mit Grund (7.4) |
| `job_status` | angenommen oder abgelehnt mit Code, Phase, Fortschritt, Ergebnis (7.3) |
| `heartbeat` | lebt, alle 30 s. Der Hub antwortet mit einem Heartbeat; 90 s Stille heisst auf beiden Seiten: Verbindung tot |
| `inventory` | Pakete von pip, pipx und npm je Ort (11.2), nach jeder Prüfung; nur an einen Hub, dessen `status` das Merkmal `inventory` nennt |

Der Hub antwortet auf `hello` mit `status` (`pending_approval`, `approved` oder `rejected` mit Grund `bad_hello`,
`key_mismatch`, `too_many_pending`, `rejected`) und schickt `status` erneut, sobald sich der Zustand ändert. Aufträge
schickt er nur an freigegebene Hosts und nur mit Verben, die der Agent im `hello` genannt hat.

**Merkmale der Leitstelle** (seit Leitstelle 0.18.0): Jeder `status` trägt `features`, die Nachrichtenarten, die diese
Leitstelle über das Grundprotokoll hinaus annimmt (`inventory`, `vet`, `containers`, `image_contents`; `ack` kündigt
sie schon in der Challenge an, 10.2). Eine ältere Leitstelle trennt die Verbindung bei
einer Nachricht, die sie nicht kennt. Der Agent schickt eine neuere Art deshalb nur, wenn die Leitstelle sie genannt
hat; er probiert nicht aus und liest es nicht aus einem Abbruch. So ist die Reihenfolge der Updates egal, die Betriebsregel
„Leitstelle vor Agent“ bleibt trotzdem.

### 6.3 Befehle am Host selbst

Für den Betrieb vor Ort. Sie lesen und prüfen nur; keiner installiert etwas:

```
vordr-agent check [--no-refresh] | identity | check-config | hub-check | version
vordr-agent packages [--plan <ort>[/<name>] | --plan-deps <ort>:<name>[,<name>…]]   # 11.2, 11.4
vordr-agent run                                                 # der Dienst selbst
vordr-agent install-service [--replace-config "<grund>"] | uninstall-service   # nur Windows (11.1)
```

Installieren und Neustart kommen nur als signierter Auftrag der Leitstelle: aus HA, aus der Warteschlange oder von der
Automatik (10.3), ausserhalb des Fensters mit Grund (6.4). Einen Befehl am Host dafür gibt es nicht.

### 6.4 Läufe ausserhalb des Fensters

[ADR-0087](../docs/adr/0087-runs-outside-the-window-with-a-reason.md): Das Fenster bindet automatische Läufe und den
Neustart-Knopf. Will ein Mensch einen Lauf jetzt, nennt er einen Grund. Beim Installieren tut das die Leitstelle für
ihn ([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)):

- **Installieren-Knopf:** Ein HA-`command` mit Verb `install` (Systempakete, Sicherheitsupdates, App, pip/npm-Ort
  oder -Paket, Abhängigkeiten auffrischen) ohne Grund bekommt von der Leitstelle `hub.ButtonReason` („install started
  in Home Assistant outside the window“), wenn das angekündigte Fenster (`policy.window`, `policy.tz`) gerade zu ist
  und der Agent Gründe annimmt. Entschieden wird beim Absenden; ein Befehl aus der Warteschlange also erst, wenn er an
  der Reihe ist. Ein eigener Grund bleibt, wie er ist. Kann die Leitstelle das Fenster nicht lesen (keines angekündigt,
  unbekannte Zeitzone), gibt sie nichts mit; dann entscheidet der Agent.
- **Vor dem Backup:** Einen Install-Auftrag ohne Grund, dessen Fenster gerade zu ist, lehnt die Leitstelle mit
  `outside_window` ab, bevor sie ihn schickt und bevor sie den Gast sichern lässt (10.2). Das gilt für jeden Weg
  (HA, Warteschlange) und trifft nur Agenten, die keine Gründe annehmen.
- **Auftrag:** `install` und `reboot` tragen dann `outside_window`, den Grund: nicht leer, höchstens 200 Byte UTF-8,
  keine Steuerzeichen (`protocol.ValidReason`). Jedes andere Verb damit ist ein ungültiger Auftrag. Die Leitstelle
  signiert den Grund mit.
- **Agent:** Ein Auftrag mit Grund kommt an `install.window` (auch bei `pkg:`) und `reboot.window` vorbei; der Agent
  schreibt den Grund ins Log. Alles andere gilt weiter: erlaubter Umfang, `packages.update`, `reboot.allowed`, Neustart
  nur wenn nötig, `install.min_interval_h`, ein Lauf zur Zeit, die Backup-Prüfung der Leitstelle.
- **Ältere Agenten** lesen Aufträge streng und würden das unbekannte Feld als `rejected_invalid_job` ablehnen. Der
  Agent kündigt es darum im Hello an (`policy.outside_window`); einem anderen schickt die Leitstelle keinen Grund und
  antwortet HA mit `outside_window_unsupported`.
- **Leitstelle:** Ein HA-`command` darf `reason` tragen. Sie prüft ihn (`bad_reason`; `bad_request` bei `check` und
  `agent_update`), legt ihn in den Lauf (`last_run.outside_window`) und ins Ereignisprotokoll (`command`, `job_sent`,
  `queued_job_started`). Ein Befehl, der hinter einem laufenden Lauf wartet, übernimmt den Grund, wenn er mit Grund
  erneut kommt. Die Rollen bleiben: Neu starten braucht `admin`.
- **HA:** Aktionen `vordr.install_outside_window` (Update-Entitäten: Systempakete oder ein pip/npm-Ort),
  `vordr.press_outside_window` (Knöpfe: Sicherheitsupdates, Apps, Neu starten), `vordr.update_package` und
  `vordr.refresh_dependencies` (11.4) mit dem Feld `reason`. Es gibt sie nur, wo die Leitstelle
  `capabilities.outside_window` meldet.

## 7. Zustände

### 7.1 Host (aus Sicht des Hubs)

`pending_approval`, `online`, `offline` (kein Heartbeat seit 2 min), `rejected`.

### 7.2 Quelle oder App

| Zustand | Bedeutung |
|---|---|
| `up_to_date` | nichts ausstehend |
| `updates_available` | Updates ausstehend; Anzahl, davon Sicherheitsupdates |
| `check_failed` | Abfrage gescheitert, mit Code aus 7.3 (z. B. `precheck_repo_unreachable`) |
| `unknown` | noch nicht abgefragt oder keine sichere Auskunft; HA-Entitäten bleiben unverändert |

### 7.3 Lauf

Phasen während eines Laufs: `queued`, `prechecking`, `downloading`, `installing`, `verifying`, `rebooting`, dazu
`backing_up`: ein Install wartet auf das Backup seines Gastes (10.2). Diese Phase setzt die Leitstelle; der Agent des
Gastes hat den Auftrag dann noch nicht.

Ergebnisse (`problem` = setzt `binary_sensor.<node>_problem`):

| Code | Bedeutung | Nächster Schritt (Text in HA) | `problem` |
|---|---|---|---|
| `ok` | alles im gewählten Umfang eingespielt | – | nein |
| `ok_reboot_required` | eingespielt, Neustart nötig | Neustart im Fenster oder per Knopf | nein |
| `rejected_invalid_job` | Signatur, Host, Ablauf oder Wiederholung stimmt nicht | Hub und Uhrzeit prüfen; kann auf einen Angriff hinweisen | ja |
| `rejected_scope_not_allowed` | Umfang lokal nicht erlaubt | Richtlinie des Hosts | nein |
| `rejected_outside_window` | ausserhalb des Fensters | warten oder mit Grund ausserhalb des Fensters (6.4) | nein |
| `rejected_rate_limited` | Mindestabstand nicht erreicht | warten | nein |
| `rejected_busy` | ein anderer Lauf oder ein fremder Paketmanager hält die Sperre (z. B. unattended-upgrades) | später erneut; Doppelbetrieb beenden | nein |
| `rejected_not_allowed` | die lokale Richtlinie verbietet das Verb (z. B. `reboot` bei `reboot.allowed: false`) | Richtlinie des Hosts | nein |
| `rejected_unsupported` | diese Agent-Version oder Plattform kann das Verb nicht; der Hub hätte es nicht schicken dürfen | Agent aktualisieren | ja |
| `precheck_disk_space` | zu wenig Platz (Paket-Cache, `/boot`) | aufräumen | ja |
| `precheck_package_db_broken` | Paketdatenbank unterbrochen (`dpkg --audit` nicht leer) | `dpkg --configure -a` von Hand | ja |
| `precheck_repo_unreachable` | Paketquelle nicht erreichbar oder ihre Signatur ungültig | Quelle und Netz prüfen | ja |
| `precheck_would_remove_packages` | der Probelauf würde Pakete entfernen, die kein Paket des Laufs ersetzt | von Hand entscheiden | ja |
| `precheck_version_flagged` | pip/npm-Update (11.4): eine Version des Plans, auch eine Abhängigkeit, steht in den OSV-Daten (Schadsoftware oder Schwachstelle) | Eintrag ansehen; auf eine behobene Version warten | ja |
| `precheck_version_too_new` | pip/npm-Update: eine Version des Plans ist jünger als `packages.min_age_days` | warten, bis sie alt genug ist | nein |
| `precheck_vetting_unavailable` | pip/npm-Update: die Leitstelle konnte den Plan nicht prüfen (keine oder veraltete OSV-Daten, keine Antwort) | Schwachstellendaten der Leitstelle prüfen | ja |
| `precheck_not_from_registry` | npm-Update: ein Eintrag des Plans kommt nicht aus der Registry (anderer Host, git, Datei; [ADR-0128](../docs/adr/0128-check-npm-tarball-origins-and-let-npm-12-fetch-them.md)); installiert wurde nichts | ansehen, wohin die Abhängigkeit zeigt; einen solchen Ort von Hand aktualisieren | ja |
| `failed_package_manager` | Paketmanager mit Fehler beendet; Exit-Code und Ergebnis der Nachprüfung als Attribute | Auszug lesen | ja |
| `failed_verification` | der Lauf meldet Erfolg, die Nachprüfung findet aber noch ausstehende Updates oder eine unterbrochene Paketdatenbank | Auszug lesen | ja |
| `failed_health` | der Lauf ging durch, aber eine Prüfung aus `health:` der `agent.yaml` (Dienst, Container, HTTP) schlägt nach 3 Minuten noch fehl | genannten Dienst ansehen | ja |
| `failed_windows_update` | Windows Update meldet einen Fehler; `hresult` als Zahl | HRESULT nachschlagen | ja |
| `failed_target_auth` | Stellvertreter: das Gerät lehnt das Dienstkonto ab ([proxy-agent.de.md](proxy-agent.de.md)) | Dienstkonto am Gerät und Zugangsdatei der Instanz prüfen | ja |
| `failed_target_tls` | Stellvertreter: das Zertifikat des Geräts passt nicht zum festgenagelten Fingerabdruck bzw. Namen | Zertifikat prüfen; gewollt neu → Fingerabdruck anpassen, sonst sitzt vielleicht jemand dazwischen | ja |
| `failed_target_hostkey` | Stellvertreter über SSH: das Gerät bietet nicht den festgenagelten Host-Schlüssel ([ADR-0094](../docs/adr/0094-ssh-drivers-use-the-system-openssh-client.md)) | Host-Schlüssel prüfen (`ssh-keyscan`); gewollt neu → `target.host_key` anpassen, sonst sitzt vielleicht jemand dazwischen | ja |
| `failed_config_backup` | Stellvertreter: Konfigurationssicherung gescheitert oder unbrauchbar ([ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md)) | Log der Instanz ansehen; ohne frische Sicherung kein Firmware-Update | ja |
| `failed_backup` | das Backup direkt vor dem Install kam nicht zustande (vzdump scheiterte, PBS hat es nicht, der Hypervisor ist weg); installiert wurde nichts ([ADR-0096](../docs/adr/0096-fresh-backup-right-before-the-install.md)). Auch der Agent des Hypervisors beendet einen gescheiterten `backup`-Auftrag damit | Detail des Laufs und Backup-Log des Hypervisors ansehen | ja |
| `timeout` | Lauf dauerte länger als `install.timeout_min` | Host prüfen | ja |
| `aborted` | per `cancel` abgebrochen, vor der Installation | – | nein |
| `interrupted` | Agent oder Host während des Laufs beendet; beim nächsten Start aus dem Laufjournal erkannt. Bei der Leitstelle auch vorläufig nach einem Tunnelabbruch, bis das nächste Hello sagt, ob der Lauf weitergeht (10.2, ADR-0114) | Paketdatenbank prüfen | ja |

Abgelehnte Aufträge ohne `problem` sind erwartbare Antworten und stehen nur im Sensor „Letzter Lauf“. Wiederholen sie
sich, bis Updates liegen bleiben, greift `stale` (7.5).

**Gescheiterter Install bleibt** ([ADR-0124](../docs/adr/0124-a-failed-system-install-stays-until-one-goes-through.md)):
Endet ein Install der Systempakete (Umfang `security` oder `all`) mit einem Code mit `problem`, merkt sich die Leitstelle
ihn neben dem letzten Lauf. Er bleibt Grund des Problems, auch wenn danach ein `check`, ein Image-, pip/npm- oder
Agent-Update läuft, eine Ablehnung kommt oder ein Lauf noch unterwegs ist. Erst ein Install der Systempakete, der
durchgeht (`ok`, `ok_reboot_required`), mit mindestens seinem Umfang (`all` deckt `security`), hebt ihn auf. Bei einem
Kanarienvogel hält er damit auch die späteren Ringe und die Verteilung neuer Agenten an (10.3, 10.4). Die Ansicht
trägt den gemerkten Lauf neben dem letzten (`failed_install`, mit Auszug und Laufprotokoll), und HA zeigt ihn am
Problem-Sensor.

**Einordnung ohne Fehlertexte:** Der Agent wertet nur Exit-Codes und strukturierte Ausgaben aus (Zeilen des
Probelaufs, `APT::Status-Fd`, Ergebniscodes von Windows Update). Nach einem Fehlschlag fragt er den Zustand gezielt
nach (Sperre, `dpkg --audit`, freier Platz, Quellen). Der Code entsteht aus diesen Prüfungen; der Fehlertext geht nur
als `log_excerpt` mit ([ADR-0007](../docs/adr/0007-fixed-codes-instead-of-error-texts.md)).

### 7.4 Neustart

`reboot_required` (ja/nein) mit `reboot_reason` = `kernel`, `libraries`, `windows_update` oder `other`, dazu die
auslösenden Pakete als Liste. Erkennung:

- laufender Kernel gegen den neuesten installierten Kernel derselben Variante (`kernel`). Die Variante ist der letzte
  Teil nach der Version (`pve`, `amd64`, `generic`, `2712`). Raspberry Pi OS installiert die Kernel für Pi 5 (`2712`)
  und Pi 4 (`v8`) nebeneinander, und dpkg hält `v8` für neuer. Ohne den Vergleich je Variante verlangte ein Pi 5
  dauernd einen Neustart, und jeder Neustart endete mit `failed_verification`,
- `/var/run/reboot-required` und `.pkgs`, wo vorhanden (Ubuntu),
- `needrestart -b`, wo installiert (maschinenlesbare Ausgabe, `libraries`),
- unter Windows die Registry-Schlüssel von Windows Update und Component Based Servicing (`windows_update`, 11.1).

### 7.5 Hängt

Der Hub setzt `stale` (mit `problem`), wenn Updates länger als `stale_after_days` (Standard 7) ausstehen, ohne dass ein
Lauf sie einspielt, gleich aus welchem Grund. Das fängt auch Fälle, für die es keinen eigenen Code gibt
([ADR-0014](../docs/adr/0014-stale-after-seven-days.md)).

### 7.6 Selbst-Update des Agenten

Phasen `downloading`, `verifying`, `rebooting`, dann ein Ergebnis aus 7.3
([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md)):

| Code | wann |
|---|---|
| `ok` | die neue Version meldet sich auf ihrer ersten freigegebenen Verbindung; oder sie läuft schon |
| `rejected_invalid_job` | Manifest oder Signatur ungültig, oder die Release ist nicht neuer (`older_release`) |
| `rejected_unsupported` | Selbst-Update ist auf dem Host aus (z. B. ohne `agent_update.enabled`), oder die Release hat keine Binary für OS und Architektur des Hosts |
| `rejected_busy` | ein anderer Lauf ist offen |
| `precheck_repo_unreachable` | der Download scheitert |
| `failed_verification` | Hash oder Grösse falsch; der Probelauf der neuen Binary scheitert; oder die neue Version erreicht binnen 10 min keine freigegebene Verbindung (hängt sie, greift nach 15 min der Update-Wächter, [ADR-0098](../docs/adr/0098-update-guard-rolls-back-a-hung-new-agent.md)), und der Agent ist auf die alte zurückgesprungen |
| `failed_package_manager` | Schreiben, Markierung, Start des Update-Wächters oder Tausch der Binary scheitert |
| `timeout` | nach der Phase `rebooting` meldet sich binnen 30 min kein Agent |

Ausser `ok` und `rejected_busy` setzen alle `problem`. Fristen: Einmal-Token für den Download 15 min, Bestätigung
durch die neue Version 10 min, Warten der Leitstelle 30 min (10.4). Unter Windows gilt dasselbe (10.4,
[ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)).

## 8. Home-Assistant-Entitäten

Die Entitäten legt die Integration `custom_components/vordr` an, die sich als freigegebener Client über `/v1/ha` mit
der Leitstelle verbindet ([ha-integration.de.md](ha-integration.de.md), Abschnitte 6 und 6a). Begründungen:
[ADR-0023](../docs/adr/0023-own-ha-integration.md) (Integration statt MQTT),
[ADR-0017](../docs/adr/0017-entity-ids-with-prefix.md) (Entitäts-IDs),
[ADR-0021](../docs/adr/0021-device-name-vordr-node.md) (Gerätename).

Entitäts-IDs setzt die Integration ausdrücklich (englisch, mit Präfix `vordr_`, damit sie nicht mit Entitäten anderer
Integrationen gleichen Hostnamens kollidieren); Anzeigenamen kommen aus den Übersetzungen und folgen der Systemsprache
von HA. `<id>` ist der Hostname mit `_` statt `-` und `.`. Den Zustand schiebt die Leitstelle als `HostView`. Was sie
nicht weiss, steht dort als `null`, und HA zeigt „unbekannt“ – nie eine Vermutung. Ohne Verbindung zur Leitstelle
sind alle Entitäten nicht verfügbar.

### 8.1 Gerät je Host

Name „Vördr <node>“, Kennung `("vordr", "host:<node>")`, Hersteller „Vördr“, Modell aus den Fakten (z. B.
„Debian 13 · lxc“), `sw_version` = Agent-Version, unter dem Hub-Gerät.

| Entität | Typ | Anzeigename | Inhalt | Gibt es nur, wenn |
|---|---|---|---|---|
| `update.vordr_<id>_system` | update | Systempakete | siehe 8.3 | der letzte Bericht eine Paketquelle nennt |
| `update.vordr_<id>_<app>` | update | Name der App | echte Versionen (installiert, verfügbar) | noch nicht gebaut; bis dahin ein Knopf je App (unten) |
| `update.vordr_<id>_agent` | update | Agent | Agent-Version gegen freigegebene Version; Installieren = `agent_update` (10.4) | die Leitstelle verteilt Agent-Releases |
| `update.vordr_<id>_containers` | update | Container-Images | veraltete und verwundbare Images, Neubauten per Knopf (11.5) | `packages.containers` am Host |
| `update.vordr_<id>_packages_<ort>` | update | Pakete <ort> | pip/npm-Pakete eines Orts; Installieren = alle Kandidaten, Dienst `vordr.update_package` = eines, `vordr.refresh_dependencies` = Abhängigkeiten mit Fund (11.4) | der Ort steht in `packages.update` und die Leitstelle hat OSV-Daten |
| `sensor.vordr_<id>_last_run` | sensor (enum) | Letzter Lauf | Phase oder Ergebniscode (7.3) des letzten Auftrags; Attribute `job_id`, `verb`, `scope`, `started`, `finished`, `reason`, `log_excerpt` | immer |
| `sensor.vordr_<id>_pending_updates` | sensor | Ausstehende Updates | Anzahl; unbekannt, wenn die Prüfung scheiterte | der letzte Bericht eine Paketquelle nennt |
| `sensor.vordr_<id>_security_updates` | sensor | Sicherheitsupdates | Anzahl | wie oben |
| `sensor.vordr_<id>_last_report` | sensor (timestamp) | Letzte Meldung | Zeitpunkt des letzten `report` | immer |
| `binary_sensor.vordr_<id>_reboot_required` | binary_sensor | Neustart nötig | an/aus; **unbekannt**, wenn kein Signal antwortete (`checked` leer, z. B. Container); Attribute `reason`, `packages`, `checked` | immer |
| `binary_sensor.vordr_<id>_problem` | binary_sensor (problem) | Update-Problem | an bei `offline` (nach `offline_after_min`, Standard 10; nicht, solange der Host angekündigt hat, dass er schläft oder herunterfährt, siehe 11.1), Codes mit `problem` (7.3), `check_failed`, `stale`; Attribute `reason`, `reasons`, `next_step`, `check_failed_code` – Codes, die HA übersetzt (design/ha-integration.de.md, 6b) –, dazu `failed_install` (Auftrag, Umfang, Code, Zeiten, Auszug, Laufprotokoll), solange ein gescheiterter Install der Systempakete gemerkt ist (ADR-0124), nicht im Recorder | immer |
| `binary_sensor.vordr_<id>_online` | binary_sensor (connectivity) | Verbunden | Tunnel steht (sofort, ohne Karenz) | immer |
| `sensor.vordr_<id>_malicious_packages` | sensor | Schädliche Pakete | installierte pip/npm-Pakete, die OSV als Schadsoftware führt; Attribut `findings` (11.2) | der Agent schickt einen Paketbestand |
| `sensor.vordr_<id>_vulnerable_packages` | sensor | Verwundbare Pakete | Pakete mit bekannter Schwachstelle; Attribute `findings` (je Fund auch `reach` und `held_by`, ADR-0129), `waiting`, `locations`, `packages_total`. Die Listen (`findings`, `locations`, `undecided`, bei den veralteten Paketen `updates`) stehen nur im Zustand, nicht in der Historie (ADR-0101) | wie oben |
| `sensor.vordr_<id>_outdated_packages` | sensor | Veraltete Pakete | direkte pip/npm-Pakete mit neuerem Release; Attribut `updates` mit `latest` und `candidate` (11.3) | wie oben |
| `button.vordr_<id>_check` | button | Jetzt prüfen | Befehl `check` | der Agent kann `check` |
| `button.vordr_<id>_install_security` | button | Sicherheitsupdates installieren | Befehl `install_security` | der Agent kann `install`, `security` ist erlaubt und nicht schon Standardumfang |
| `button.vordr_<id>_reboot` | button (restart) | Neu starten | Befehl `reboot` | der Agent kann `reboot` und `reboot.allowed` |
| `button.vordr_<id>_install_app_<app>` | button | Name der App | Befehl `install_app:<app>` | die App steht in `apps` und `app:<app>` ist erlaubt |

Knöpfe entstehen nur für Verben, die der Agent im Hello angekündigt hat **und** die seine Richtlinie erlaubt. Eine
Richtlinie allein bietet nichts an, was die Agent-Version nicht kann. Hat ein Host noch keinen Bericht, werden die
Paket-Entitäten weder angelegt noch gelöscht (unentschieden); erst ein Bericht entscheidet.

### 8.2 Hub-Gerät

| Entität | Typ | Anzeigename | Inhalt |
|---|---|---|---|
| `binary_sensor.vordr_hub_online` | binary_sensor (connectivity) | Leitstelle | Last Will des Hubs |
| `sensor.vordr_hub_hosts_online` | sensor | Hosts verbunden | Anzahl |
| `sensor.vordr_hub_hosts_with_problem` | sensor | Hosts mit Problem | Anzahl; Attribut mit der Liste |
| `sensor.vordr_hub_pending_approvals` | sensor | Wartende Freigaben | Anzahl; Attribut mit Namen und Fingerprints |
| `sensor.vordr_hub_vulnerability_data` | sensor (timestamp) | Schwachstellendaten | wann die Leitstelle ihre OSV-Daten zuletzt geholt hat (das älteste Ökosystem); je Ökosystem Zustand `ok`, `stale`, `missing` und Fehlercode (11.2) |

### 8.3 Die Entität „Systempakete“

Eine `update`-Entität erwartet zwei Versionen; für eine Paketmenge gibt es keine einzelne. Darum:

- `installed_version`: das Betriebssystem, z. B. `Debian GNU/Linux 13 (trixie)`.
- `latest_version`: gleich `installed_version`, wenn nichts aussteht oder der Install-Knopf nichts davon nähme, sonst
  z. B. `12 ausstehend (3 Sicherheit)`; `null` (unbekannt), wenn die Prüfung scheiterte. „Überspringen“ in HA gilt nur,
  bis sich die Menge ändert. Geprüft: HA vergleicht zuerst auf Gleichheit; zwei verschiedene Freitexte kann
  AwesomeVersion nicht vergleichen, dann gilt die Entität als `on`.
- **`on` nur, wenn Vördr etwas installieren würde** ([ADR-0106](../docs/adr/0106-update-entity-on-only-when-vordr-would-install.md),
  seit Leitstelle 0.22.0 und Integration 0.20.0):
  - Die Leitstelle meldet je Host `installable`: so viele ausstehende Updates nähme der Install-Knopf nach der
    Richtlinie des Hosts. Mit Standardumfang `all` alle, mit `security` oder nur dem Knopf für Sicherheitsupdates die
    Sicherheitsupdates, ohne beides keine.
  - Ist es 0, ist die Entität `off`. Ein Host, dessen Richtlinie nur `security` erlaubt, zeigt seine übrigen Updates
    dann nur noch in den Sensoren und der Zusammenfassung.
  - Häufen sie sich, meldet `stale` (aus `pending_since`, alle ausstehenden Updates) das Problem nach
    `stale_after_days` weiterhin.
  - Eine ältere Leitstelle schickt das Feld nicht; dann gilt die alte Regel.
- `release_summary`: die ersten Pakete mit alter → neuer Version (HA erlaubt höchstens 255 Zeichen).
- `in_progress` und `update_percentage` während eines Laufs, Fortschritt aus `APT::Status-Fd`.
- Install-Knopf: Auftrag `install` mit `install.default_scope` des Hosts.

### 8.4 Befehle und Schwellen

Befehle kommen als `command` über `/v1/ha` ([ha-integration.de.md](ha-integration.de.md), Abschnitt 5): `check`, `install`
(Standardumfang), `install_security`, `install_app:<id>`, `reboot`, `agent_update`, dazu die Befehle für pip/npm-Orte
und Container-Images (11.4, 11.5). Die Leitstelle prüft jeden gegen die Verben und die gemeldete Richtlinie des Hosts
und gegen die Rolle des Clients (12), bevor sie einen Auftrag signiert; der Agent prüft trotzdem selbst.

Übernommen aus dem Vorbild-Projekt: Entitäten entstehen und verschwinden nur auf eine vollständige Sicht hin, und
„noch nicht entschieden“ ist etwas anderes als „soll nicht existieren“ (Tri-State, ha-integration.de.md 6a).

`hub.yaml` dazu: `stale_after_days` (Standard 7), `offline_after_min` (Standard 10), `reboot_pending_after_days`
(Standard 7), `no_report_after_hours` (Standard 26). Einen `mqtt`-Abschnitt lehnt `check-config` ab: MQTT gibt es seit
Leitstelle 0.6 nicht mehr ([ADR-0026](../docs/adr/0026-remove-mqtt.md)).

### 8.5 Auf der HA-Seite

- Alle `update`-Entitäten erscheinen ohne weiteres Zutun unter *Einstellungen → Updates*.
- Je Host-Problem ein Reparaturhinweis und das Ereignis `vordr_problem` für Automationen (ha-integration.de.md 6a).
- Noch nicht gebaut: ein Blueprint, der aus `binary_sensor.*_problem` eine Benachrichtigung bzw. einen
  Aufgabeneintrag mit `next_step` macht und alle übrigen `update`-Entitäten (aus Integrationen) auf „hängt seit
  N Tagen“ prüft. Damit gälte 7.5 auch für Geräte ohne Agent.

## 9. Lokale Richtlinie

Beispiel `/etc/vordr/agent.yaml` (Windows: `%ProgramData%\vordr\agent.yaml`):

```yaml
node: example-host                  # Standard: Hostname
hub:
  url: wss://vordr-hub.example/v1/tunnel
  public_key: "<base64>"            # Hub-Schlüssel; Aufträge ohne gültige Signatur werden abgelehnt
  ca_file: /etc/vordr/hub-ca.pem    # optional: nur diesen CAs trauen statt den System-Wurzeln (interne PKI)
install:
  allowed_scopes: [security, all]   # app:<id> ist erlaubt, wenn die App unter apps steht;
                                    # image:<projekt>/<dienst> ("*" erlaubt) für Compose-Dienste (11.5, ADR-0119)
  default_scope: security           # Install-Knopf der Entität "Systempakete"
  auto: hub                         # off | hub (siehe 10.3; local noch nicht gebaut)
  window: {days: [mon, tue, wed, thu, fri, sat, sun], start: "02:00", end: "05:00"}
  min_interval_h: 6
  timeout_min: 60
reboot:
  allowed: false
  window: {days: [sun], start: "03:00", end: "04:00"}
agent_update:
  enabled: true
  public_key: "<base64>"            # Betreiber-Schlüssel
apps:
  - id: gitlab
    name: GitLab
    source: apt-package
    package: gitlab-ce
guest_backup:                       # nur auf dem Hypervisor (10.2, ADR-0096)
  storage: pbs                      # ein Proxmox-Backup-Server-Speicher dieses Hosts (/etc/pve/storage.cfg)
  timeout_min: 60                   # je Backup; 5 bis 240
```

Fenster gelten in der Ortszeit des Hosts. Fehlt ein Schlüssel, gilt der strengere Wert: kein Neustart, kein
automatisches Installieren, nur `check`. `vordr-agent check-config` prüft die Datei, bevor der Dienst sie übernimmt
([ADR-0006](../docs/adr/0006-local-policy-decides.md)).

## 10. Abläufe

### 10.1 Anmeldung und Freigabe

1. Beim ersten Start erzeugt der Agent seine Identität, verbindet sich, signiert die Challenge und schickt `hello`.
2. Der Hub legt den Host als `pending_approval` an; HA zeigt ihn unter „Wartende Freigaben“ mit Fingerprint.
3. Freigegeben wird nur am Hub selbst: `vordr-hub approve <node> --fingerprint <fp>`, nach Abgleich mit
   `vordr-agent identity` am Host. HA kann keine Hosts zulassen
   ([ADR-0010](../docs/adr/0010-administration-only-local.md)).
4. Erst danach entstehen die HA-Entitäten, und der Hub nimmt Meldungen an.

### 10.2 Installationslauf (apt)

1. Auftrag prüfen: Signatur, Host, Ablauf, Wiederholung. Sonst `rejected_invalid_job`.
2. Richtlinie prüfen: Umfang, Fenster, Mindestabstand. Sonst `rejected_*`.
3. Eigene Sperre nehmen; ist die Sperre des Paketmanagers belegt: `rejected_busy`.
4. Vorbedingungen: freier Platz, `dpkg --audit` leer, `apt-get update --error-on=any` erfolgreich.
5. Probelauf (`apt-get -s`), daraus der Plan. Würde er Pakete entfernen: `precheck_would_remove_packages`.
   Ausgenommen sind Pakete, die ein Paket desselben Laufs ausdrücklich ersetzt. Das neue Paket erklärt dafür
   `Replaces` und zusätzlich `Breaks` oder `Conflicts` auf das alte (Debian Policy 7.6.2). So benennen Distributionen
   Pakete um oder teilen sie auf. Beispiel: Raspberry Pi OS ersetzte 2026 `pcmanfm` durch `pcmanfm-pi`; ohne diese Ausnahme
   blieben auf einem Pi deshalb Hunderte Updates liegen ([ADR-0042](../docs/adr/0042-replaced-packages-are-no-removal.md)).
   Ebenfalls ausgenommen ist eine alte Bibliothek, die der Lauf zurücklässt, wenn eine Bibliothek auf einen neuen
   Soname wechselt: Sie wurde automatisch installiert, ein Paket des Laufs stammt aus demselben Quellpaket, und jedes
   installierte Paket, das von ihr abhängt, wechselt im selben Lauf auf eine Fassung ohne sie
   ([ADR-0146](../docs/adr/0146-an-old-library-the-run-supersedes-is-no-removal.md)).
   - Der Lauf nennt solche Ersetzungen in der Zusammenfassung (`replaced`).
   - Können `apt-cache`, `apt-mark` oder `dpkg-query` nichts dazu sagen, zählt jede Entfernung als echte.
   - Beim Umfang `all` `full-upgrade`, ausser `install.mode` sagt `upgrade`
     ([ADR-0076](../docs/adr/0076-full-upgrade-by-default.md)).
   - Bei `scope=security` nur Pakete, deren Kandidat aus einer Sicherheitsquelle stammt
     (`apt-get install --only-upgrade` mit den Namen aus dem eigenen Probelauf). Ihr Plan darf nur ändern, was aus
     einem Sicherheitsarchiv oder aus der Release des Hosts selbst kommt, wie unattended-upgrades
     ([ADR-0116](../docs/adr/0116-security-runs-stay-within-security-archives-and-the-release.md)).
     - Release des Hosts: Label `Debian` oder `Ubuntu`, Suite ohne Bindestrich, Version gleich `VERSION_ID` aus
       `/etc/os-release` oder eine Zwischenversion davon (`12.15` auf Debian 12). Ist die Release unbekannt, zählen
       nur die Sicherheitsarchive.
     - Nicht aus `-updates`, `-backports`, `-proposed`, einer anderen Release oder einem Hersteller-Repo, auch wenn
       dessen Suite `stable` heisst.
     - Erst alle zusammen. Reicht dieser Plan weiter, einzeln in der Reihenfolge des Probelaufs: Ein Update bleibt,
       wenn der Plan mit den bisher behaltenen und ihm innerhalb bleibt.
     - Zurückgehaltene bleiben ausstehend. Das Ergebnis ist `ok`, und `detail` nennt sie mit dem, was sie bräuchten.
       Hält der Lauf alles zurück, lädt er nichts und schreibt kein Journal. Läuft die Zeit beim Auswählen ab, endet
       er mit `timeout`.
6. Herunterladen (`apt-get -d`), dann das Laufjournal schreiben (`/var/lib/vordr/run.json`), bevor ein Paket
   angefasst wird. Der Download füllt nur den Paket-Cache. Scheitert er (Spiegel nicht erreichbar, apt belegt), hat der
   Lauf nichts geändert, schreibt kein Journal und zählt nicht für `install.min_interval_h`. Ein Aussetzer des Spiegels
   kostet so nicht das ganze Fenster. Unter Windows Update laufen Download und Installation in einem Skript, dort steht das
   Journal weiter davor.
7. Ausführen mit `DEBIAN_FRONTEND=noninteractive` und `--force-confold`, Fortschritt über `APT::Status-Fd`.
8. Nachprüfen: ausstehende Updates im Umfang, `dpkg --audit`, Neustartbedarf.
9. Ergebniscode melden, Journal schliessen.

**Backup-Prüfung vor dem Lauf (Leitstelle, optional):** Hat `hub.yaml` einen Abschnitt `backup`, fragt die
Leitstelle vor jedem `install` den Backup-Server nach dem letzten Backup der Gruppe des Hosts (erste Umsetzung:
Proxmox Backup Server, PBS; eigenes
Lese-Token, nur `DatastoreAudit`, Zertifikat per Fingerprint gepinnt). Ist es älter als `max_age_hours` (Standard 26),
fehlt es, antwortet PBS nicht (ein Abruf jünger als 30 min zählt noch) oder nennt `hub.yaml` keine Gruppe, lehnt sie
mit `backup_stale`, `backup_missing`, `backup_unreachable` bzw. `backup_unmapped` ab, bevor ein Auftrag hinausgeht.
Hosts ohne Backup stehen ausdrücklich mit Grund drin (`without`). Prüfen statt Zeitplan: ein gescheitertes oder
verspätetes Backup hält die Installation an. HA zeigt je Host den Sensor „Letztes Backup“ und die Ablehnung als
Fehler mit Alter und Grenze ([ADR-0030](../docs/adr/0030-backup-required.md)).

**Konfigurationssicherung statt PBS (Appliances):** Ein Eintrag `config_backup: true` statt `group` oder `without`
gilt für eine Appliance hinter einem Stellvertreter. Die Leitstelle nimmt dann das Alter aus dem Fakt
`config_backup_at`, den der Agent in seinem letzten Bericht unter der Quelle `config-backup` meldet. PBS fragt sie dafür
nicht. Es gelten dieselbe Grenze und dieselben Codes `backup_stale` und `backup_missing`. Der Sensor „Letztes Backup“
zeigt diese Sicherung ([ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md),
[Stellvertreter 4.4](proxy-agent.de.md#44-konfigurationssicherung-adr-0090)).

**Backup direkt vor dem Install (Leitstelle und Agent des Hypervisors; erste Umsetzung: Proxmox VE mit PBS):** Mit
`backup.before_install.hypervisor: <node>` bekommt jeder Gast mit einer Gruppe `vm/<id>` oder `ct/<id>` vor jedem
Install ein frisches Backup; Gruppen `host/` und `without` bleiben bei der Altersprüfung
([ADR-0096](../docs/adr/0096-fresh-backup-right-before-the-install.md)).

- **Vor dem Lauf:** Das Alter des letzten Backups hält einen solchen Gast nicht mehr auf. Die Leitstelle lehnt aber ab,
  wenn PBS nicht antwortet (`backup_unreachable`) oder der Agent des Hypervisors nicht da ist, nicht freigegeben ist
  oder `backup` nicht anbietet (`backup_host_unavailable`).
- **Lauf:** Der Install-Lauf beginnt in der Phase `backing_up`, mit seiner endgültigen `job_id`. Die Leitstelle
  schickt dem Hypervisor den signierten Auftrag `backup` mit dem Gast und wartet auf das Ergebnis. Danach fragt sie
  PBS mit dem Lese-Token, ob die Gruppe ein Backup hat, das nicht älter ist als der Moment des Auftrags (1 min
  Uhrabweichung). Erst dann geht der Install mit derselben `job_id` hinaus.
- **Erneut entscheiden:** Das Warten auf das Backup kann dauern: andere Gäste werden vorher gesichert, das Backup
  selbst braucht Minuten. Zwei Mal prüft die Leitstelle deshalb erneut, was sie beim Start geprüft hat (Fenster nach
  [ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md), Mindestabstand nach
  [ADR-0110](../docs/adr/0110-the-hub-checks-the-minimum-interval-before-the-backup.md)): wenn das Backup an der Reihe
  ist, und bevor der Install hinausgeht.
  - Ein Install aus HA ohne Grund bekommt dann den Grund des Knopfs, wenn das Fenster inzwischen zu ist.
  - Jeder andere Install ohne Grund endet mit `rejected_outside_window`, ohne dass er gesendet wird. Ebenso einer, den
    der Mindestabstand aufhält (`rejected_rate_limited`).
  - Schliesst das Fenster vor dem Backup, wird nichts gesichert.
- **Wiederverwenden:** Ist das letzte Backup des Gastes jünger als 15 min (der geplante Job lief eben, derselbe Install
  wird wiederholt), nimmt die Leitstelle dieses.
- **Eins nach dem anderen:** Es läuft immer nur ein Backup. Der Agent führt einen Auftrag zur Zeit aus, und vzdump hat
  eine Sperre je Host. Läuft auf dem Hypervisor ein eigener Lauf, wartet das Backup, bei `rejected_busy` bis zu drei
  Versuche. Befehle an den Hypervisor warten, solange er sichert (Warteschlange 10.3).
- **Kein Lauf des Hypervisors:** Sein „Letzter Lauf“ in HA bleibt sein eigener. Das Ergebnis steht im Lauf des Gastes
  und im Ereignisprotokoll (`install_awaits_backup`, `job_sent` mit `guest`, `for`, `for_job`, `pre_install_backup`).
- **Scheitert es** (vzdump, PBS hat das Backup nicht, Hypervisor weg, mehr als 5 h Wartezeit), endet der Lauf mit
  `failed_backup` und dem Grund im Detail. Installiert wurde nichts.
- **Agent des Hypervisors** (`guest_backup` in der `agent.yaml`, Kapitel 9):
  - Er bietet `backup` nur unter Linux als root an, mit `vzdump` und einem `pbs:`-Speicher in `/etc/pve/storage.cfg`.
    `check-config` prüft den Speicher.
  - Er sichert nur Gäste dieses Hosts (Konfigurationsdatei unter `/etc/pve/lxc` bzw. `/etc/pve/qemu-server`) und
    jeden höchstens einmal in 10 min (`rejected_rate_limited`).
  - Aufruf: `vzdump <id> --storage <speicher> --mode snapshot --remove 0 --lockwait <timeout_min/2>
    --notes-template "vordr: before install, job <job_id>"`. Snapshot-Modus fällt bei Speichern ohne Snapshots von
    selbst auf Suspend zurück, nie auf Stop. `--remove 0`: Vördr löscht nie ein Backup; die Aufbewahrung regeln die
    Prune-Jobs von PBS.
  - **Umask 022** wie beim Scheduler von Proxmox: Der Agent startet `vzdump` über eine feste `sh`-Zeile
    (`umask 022 && exec "$0" "$@"`; die Argumente werden nie Teil des Skripts). Die Unit des Agenten hat `UMask=0077`,
    und `vzdump` legt sein Temp-Verzeichnis und darin die `pct.conf` mit der geerbten Umask an. Bei unprivilegierten
    Containern liest `proxmox-backup-client` sie als gemappter root (uid 100000). Unter 0077 scheitert das mit
    „Permission denied“.
  - Er hält seine Sperre wie ein Install. Ein Backup bricht nicht ab, wenn der Agent stoppt; das Protokoll liegt unter
    `<state_dir>/runs/<job>.log`, und ein gescheitertes Backup nennt diesen Pfad im Detail.
- **Neustart der Leitstelle:** Ein Lauf, der beim Neustart noch in `backing_up` steht, endet mit `interrupted`. Der
  Auftrag ging nie hinaus.

`vordr-hub backup <node>` zeigt die Entscheidung mit „a fresh backup by <hypervisor> before the install“. Synology
Active Backup for Business wurde dafür geprüft und verworfen
([ADR-0097](../docs/adr/0097-synology-active-backup-for-business-for-backups-before-installs.md)).

**Festlegungen zum Lauf:**

- **Fenster:** Ist `install.window` gesetzt, gilt es auch für den Knopf in HA. Ohne Fenster darf der Knopf jederzeit;
  automatisch (`auto: hub`) geht nur mit Fenster. Ausserhalb des Fensters nur mit Grund (6.4).
- **Fortschritt:** Download erst getrennt (`apt-get -d`), dann die Installation. Prozent des ganzen Laufs:
  Vorprüfung bis 5, Download bis 35 (`dlstatus`), Installation bis 95 (`pmstatus`, mit Paket), Nachprüfung bis 100.
  Höchstens eine Meldung pro Sekunde und Phase; der Hub legt Phase, Prozent und Paket in den letzten Lauf, HA zeigt
  sie in `update.*_system` (`update_percentage`) und am Sensor „Letzter Lauf“ (`progress`, `item`).
- **Fristen:** `install.timeout_min` gilt bis zum Beginn der Installation. dpkg wird nie mittendrin gestoppt; die
  Installation selbst endet spätestens nach der doppelten Frist mit `timeout`.
- **Tunnel und Neustart:** Ein Lauf gehört nicht zur Verbindung, die ihn brachte. Fortschritt und Ergebnis gehen über
  die jeweils aktuelle Verbindung, sonst aus dem Journal bei der nächsten. Ein Ergebnis, das nach einem Tunnelabbruch
  kommt, löst beim Hub das vorläufige `interrupted` ab. Ein Stopp des Dienstes wartet auf den Lauf (`KillMode=mixed`,
  `TimeoutStopSec=30min`; [ADR-0029](../docs/adr/0029-runs-survive-disconnects.md)).
- **Handschlag zu Läufen** (Leitstelle 0.26.0, Agent 0.15.0;
  [ADR-0114](../docs/adr/0114-agent-names-its-run-and-the-hub-acknowledges-results.md)): Beim Tunnelabbruch muss die
  Leitstelle raten, was aus dem Lauf wurde. Seit dem Handschlag sagt es der Agent beim nächsten Hello.
  - Die Leitstelle kündigt `ack` schon in der Challenge an (`features`), weil das Hello vor dem Status kommt und eine
    ältere Leitstelle ein Hello mit unbekanntem Feld ablehnt. Nur dann schickt der Agent `runs`.
  - `runs.running`: die Aufträge, die der Agent gerade ausführt (vom geprüften Auftrag bis zum gesendeten Ergebnis;
    ein Neustart des Hosts, bis er wirklich herunterfährt). `runs.unacknowledged`: Ergebnisse, die er hält und auf
    dieser Verbindung schickt – das Journal, bis die Leitstelle es quittiert hat, ein Selbst-Update und ein Neustart
    nach dem Start.
  - Die Leitstelle gleicht ihren letzten Lauf damit ab, bevor ein Auftrag den Host erreichen kann: Nennt der Agent
    ihn als laufend, läuft er wieder (Ereignis `run_continues`), und Warteschlange, Verteilung und Automatik warten.
    Nennt er ihn gar nicht, ist der Lauf weg: `interrupted` mit „the agent reported no such run…“ (`run_lost`), und
    der Host ist sofort frei statt erst nach der Frist des Auftrags. Ein Install, der noch auf das Backup seines
    Gastes wartet (`backing_up`), bleibt, wie er ist: Er ging noch nicht hinaus.
  - Jeder als laufend genannte Auftrag hält den Host besetzt, bis sein Ergebnis kommt, auch einer, der kein eigener
    Lauf des Hosts ist (das Backup eines Gastes auf dem Hypervisor).
  - Ein Endstatus ohne Journal, den keine Verbindung nimmt, bleibt im Speicher des Agenten, steht im nächsten Hello
    und geht auf der nächsten freigegebenen Verbindung hinaus.
  - Endet die alte Verbindung bei der Leitstelle erst nach dem neuen Hello, wartet der Auftrag weiter
    (`job_continues`), statt `interrupted` zu verbuchen.
  - Jeden Endstatus speichert die Leitstelle und quittiert ihn dann mit `ack` (Auftrags-ID). Erst damit gilt das
    Ergebnis im Journal als gemeldet (`acknowledged`), sonst schickt der Agent es bei jeder neuen Verbindung wieder.
    Eine zweite Kopie ändert nichts (`job_status_again`) und wird wieder quittiert. Ebenso der Neustart-Marker.
  - Das Selbst-Update bleibt bei seiner Regel: Der Marker verschwindet, sobald das Ergebnis auf der ersten
    freigegebenen Verbindung gesendet ist; das Verschwinden ist die Bestätigung für den Wächter (ADR-0098).
  - Ältere Agenten schicken kein `runs` und bekommen kein `ack`; mit einer älteren Leitstelle gilt ein gesendetes
    Ergebnis wie bisher als gemeldet. Ein Journal von vorher (`reported`) gilt als nicht quittiert und geht einmal
    mehr hinaus.
- **Was der Lauf getan hat:** Das Ergebnis eines Laufs, der Pakete
  angefasst hat, trägt eine Zusammenfassung – Paketzahl und -namen (höchstens 100), Dauer, Pfad des Laufprotokolls am
  Host (`<state_dir>/runs/<job>.log`, die ganze Ausgabe von apt, die letzten 10 Läufe) und die Dienste, die laut
  `needrestart -b` noch alte Bibliotheken nutzen. HA zeigt sie am Sensor „Letzter Lauf“ und im Ereignis
  `vordr_run_finished` (`summary`). Phasen, Paketzahl und Dauer stehen auch im Journal des Agenten.
- **Umfang `all`:** `install.mode` (`upgrade` oder `full-upgrade`) übersteuert die Voreinstellung `full-upgrade`.
  Mit `upgrade` bleiben Pakete liegen, die neue Abhängigkeiten brauchen (unter Ubuntu typisch Kernel und
  `linux-firmware`); ein Lauf, der Pakete entfernen würde, wird in beiden Fällen abgelehnt
  ([ADR-0031](../docs/adr/0031-install-mode-full-upgrade.md), abgelöst durch
  [ADR-0076](../docs/adr/0076-full-upgrade-by-default.md)). Docker-Pakete werden nicht zurückgehalten
  ([ADR-0077](../docs/adr/0077-hold-docker-packages.md)).
  - Mit `upgrade` simuliert der Check zusätzlich `apt-get -s upgrade` und markiert, was nur `full-upgrade` nähme, als
    `kept_back` (je Paket und gezählt je Quelle). Es bleibt ausstehend, zählt aber nicht als installierbar für den
    Umfang `all`: Die Leitstelle zieht es von `installable` ab und meldet `kept_back`, und die Automatik mit
    `auto_scope: all` startet dafür keinen Lauf. Der Umfang `security` installiert nach Namen und ist nicht betroffen.
    Schlägt die zweite Simulation fehl, wird nichts markiert
    ([ADR-0127](../docs/adr/0127-count-only-what-the-upgrade-mode-installs.md)).
- Nach einem gelungenen Lauf leert der Agent den Paket-Cache (`apt-get clean`).
- **Prüfung nach dem Update** (`health:` in `agent.yaml`;
  [ADR-0038](../docs/adr/0038-health-checks-after-install.md),
  [ADR-0061](../docs/adr/0061-restart-containers-after-install.md)):
  - Je Eintrag eine Art: `systemd` (Unit aktiv), `docker` (Container läuft, und ist gesund, wo er einen Healthcheck
    hat) oder `http` (GET mit 2xx/3xx in 10 s; das Zertifikat wird nicht geprüft, lokale Dienste).
  - Nach einem Lauf, der Pakete geändert hat und mit `ok`/`ok_reboot_required` endet, wiederholt der Agent die
    Prüfungen bis zu 3 Minuten lang alle 15 s; ein Upgrade startet Dienste neu.
  - Schlägt danach noch eine fehl, endet der Lauf mit `failed_health`, mit der fehlgeschlagenen Prüfung im Auszug.
  - Namen sind nur Namen (Muster ohne Optionen und Shell); sie werden zu Befehlsargumenten.
  - Container: Vor einem Install der Systempakete merkt sich der Agent die laufenden Container mit Neustart-Richtlinie
    (`always`, `unless-stopped`) und startet danach die, die nicht laufen, vor den Prüfungen (ADR-0061). Die Liste
    steht auch im Laufjournal: Wird der Lauf abgebrochen (Agent oder Host gestoppt), holt der nächste Start des
    Agenten diese Container zurück, bevor er sich meldet; das Ergebnis `interrupted` nennt sie (`restarted`) und die,
    die nicht starten ([ADR-0125](../docs/adr/0125-bring-back-containers-after-an-interrupted-run.md)).
- **Neustart** (Verb `reboot`; [ADR-0041](../docs/adr/0041-reboot-with-window-and-proof.md)):
  - Nur mit `reboot.allowed` und im `reboot.window` (ausserhalb nur mit Grund, 6.4), nur wenn der Host ihn wirklich
    braucht (dieselben Signale wie der Bericht: `reboot-required`, laufender gegen neuesten Kernel), und nur, wenn
    sonst nichts läuft.
  - Höchstens einer pro Stunde, gegen Neustart-Schleifen.
  - Vor dem Neustart schreibt der Agent `reboot.json` mit der Boot-Kennung (`/proc/sys/kernel/random/boot_id`) und
    meldet die Phase `rebooting`. Die Leitstelle wartet deshalb auf ihn, statt `interrupted` zu melden.
  - Der Agent nach dem Start belegt den Neustart mit einer neuen Boot-Kennung und prüft, dass keiner mehr nötig ist.
    Erst dann meldet er `ok`; sonst `failed_verification`, mit dem Grund.
  - HA bietet den Knopf nur bei Hosts, die es erlauben. Die Rolle des Clients muss `admin` sein (12).
  - Containern (LXC) bringt ein Neustart nichts. Sie teilen den Kernel des Hosts, und der Agent sieht dort meist
    keinen Bedarf.
- **Neustart-Hinweise** folgen der Fähigkeit des Hosts: Erlaubt er keinen Neustart über Vördr, heisst der nächste
  Schritt bei `ok_reboot_required` und `reboot_pending` „von Hand neu starten“. HA zeigt laufenden und neuesten Kernel
  am Sensor „Neustart nötig“.
- **Mindestabstand** `install.min_interval_h` zählt ab dem Journal, also nur Läufe, die Pakete angefasst haben. Er
  hält Läufe der Systempakete und von Apps zurück, nicht pip/npm-Läufe und das Auffrischen von Abhängigkeiten (die
  zählen aber mit). Auch die Leitstelle kennt ihn
  ([ADR-0110](../docs/adr/0110-the-hub-checks-the-minimum-interval-before-the-backup.md)):
  - Der Agent meldet `policy.min_interval_h` im Hello, dazu `last_install` (Start des Laufs im Journal) und mit dem
    Ergebnis jedes Laufs, der ins Journal schrieb, `install_started`. Die Leitstelle hält das als `last_install` im
    Register.
  - Liegt `last_install` plus Mindestabstand noch vor uns, lehnt sie einen solchen Install mit `rate_limited` ab:
    bevor sie ihn schickt und bevor sie den Gast sichern lässt, auf jedem Weg. Die Automatik zählt den Host dann
    noch nicht als fällig; ihr Versuch für das Fenster bleibt.
  - `HostView.next_install` sagt, ab wann es wieder geht. HA nennt die Uhrzeit in der Ablehnung.
  - Ältere Agenten melden keinen Mindestabstand; dann prüft nur der Agent. Die Leitstelle liest das Hello streng,
    darum kommt sie vor den Agenten.
- **Plattenlast:** Die Unit gibt dem Agenten die IO-Klasse `idle`, gut für Prüfungen. Paketläufe laufen mit
  `ionice -c2 -n7`, sonst käme dpkg auf einem Host mit dauernder Plattenlast kaum voran.
- Nach dem Lauf schickt der Agent sofort einen neuen Bericht, damit die Zähler in HA dem Ergebnis vorangehen.
- `install-agent.sh --replace-config "<grund>"` ersetzt eine geänderte `agent.yaml` (Sicherung, Diff, Grund ins
  Systemlog), wie bei der Leitstelle. Unter Windows gilt dasselbe für `install-service` (11.1).

### 10.3 Automatisch einspielen

`install.auto` legt fest, wer einen Lauf auslöst ([ADR-0036](../docs/adr/0036-automatic-install-in-rings.md)):

- `hub`: Der Hub plant Läufe in Ringen. Ring `canary` kommt zuerst, die übrigen Ringe erst, wenn der Kanarienvogel
  `canary_clean_h` Stunden ohne Problem läuft. Ist der Hub weg, wird nicht installiert; HA sieht dann den Hub offline.
  Unter Windows lehnt die Richtlinie `hub` ab: dort nur per Knopf
  ([ADR-0086](../docs/adr/0086-windows-update-source.md)).
- `local`: Der Agent löst selbst im Fenster aus, auch ohne Hub (für Hosts, die unabhängig patchen sollen). Ergebnisse
  meldet er nach, sobald der Hub erreichbar ist. **Noch nicht gebaut:** Bis es das gibt, lehnt die Richtlinie den Wert
  ab (`check-config`, Start des Agenten), statt ihn still anzunehmen
  ([ADR-0074](../docs/adr/0074-refuse-auto-local-until-built.md)).
- `off`: nur auf Knopfdruck.

Läuft auf dem Host zusätzlich `unattended-upgrades`, meldet der Agent das als Fakt. Beide konkurrieren um die Sperre
(`rejected_busy`). Vördr empfiehlt, auf jedem Host mit `auto: hub` unattended-upgrades abzuschalten
([ADR-0080](../docs/adr/0080-security-updates-everywhere-unattended-upgrades-off.md)), schaltet aber selbst nichts um.
Den Hypervisor nur per Knopf aktualisieren und nie über Vördr neu starten
([ADR-0088](../docs/adr/0088-hypervisor-updates-by-button-only.md)). Auf Linux-Gästen, deren Richtlinie `all`
erlaubt, nimmt die Automatik alles statt nur Sicherheitsupdates
([ADR-0108](../docs/adr/0108-automatic-installs-take-every-update-on-linux-guests.md)). Als
Sicherheitsupdate gilt nur, was aus einem `-security`-Archiv kommt, Hersteller-Repos nie. Darum bleibt ein Host, der
nur `security` erlaubt, bei `security`.

**Festlegungen zu `auto: hub`:**

- **Beide Seiten müssen es wollen:** Der Agent sagt `install.auto: hub` (mit Fenster und `default_scope`), die
  Leitstelle hat einen Abschnitt `auto_install`. Fehlt eines, startet sie nichts von selbst.
- **Umfang der Automatik** ([ADR-0059](../docs/adr/0059-own-auto-scope.md)):
  `install.auto_scope` (`security` oder `all`), ohne Angabe `default_scope`.
  So installiert ein Host von selbst nur Sicherheitsupdates, während der Knopf in HA weiter alles installiert. Agenten
  vor 0.8.2 melden das Feld nicht; für sie gilt `default_scope` wie bisher.
- **Fenster und Zeitzone kommen vom Agenten:** Er meldet sein `install.window` und seine Zeitzone im Hello, als
  IANA-Name: unter Linux aus `/etc/timezone` oder dem Link `/etc/localtime`, unter Windows aus der Windows-Zonen-ID in
  der Registry über die CLDR-Tabelle ([ADR-0112](../docs/adr/0112-windows-time-zone-through-the-cldr-table.md)). Die Leitstelle plant damit, statt zu fragen und
  `rejected_outside_window` zu sammeln. Es bleibt eine einzige Quelle, und der Agent prüft das Fenster weiterhin
  selbst. Agenten vor 0.7.4 melden kein Fenster und nehmen deshalb nicht teil (Ereignis `auto_install_unavailable`,
  Grund `no_window`).
- **Wann:**
  - Alle 5 Minuten prüft die Leitstelle je Host: im Fenster (in der Zeitzone des Hosts), verbunden, kein Lauf offen,
    etwas zu tun (`security`: Sicherheitsupdates ausstehend, `all`: irgendetwas ausstehend).
  - Höchstens ein Versuch je Host in 20 Stunden, also einer je Fenster, was auch daraus wird.
  - Vorher läuft die Backup-Prüfung. Ist das Backup nicht frisch, folgt nur das Ereignis `auto_install_skipped`.
- **Ringe** (Kanarienvögel: [ADR-0058](../docs/adr/0058-site-decision.md)):
  - Hosts in `auto_install.canary` installieren, sobald ihr Fenster offen ist.
  - Die anderen nur, solange jeder Kanarienvogel ohne Problem ist. Ein gescheiterter Lauf, `offline` oder `stale` hält
    sie an.
  - Und nur, wenn jedes ausstehende Update ihres automatischen Umfangs entweder seit `delay_hours` (Standard 24)
    aussteht oder vor dem Beginn der letzten sauberen Installation eines Kanarienvogels zum ersten Mal gemeldet wurde
    ([ADR-0107](../docs/adr/0107-ring-delay-per-update-ended-by-a-clean-canary-run.md), seit Leitstelle 0.22.0).
    Sauber heisst: Verb `install`, Umfang `security` oder `all`, Ergebnis `ok` oder `ok_reboot_required`, und der Lauf
    hat Pakete geändert (seine Zusammenfassung nennt mindestens eines). Ein Lauf, der nichts zu tun fand, beendet keine
    Wartezeit. Das passiert, wenn im Umfang nichts aussteht, oder bei `install.mode: upgrade`, das einen neuen Kernel
    in jedem Fenster liegen lässt.
  - Ein sauberer Lauf trifft nur, was er nicht liegen liess
    ([ADR-0118](../docs/adr/0118-a-canary-run-meets-only-what-it-installed.md)).
    - Er zählt erst, wenn ein vollständiger Bericht des Kanarienvogels nach dem Ende des Laufs eingegangen ist.
      Das Register führt dazu das Ende der letzten sauberen Installation (`clean_install_end`) und den Empfang des
      vollständigen Berichts hinter der Liste ausstehender Updates (`pending_at`).
    - Was dieser Bericht noch als ausstehend nennt (Quelle und Paket), hat der Lauf nicht installiert:
      zurückgehalten (ADR-0116), ausserhalb seines Umfangs, von apt zurückgestellt oder erst jetzt beim
      Kanarienvogel angekommen. Für diese Updates endet keine Wartezeit.
  - Gemessen wird je Update: Das Register führt je Host die ausstehenden Updates des letzten vollständigen Berichts
    (Quelle, Paket, Zielversion, Sicherheit, erstmals gemeldet) und den Beginn der letzten sauberen Installation.
    Eine neue Zielversion ist ein neues Update.
  - „Erstmals gemeldet“ ist der spätere Zeitpunkt aus der Prüfzeit des Agenten und dem Empfang an der Leitstelle. Ein
    Host, dessen Uhr nachgeht, etwa ein Board ohne Echtzeituhr vor dem Zeitabgleich, datiert neue Updates sonst Tage
    zurück und überspringt die Wartezeit. Eine vorgehende Uhr verlängert sie nur.
  - So folgen die übrigen Hosts dem Kanarienvogel im selben Fenster, Minuten nach seinem Lauf. Ein Host mit
    altem Rückstand nimmt ein neues Update nicht mehr vor dem Kanarienvogel. Hatte kein Kanarienvogel etwas zu tun,
    gilt die Wartezeit.
  - Bis Leitstelle 0.21 galt `pending_since` (seit wann irgendetwas aussteht) für den ganzen Host.
- Der Lauf trägt `by: auto` und erscheint in HA wie jeder andere.

### 10.4 Selbst-Update

Wie im Vorbild-Projekt: Das Manifest (Version, Datei je OS/Arch, SHA-256) signiert der Betreiber ausserhalb der
Leitstelle, der Hub verteilt es. Der Agent prüft **zuerst die Signatur**, dann den Eintrag für sein OS und seine
Architektur, lädt die Binary neben sich, prüft den Hash, lässt die neue Datei `version` sagen, tauscht sie ein und
startet neu (systemd `Restart=always` bzw. Windows-Dienst). Rollout in denselben Ringen wie 10.3 ([ADR-0032](../docs/adr/0032-self-update-signed-by-operator.md)).

**Festlegungen:**

- **Veröffentlichen:** Ein Release besteht aus `manifest.json`, seiner Signatur `manifest.sig` (Kontext
  `vordr/agent-manifest/v1`, Betreiber-Schlüssel) und den Binaries aus `./build.sh`; alles liegt in `<state_dir>/agent`
  der Leitstelle. Signiert wird ausserhalb der Leitstelle, mit `vordr-release sign`
  ([ADR-0139](../docs/adr/0139-the-release-signer-is-part-of-the-repository.md)). Der Betreiber-Schlüssel liegt nie auf der Leitstelle. Die Leitstelle prüft die Signatur ebenfalls
  (`agent_update.public_key` in `hub.yaml`), nur um die gültige Version zu kennen.
- **Auftrag** `agent_update` trägt das Manifest samt Signatur und ein Einmal-Token. Mit dem Token lädt der Agent seine
  Binary über `https://<leitstelle>/v1/agent/<datei>`; das Token gilt 15 Minuten.
- **Agent:** Er prüft das Manifest gegen den Schlüssel in **seiner** `agent.yaml`, dann Hash und Grösse. Probelauf
  der neuen Binary: `version` muss die Manifest-Version sagen, `check-config` die eigene `agent.yaml` annehmen. Dann
  setzt er die Markierung `update.json`, tauscht die Binary (die alte bleibt als `.old`) und beendet sich; systemd
  startet die neue. Das gibt es unter Linux als root und unter Windows als Dienst, jeweils mit `agent_update.enabled`.
- **Unter Windows** ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)):
  - Eine laufende `.exe` lässt sich umbenennen, aber nicht überschreiben. Der Tausch benennt sie in `.old` um und legt
    die neue an ihren Platz; die neue heisst bis dahin `vordr-agent.new.exe`, damit der Probelauf sie starten kann.
  - Neustart ([ADR-0103](../docs/adr/0103-windows-service-restarts-cleanly-after-a-self-update.md)):
    - Danach hält der Agent den Dienst sauber an (Exit-Code 0), und der Update-Wächter startet ihn mit der neuen
      Binary wieder. Ein sauberer Stopp zählt beim Dienstmanager nicht als Fehlschlag.
    - Der Wächter lauscht dafür auf ein benanntes Ereignis des Auftrags (`Global\vordr-update-guard-<job>`), das nur
      besteht, solange er läuft. Der Tausch wartet, bis es da ist; sonst gibt es keinen Tausch.
    - Erreicht der Agent keinen Wächter, beendet er sich mit Code 3, und die Wiederherstellung des Dienstes (von
      `install-service` gesetzt) startet ihn nach 10 s.
    - Warum nicht immer so: Der Dienstmanager zählt Fehlschläge einen Tag lang, und der dritte führt `rollback.cmd`
      aus. Endete jeder Neustart mit Code 3, spränge das dritte Selbst-Update binnen eines Tages zurück. Agenten bis
      0.11.0 starten so neu. Weil immer der laufende Agent tauscht, wirkt der saubere Stopp erst bei einem Update, das
      ein Agent ab 0.11.1 ausführt.
  - Rücksprung nach 10 Minuten ohne Verbindung: Die laufende `.exe` wird zu `.failed`, die alte zurückgelegt, dann
    Neustart wie oben. `.failed` löscht der nächste Start.
  - Startet die neue gar nicht, führt der Dienstmanager beim dritten Fehlschlag statt eines Neustarts `rollback.cmd`
    aus. Das legt die alte Binary nur zurück, solange ein Selbst-Update offen ist, und startet den Dienst in jedem
    Fall. Seit ADR-0103 braucht das drei Abstürze der neuen Binary, nicht mehr den geplanten Neustart und zwei.
  - Die Binary ist nicht mit Authenticode signiert; ihre Echtheit sichert wie unter Linux das Betreiber-Manifest.
- **Bestätigen oder zurück:**
  - Die neue Version meldet das Ergebnis `ok` auf ihrer ersten freigegebenen Verbindung.
  - Gelingt die nicht innerhalb von 10 Minuten, legt sie die alte Binary zurück und beendet sich.
  - Startet die neue gar nicht, macht nach vier gescheiterten Starts `OnFailure` → `vordr-agent-rollback.service`
    (`/usr/local/lib/vordr/rollback.sh`) dasselbe.
  - Hängt die neue, macht der Update-Wächter dasselbe
    ([ADR-0098](../docs/adr/0098-update-guard-rolls-back-a-hung-new-agent.md)):
    - Der alte Agent startet ihn vor dem Tausch aus der eigenen Binary. Unter Linux läuft er als transiente Unit
      `vordr-agent-update-guard.service` (`systemd-run`, `Type=exec`), unter Windows als vom Dienst gelöster Prozess.
      Lässt er sich nicht starten, gibt es keinen Tausch.
    - Er endet, sobald die Markierung weg ist.
    - Ist das Update nach 15 Minuten noch offen und `.old` noch da, legt er `.old` zurück und vermerkt den Grund in der
      Markierung (`rolled_back`). Dann beendet er den hängenden Agenten hart und lässt den Dienst neu starten.
    - Unter Windows setzt das voraus, dass der Dienst in keinem Job-Objekt läuft, das seine Kindprozesse mitbeenden
      würde (prüfbar mit `IsProcessInJob` auf dem Dienstprozess). Dann überlebt der gelöste Wächter das Ende des
      Dienstes, wie ADR-0098 annimmt.
  - Die alte Version meldet danach `failed_verification`, mit dem Grund aus der Markierung, sofern einer drinsteht.
  - Die Leitstelle wertet das Ende der Verbindung nach der Phase `rebooting` nicht als Abbruch: Der Agent hat seinen
    Neustart angekündigt. Der Lauf bleibt offen, bis der nächste Agent das Ergebnis meldet. Kommt bis zum Ende der
    Wartezeit (30 Minuten) keiner, gilt er als `timeout`.
- **Ringe** (Kanarienvögel: [ADR-0058](../docs/adr/0058-site-decision.md)):
  - Zuerst `agent_update.canary`.
  - Die anderen erst, wenn jeder Kanarienvogel die Version seit `canary_clean_hours` (Standard 24) ohne Problem
    fährt. Das Register merkt sich dafür `agent_since`.
  - Ein Fehlversuch wird nach `retry_after_hours` (Standard 6) wiederholt.
  - Der Knopf „Installieren“ an der Update-Entität „Agent“ des Hosts in HA erzwingt das Update je Host sofort.
- **Nur wo eine Binary passt** ([ADR-0102](../docs/adr/0102-offer-an-agent-release-only-where-it-has-a-binary.md)):
  - Der Agent meldet im Hello neben `arch` auch `goos`, also den Bau, unter dem das Manifest seine Binary führt.
  - Hat die Release keine Binary für diese Plattform, bietet die Leitstelle sie dem Host weder im Rollout noch über
    „Installieren“ an. Der Knopf endet mit `no_binary_for_platform`.
  - Die Host-Ansicht trägt dann `agent_release_no_binary`, und HA zeigt dort kein Update an.
  - Ein solcher Kanarienvogel hält die anderen nicht auf. Mindestens ein Kanarienvogel muss die Release aber
    gefahren haben, sonst bleibt der zweite Ring zu.
  - Agenten vor 0.11.0 melden `goos` nicht. Ihnen bietet die Leitstelle die Release wie bisher an, und der Agent lehnt
    selbst mit `rejected_unsupported` ab, wenn seine Binary fehlt.
- `install-agent.sh` richtet Rücksprung-Skript und -Unit ein und räumt eine hängende Markierung weg: Eine
  Handinstallation ist ein Neuanfang.
- Den Tausch macht immer der Agent, der schon läuft. Eine Änderung am Selbst-Update-Pfad wirkt deshalb erst beim Update
  danach. Beispiel: Die Korrektur der Dateirechte kam mit 0.7.2, installiert hat 0.7.2 aber noch 0.7.1 mit `0700`.
  Muss eine Korrektur sofort greifen, geht das nur von Hand (`install-agent.sh`) oder über ein Zwischen-Release.
- Während des Tauschs ist der Host unter Linux etwa 30 s ohne Agenten (`RestartSec` der Unit).

**Nachgeschärft nach einer Sicherheitsdurchsicht:**

- **Nur aufwärts** ([ADR-0034](../docs/adr/0034-only-newer-releases.md)):
  Jedes Manifest, das der Betreiber je signiert hat, bleibt gültig. Ein Agent auf einer Release
  (`X.Y.Z`) nimmt deshalb nur eine neuere. Sonst lehnt er mit `rejected_invalid_job`, Grund `older_release` ab. Sonst
  könnte, wer die Leitstelle übernimmt, ein altes Manifest erneut schicken und damit einen alten Agenten mit bekanntem
  Fehler zurückbringen. Die Verteilung bietet einem neueren Agenten keine ältere Release an. Ein von Hand gebauter
  Agent (Datum oder Zusatz in der Version) nimmt jede Release. Alten Code zurückbringen heisst: ihn unter einer neuen
  Version veröffentlichen.
- **Ersatzschlüssel** ([ADR-0035](../docs/adr/0035-backup-operator-key.md)):
  `agent_update.backup_public_key` in `agent.yaml` und `hub.yaml`. Sein geheimer Teil liegt nur beim Betreiber, nie
  auf der Leitstelle und getrennt vom Betreiber-Schlüssel, so dass beide nicht mit demselben Gerät oder Zugang verloren
  gehen. Ist der Betreiber-Schlüssel verloren oder kompromittiert, erreichen mit dem Ersatz signierte Releases trotzdem
  jeden Host, ohne Besuch am Gerät. Den Eintrag verstehen Agenten
  erst ab 0.7.3: Ältere lehnen die unbekannte Zeile in `agent.yaml` ab.
- **Auslöser:** Jeder Lauf trägt `by` (`ha:<client>`, `admin`, `rollout`), in `job_sent` und in HA.

## 11. Quellen

| Quelle | Plattform | Stand | Sicherheitsupdates erkannt an | Neustart |
|---|---|---|---|---|
| `apt` | Debian, Ubuntu, Raspberry Pi OS, Proxmox VE und Backup Server | gebaut | Ursprung `*-security` | 7.4 |
| `winget` | Windows 10/11 (Programme) | gebaut (11.1) | – (winget kennt keine Einstufung) | Registry, siehe unten |
| `winupdate` | Windows (System) | gebaut (11.1) | Klassifizierung „Security Updates“ | Registry wie `winget` |
| `dnf`, `apk` | RHEL-Familie, Alpine | noch nicht gebaut | Advisory-Typ bzw. – | – |
| Apps `apt-package`, `winget-package` | wie `apt` bzw. `winget` | gebaut: Knopf je App (8.1); eigene Update-Entität noch nicht | – | – |
| Container-Images | Linux-Hosts mit Docker-Engine | gebaut (11.5) | – | – |

Jede Quelle liefert einen Bericht (`source.Report`) mit Zustand, Paketen und Fakten. `source.Presence` sagt `present`,
`absent` oder `unknown`; nur `present` und `absent` lösen Anlegen oder Löschen von Entitäten aus. Im Agenten
(`internal/agent`) stehen dafür zwei schmale Schnittstellen:

```go
type Checker interface {
	Check(ctx context.Context) wire.Report
}

type Installer interface {
	Install(ctx context.Context, req source.InstallRequest, installTimeout time.Duration,
		progress func(source.Progress)) source.InstallResult
	RebootRequired(ctx context.Context) bool
}
```

Plan, Ausführung und Nachprüfung (10.2) liegen in der Quelle hinter `Install`; das Ergebnis trägt immer einen Code
aus 7.3. Windows Update läuft über die COM-Schnittstelle des Windows Update Agent
(`Microsoft.Update.Session`, Suche `IsInstalled=0 and IsHidden=0 and Type='Software'`, `ResultCode` und `HResult` je
Update; Einzelheiten in 11.1 unter „Windows Update“); dafür läuft der Agent unter Windows als LocalSystem.

### 11.1 winget

Programme unter Windows aktualisiert der Agent mit winget (winget vor Windows Update;
[ADR-0048](../docs/adr/0048-winget-before-windows-update.md)). Die Tabelle von
`winget upgrade` ist übersetzt und auf Konsolenbreite gekürzt. Der Agent liest sie deshalb nicht, sondern fragt
Microsofts PowerShell-Modul `Microsoft.WinGet.Client` (dieselbe COM-Schnittstelle wie winget selbst;
[ADR-0050](../docs/adr/0050-winget-via-powershell-module.md)):

- **Check:** `Get-WinGetPackage`, davon die mit `IsUpdateAvailable` (Id, installierte und neueste Version, Quelle).
  Angeheftete Pakete (`winget pin`) zählen nicht. Das Modul hält sich daran wie `winget upgrade`: Ein blockierend
  angeheftetes Paket bleibt draussen, auch wenn der Katalog eine neuere Version hat.
- **Hauptversionssprünge** ([ADR-0052](../docs/adr/0052-major-version-jumps.md)):
  - **Was markiert wird:** Ein Paket, dessen Version mit einer anderen Zahl beginnt, trägt `major` (z. B.
    3.9 → 4.0).
  - **Was nicht:** Kalenderversionen (erste Zahl ab 1000) und Versionen ohne Zahl vorn.
  - **Wo es erscheint:** Die Leitstelle reicht es durch, HA schreibt „neue Hauptversion“ in die Zusammenfassung des
    Update-Eintrags (Integration 0.10.7).
  - **Nur winget markiert:** Die stabilen Updates einer Distribution bleiben in ihrer Hauptversion.
  - **Wer einen Sprung nicht will,** heftet das Paket an (`winget pin add --id <id> --blocking`); „alle“ lässt es
    dann aus.
- **Install:** `Update-WinGetPackage -Mode Silent` je Paket, nacheinander, in einem PowerShell-Prozess. Er meldet je
  Paket eine JSON-Zeile mit winget's eigenem Status (`PSInstallResultStatus`), dem Exit-Code des Installers und
  `RebootRequired`. Daraus:

  | winget | Code |
  |---|---|
  | `Ok`, `NoApplicableUpgrade` | `ok` (mit `RebootRequired` → `ok_reboot_required`) |
  | `DownloadError`, `CatalogError` | `precheck_repo_unreachable` |
  | `InstallError` mit Installer-Code 1618 (anderes MSI läuft) | `rejected_busy` |
  | alles andere | `failed_package_manager`, Status und Codes im Auszug |

  Danach fragt der Agent die Liste neu ab. Steht ein Paket des Laufs noch darin, heisst das `failed_verification`.
- **Vorprüfungen:** Windows Installer beschäftigt (Mutex `Global\_MSIExecute`) → `rejected_busy`. Weniger als
  2 GiB frei auf dem Systemlaufwerk → `precheck_disk_space`, denn winget nennt vorher keine Downloadgrösse.
- **Umfang:** `all` = alles, was winget aktualisieren würde. `app:<id>` = ein Paket aus `apps` mit
  `source: winget-package` und der winget-Id als `package`. `security` findet nichts, weil winget nicht einstuft.
- **Ohne Adminrechte** (Agent im Vordergrund statt als Dienst) nur `app:<id>`. Jeder maschinenweite Installer würde
  sonst per UAC fragen, und niemand antwortet. `all` endet dann mit `rejected_unsupported`. Fakt
  `install_context`: `elevated` oder `user`.
- **Fehlt das Modul,** ist der Check `check_failed` mit Fakt `winget_client: missing` und dem nächsten Schritt im
  Auszug. Die Quelle verschwindet dann nicht stillschweigend.
- **Neustart:** Es gelten die Registry-Schlüssel von Windows Update und Component Based Servicing (`RebootPending`,
  `RebootRequired`), Grund `windows_update`. `PendingFileRenameOperations` zählt nicht: Edge, Office und Treiber
  hinterlassen dort wochenlang Einträge, auf einem gewöhnlichen PC oft über tausend.
- **Dienst** ([ADR-0015](../docs/adr/0015-windows-service-localsystem.md)):
  `vordr-agent install-service` (als Administrator) macht alles in einem Schritt:
  - prüft zuerst alles, was ablehnen kann, und ändert bis dahin nichts (Exit 2): Adminrechte, `agent.yaml`, mit
    `hub-check` die Leitstelle, eine schon installierte `agent.yaml`, Dienstverwaltung und vorhandenen Dienst;
  - ersetzt eine abweichende `agent.yaml` in `%ProgramData%\vordr` nur mit `--replace-config "<grund>"`, wie
    `install-agent.sh` unter Linux: alte Datei als `agent.yaml.bak-<zeit>`, Abweichung angezeigt, Grund ins
    `agent.log` (unter Windows das Log des Agenten, wie unter Linux das Systemlog). Ohne Grund lehnt es ab und nennt
    beide Dateien und den nächsten Schritt;
  - installiert PowerShell 7 (als MSI) und das Modul für alle Benutzer, gibt `%ProgramData%\vordr` und `state_dir` nur
    SYSTEM und Administratoren frei und sichert die alte `agent.yaml`, solange ein vorhandener Dienst noch läuft;
  - hält erst dann den Dienst an, legt die Binary nach `%ProgramFiles%\Vordr` (die vorige bis zum Erfolg als
    `vordr-agent.exe.old`) und `agent.yaml` nach `%ProgramData%\vordr`;
  - richtet den Dienst unter LocalSystem ein (automatischer Start, Neustart nach Absturz) und startet ihn.
  - **Rückweg:** Scheitert ein Schritt nach dem Anhalten, macht es die Schritte rückwärts rückgängig (Dienst,
    Dienstkonfiguration, `agent.yaml`, Binary) und startet den Dienst wieder, wenn er vorher lief. Den Zustand danach
    erfragt es bei der Dienstverwaltung und meldet ihn als Fakt: Exit 3, wenn der Dienst wie vorher ist; Exit 1 nur,
    wenn etwas nicht zurückging (Zeilen `STUCK`).

  **PowerShell 7 ist Pflicht für den Dienst**
  ([ADR-0053](../docs/adr/0053-powershell-7-for-the-service.md)):
  - Als LocalSystem lehnt `Microsoft.WinGet.Client` Windows PowerShell 5.1 ab („This cmdlet is not supported in
    Windows PowerShell“); mit `pwsh` aus `%ProgramFiles%\PowerShell\7` läuft der Check.
  - Der Agent nimmt PowerShell 7 für alle Benutzer, wenn es da ist, sonst 5.1.
  - Läuft er als LocalSystem ohne PowerShell 7, meldet er das vor jedem Aufruf als Fakt
    `winget_client: needs_powershell_7` mit dem nächsten Schritt, und `install` lehnt mit `rejected_unsupported` ab.
  - **Falle:** `winget install Microsoft.PowerShell --scope machine` nimmt sonst das msix-Paket. Das scheitert mit
    0x80070005 und entfernt dabei die Store-Fassung des Benutzers. Darum `--installer-type wix`.

  Log: `<state_dir>\agent.log`.
- **Schlafen ist kein Ausfall** (Leitstelle ab 0.15.0; [ADR-0051](../docs/adr/0051-sleep-is-no-outage.md)):
  Ein PC schläft oder ist aus, ohne dass etwas kaputt ist.
  - **Ankündigung:** Der Dienst bekommt die Ankündigung von Windows (`PBT_APMSUSPEND`, Herunterfahren) und meldet
    sie der Leitstelle als Nachricht `power` (`sleeping`, `shutdown`), bevor die Verbindung abreisst.
  - **Bis der PC sich wieder meldet,** zählt die fehlende Verbindung nicht als `offline`; das gilt für jede neue
    Anmeldung und jede Nachricht. Die Host-Sicht trägt `power`, HA bleibt ohne Problem.
  - **Ohne Ankündigung** (Absturz, Netz weg) gilt die Regel wie bisher.
  - **Rückmeldung statt Raten:** Der Zustand kommt als Meldung vom PC und wird nicht aus der abgerissenen
    Verbindung erraten.
  - **Alte Leitstellen** (vor 0.15.0) kennen `power` nicht und trennen die Verbindung. Darum zuerst die Leitstelle
    erneuern, dann Windows-Agenten anmelden.
- **Zeitzone für `install.window`** (seit Agent 0.15.0,
  [ADR-0112](../docs/adr/0112-windows-time-zone-through-the-cldr-table.md)):
  - Der Agent liest die Windows-Zonen-ID (`TimeZoneKeyName` unter
    `HKLM\SYSTEM\CurrentControlSet\Control\TimeZoneInformation`).
  - Daraus wird über die Tabelle `internal/agent/zone_table.go` (CLDR windowsZones, Gebiet 001) der IANA-Name.
  - Die Tabelle erzeugt `tools/windows_zones.ps1` aus der ICU von .NET.
  - Gemeldet wird die Hauptzone des Gebiets, etwa `America/New_York` für „Eastern Standard Time“, auch für andere Orte
    derselben Windows-Zone. Die Regeln sind dieselben.
  - Ohne Zeitzone bleiben unbekannte IDs und Hosts, auf denen die Sommerzeit abgeschaltet ist. Ältere Agenten melden
    unter Windows keine Zeitzone: Die Leitstelle kann das Fenster dann nicht planen (`autoNoZone`), und der Knopf
    ausserhalb des Fensters bekommt keinen Grund ([ADR-0109](../docs/adr/0109-the-install-button-installs-at-once.md)).
- **Noch nicht unter Windows:** das Neustart-Verb.

#### Windows Update (`winupdate`)

Windows selbst aktualisiert der Agent über die COM-Schnittstelle des Windows Update Agent (`Microsoft.Update.Session`),
in PowerShell wie winget (melden und per Knopf einspielen;
[ADR-0086](../docs/adr/0086-windows-update-source.md)):

- **Check:** Suche `IsInstalled=0 and IsHidden=0 and Type='Software'`, also ohne Treiber. Jedes Update ist ein Paket.
  Der Name ist die KB-Nummer (`KB<nummer>`), ohne KB die Update-ID. `to` trägt den Titel, denn Windows-Updates haben
  keine Version. `security` heisst: Klassifizierung „Security Updates“.
- **Nicht gezählt,** je als Anzahl in einem Fakt:
  - Funktionsupdates (Klassifizierung „Upgrades“, `windows_update_feature_upgrade`). Eine neue Windows-Version ist
    kein Update.
  - Definitionsupdates (Klassifizierung „Definition Updates“, `windows_update_definitions`). Defender holt seine
    Signaturen mehrmals am Tag selbst; gezählt stünde fast immer eines an, und der Host wäre nie aktuell.
  - optionale Updates (`BrowseOnly`, z. B. Vorschau-Updates, `windows_update_optional`). Windows installiert sie
    nicht selbst; sie stünden sonst wochenlang an und lösten `stale` aus.
  - installierte Updates, die auf den Neustart warten (`RebootRequired`, `windows_update_awaiting_reboot`).

  Klassifizierungen erkennt der Agent an ihren festen IDs, nicht an den Namen, die Windows übersetzen kann.
- **Fakten:** `install_context` wie bei winget, `windows_update_search` (`ok`, `failed`, `no_answer`), bei `failed`
  das HRESULT als `windows_update_hresult`, dazu `windows_update_agent` (Version). Eine gescheiterte Suche ist
  `check_failed` mit `failed_windows_update`; antwortet PowerShell gar nicht, `failed_package_manager`.
- **Einspielen nur per Knopf:** `install.auto: hub` lehnt der Agent unter Windows ab (`check-config` wie beim Start).
  Der Agent verbindet beide Quellen zu einem Lauf:
  - `security`: nur Windows Update, nur Klassifizierung „Security Updates“ (winget stuft nicht ein);
  - `all`: erst alle gezählten Windows-Updates, dann winget;
  - `app:<id>`: nur winget.

  Bei `all` laufen beide Teile, gleich wie der erste endet, mit einem Laufjournal. Ergebnis ist der erste Code, der
  nicht `ok` ist, mit Paketen und Auszügen beider Teile; `ok_reboot_required`, wenn einer einen Neustart braucht.
- **Lauf von Windows Update:**
  1. Vorprüfungen: ohne Adminrechte und nicht als LocalSystem `rejected_unsupported`. Windows Installer beschäftigt
     (Mutex wie bei winget) oder `IUpdateInstaller.IsBusy` → `rejected_busy`. Unter 2 GiB frei →
     `precheck_disk_space`.
  2. Neue Suche, daraus der Plan im Umfang. Steht nichts an: `ok` ohne Plan.
  3. Laufjournal. Dann je Update Download (`IUpdateDownloader`) und Installation (`IUpdateInstaller`), eines nach dem
     anderen. Je Update eine JSON-Zeile mit `OperationResultCode`, `HResult` und `RebootRequired`.
  4. Nachprüfung mit einer neuen Suche. Steht ein Update des Plans noch an und wartet nicht auf den Neustart:
     `failed_verification`.

  Fortschritt wie bei apt (10.2): Vorprüfung bis 5, Download bis 35, Installation bis 95, Nachprüfung bis 100.

  | Windows Update | Code |
  |---|---|
  | 2 (erfolgreich), 3 (erfolgreich mit Fehlern) | `ok` (mit `RebootRequired` → `ok_reboot_required`) |
  | 4 (fehlgeschlagen), 5 (abgebrochen), jeder andere Code | `failed_windows_update`, HRESULT im Auszug |
  | Ausnahme eines COM-Aufrufs | `failed_windows_update`, HRESULT im Auszug |
  | nicht mehr angeboten (inzwischen installiert) | `ok` |
- **Lizenzbedingungen:** Der Agent nimmt sie für die Updates seines Plans an (`AcceptEula`), wie ein Klick auf
  „Installieren“ in den Einstellungen. Der Knopf in HA ist die Zustimmung. Jede Annahme steht im Laufprotokoll.
- **Neustart** bleibt von Hand. `reboot_required` kommt weiter aus der Registry (oben).
- **Die automatischen Updates von Windows** laufen weiter. Installieren sie ein Update vor dem Lauf, zählt es als
  erledigt. Hält Windows gerade den Installer, endet der Lauf mit `rejected_busy`.

### 11.2 Pakete von pip, pipx und npm: Bestand und Funde

**Anlass:** apt und winget sehen nicht, was Python- und Node.js-Paketmanager installiert haben. Gerade
dort kommen Lieferkettenangriffe an, meist als bösartige neue Version eines beliebten Pakets: `chalk` 5.6.1 und `debug`
4.4.2 im September 2025, `axios` 0.30.4 und 1.14.1 im April 2026. Vördr meldet deshalb jedes installierte Paket mit bekannter
Schwachstelle und jedes, das als Schadsoftware bekannt ist, als Problem in HA.

Begründungen der Entscheidungen: [ADR-0064](../docs/adr/0064-osv-data-local-at-the-hub.md) (OSV-Daten lokal),
[ADR-0065](../docs/adr/0065-scope-of-package-locations.md) (Umfang),
[ADR-0066](../docs/adr/0066-inventory-from-metadata.md) (Bestand aus Metadaten),
[ADR-0067](../docs/adr/0067-hub-announces-features.md) (Merkmale der Leitstelle),
[ADR-0079](../docs/adr/0079-detect-packages-in-msix-app-containers.md) (Container von MSIX-Apps).

Die Teile:

| Teil | Inhalt |
|---|---|
| 11.2 | Bestand, OSV-Daten an der Leitstelle, Abgleich, Problem in HA |
| 11.3 | veraltete Pakete („neuere Version verfügbar“) |
| 11.4 | Aktualisieren per Knopf, mit Schutz gegen bösartige neue Versionen |
| 11.5 | Container-Images melden (neu gebaut, neuerer Tag), Neubauten per Knopf (ADR-0113, ADR-0119) |

**Bestand (Agent).** Ein *Ort* (`location`) ist ein Platz, an dem ein Paketmanager installiert hat. Er hat eine Art,
ein Ökosystem (`PyPI` oder `npm`, wie OSV sie schreibt) und eine Kennung nach den Regeln der App-Kennungen:

| Art | Was | Gelesen aus |
|---|---|---|
| `npm-global` | `npm install -g` | `package.json` jedes Pakets unter `node_modules`, auch verschachtelt |
| `npm-dir` | ein Ordner mit `package.json` und `node_modules`: Node-REDs Benutzerordner, ein Projekt | wie oben; direkt ist, was `package.json` verlangt |
| `pip-site` | `site-packages` eines Interpreters (pip systemweit oder `--user`) | `*.dist-info/METADATA`, `*.egg-info` |
| `venv` | eine virtuelle Umgebung (`pyvenv.cfg`), auch die Apps von pipx | wie oben |

- **Kein Paketmanager läuft, niemand draussen wird gefragt.** Der Agent liest nur die Metadaten, die pip und npm
  hinterlassen. Das geht als root und als LocalSystem gleich, auch in fremden Benutzerprofilen, und kostet wenig
  (knapp 3000 Pakete unter Windows: etwa 3 s).
- **Bekannte Orte** (`packages.detect`, Standard an):
  - Linux: `/usr/lib/node_modules`, `/usr/local/lib/node_modules`, `/usr/local/lib/python3*/{dist,site}-packages`,
    `~/.local/lib/python3*/site-packages` von root und `/home/*`, pipx (`~/.local/share/pipx/venvs/*`,
    `/opt/pipx/venvs/*`), Node-RED (`~/.node-red`).
  - Windows: je Benutzer npm (`AppData\Roaming\npm\node_modules`), Python (`AppData\Local\Programs\Python\*`),
    `pip --user`, pipx und `.node-red`; dazu `%ProgramFiles%\Python*`. Die Kennung trägt den Benutzer
    (`npm-global-bob`).
  - Windows, **Container von MSIX-Apps** ([ADR-0079](../docs/adr/0079-detect-packages-in-msix-app-containers.md)):
    Was ein Prozess einer MSIX-App (z. B. der PowerShell aus dem Store) nach `AppData` schreibt, liegt unter
    `AppData\Local\Packages\<Paket>\LocalCache`. Der Dienst sieht es am üblichen Pfad nicht. Deshalb liest er dort
    `Roaming\npm\node_modules` und `Roaming\Python\*\site-packages` jeder App sowie das `pip --user` der Python aus dem
    Store (`local-packages\Python*\site-packages`). Die Kennung trägt App und Benutzer
    (`powershell-pip-user-python312-bob`, `store-pip-user-python312-bob`). Ein Lauf des Agenten *in* einer solchen App
    (`vordr-agent packages` aus der Shell einer MSIX-App) sähe diese Pakete doppelt; Prüfläufe deshalb aus einer
    gewöhnlichen PowerShell starten.
  - Leere Ordner sind kein Ort; das leere `/usr/local/lib/python3.11/dist-packages` hat jeder Debian-Host.
  - **Bewusst nicht:** `/usr/lib/python3/dist-packages`. Den Ordner verwaltet apt. Debian portiert Korrekturen zurück,
    ohne die Upstream-Version zu ändern, und OSVs PyPI-Bereiche kennen das nicht: Es gäbe falsche Funde. apt meldet
    diese Pakete schon (Quelle `apt`). Ebenso der npm, den das Node.js-Paket unter `%ProgramFiles%\nodejs` mitbringt
    (Quelle `winget`).
- **Weitere Orte** stehen in `agent.yaml`, `packages.locations`, je mit `id` und absolutem `path`. Der Pfad darf `*`
  enthalten, dann ist jeder Treffer ein Ort mit der Kennung `<id>-<was der Stern traf>`: `/opt/*/venv` mit der `id`
  `services` ergibt für `/opt/router/venv` den Ort `services-router`, `D:\projekte\*` mit `projekte` für ein Projekt
  `D:\projekte\webapp` den Ort `projekte-webapp`. Ein Treffer darf die Umgebung selbst sein oder ein Projekt, das sie
  in `.venv` oder `venv` hat; hat ein Projekt Python und npm, entstehen zwei Orte (`-py`, `-npm`).
- **Je Paket:** Name, Version, `direct` (für sich installiert: oberste Ebene von `npm -g`, in `package.json` verlangt,
  bei Python keine andere Verteilung der Umgebung verlangt es, Extras nicht gezählt) und `via` (bei verschachtelten
  npm-Paketen das oberste Paket, mit dem es kam; das zu aktualisieren hilft).
- **Was ein Paket festhält** (`holds`, [ADR-0129](../docs/adr/0129-findings-say-where-their-fix-is.md)), höchstens
  zwei je Paket:
  - der engste Bereich unter allem, was am Ort davon abhängt: bei npm `dependencies` und `optionalDependencies` der
    installierten Pakete, je für die Kopie, auf die Node.js die Abhängigkeit auflöst (das nächste `node_modules/<name>`
    vom Abhängigen aufwärts); bei Python `Requires-Dist` ohne Umgebungsmarker und ohne Extra. Bei einer Umgebung in
    einem Projektordner (`.venv`, `venv`) auch die Dateien des Projekts: die Version in `uv.lock`, `poetry.lock`,
    `pdm.lock` oder `Pipfile.lock`, die Angabe in `requirements*.txt` (ohne Optionen, Verweise und Marker). Form
    `{by: "<name>@<version>" oder "<datei>", range}`;
  - bei npm das äusserste Paket mit `bundleDependencies` über ihm: Es kam in dessen Tarball, `{by, bundled: true}`.
  - Ein Bereich ohne Obergrenze hält nichts fest und fällt weg, ebenso einer, den der Agent nicht lesen kann (Tag,
    URL, git, Alias). Das `package.json` eines npm-Ordners selbst zählt nicht: Ein Update schreibt es neu.
- **Was nicht passt, bleibt am Host.** Namen und Versionen werden später Argumente von pip und npm (11.4). Nur, was ein
  Register annehmen würde, geht hinaus. Der Rest zählt als `skipped`, etwa ein `package.json` mit
  `"name": "evil; rm -rf /"`.
- **Grenzen:** höchstens 20 000 Pakete je Ort und 3 MiB je Nachricht. Wird es mehr, halbiert der Agent den grössten
  Ort, bis es passt, und der Ort trägt den Code `too_many_packages`. Sonst trennte die Leitstelle die Verbindung (4 MiB
  je Nachricht). Gelinkte Pakete (`npm link`, unter Windows Junctions) liest er, steigt aber nicht hinein: Sie sind
  jemandes Arbeitskopie.
- **Wann:** nach jeder Prüfung (alle 6 h und auf „Jetzt prüfen“), als Nachricht `inventory`, nur an eine Leitstelle mit
  Merkmal `inventory` (6.2). `vordr-agent packages` zeigt am Host genau, was gesendet würde.
- **Standard an:** Der Bestand ändert nichts und fragt niemanden ausserhalb, wie `check`. `packages.inventory: false`
  schaltet ihn ab.

**OSV-Daten (Leitstelle).** Mit dem Abschnitt `osv:` in `hub.yaml` lädt die Leitstelle die ganzen Dumps von osv.dev
(`<url>/<ökosystem>/all.zip`, öffentlicher Bucket, täglich neu) und gleicht selbst ab. **Keine Paketliste eines Hosts
verlässt das Netz**; `api.osv.dev` bekommt keine Anfrage.

- Alle 24 h (`refresh_hours`) fragt sie mit dem ETag der letzten Antwort. Unverändert heisst `304`, eine Anfrage.
- Neu: Die Datei landet in `<state_dir>/osv`. Daraus baut die Leitstelle einen kleinen Index (`<ökosystem>.idx`, gob,
  gzip) und löscht die Datei wieder.
- Scheitert es, bleiben die alten Daten. Die Sicht nennt den Code (`download_failed`, `bad_archive`, `store_failed`,
  `implausible_dump`), neu versucht wird nach einer Stunde. Daten älter als `stale_hours` (Standard 72) heissen
  `stale`, noch keine heisst `missing`. HA zeigt das am Sensor „Schwachstellendaten“.
- **Unglaubwürdige Dumps** ([ADR-0085](../docs/adr/0085-keep-osv-data-when-a-dump-is-implausible.md)) ersetzen die
  Daten nicht: ein leerer Dump oder einer mit weniger als 90 % der Einträge oder der Schadsoftware-Einträge der Daten in
  Gebrauch (`implausible_dump`, beide Zahlen im Log). Sonst würde ein kaputter Dump die Leitstelle für jeden Plan
  bürgen lassen. Die alten Daten melden weiter und werden nach 72 h `stale`; dann bürgt die Leitstelle für nichts mehr.
  Eine gewollte grosse Bereinigung bei OSV übernimmt man, indem man `<ökosystem>.idx` löscht und die Leitstelle neu
  startet.
- Ohne `osv:` bewahrt die Leitstelle Bestände auf, gleicht aber nichts ab. Die Zahlen sind dann unbekannt, nicht null.
- Die Echtheit der Dumps sichert nur TLS zum Bucket; OSV signiert sie nicht.

Grössenordnung (Dumps von Ende Oktober 2026):

| | PyPI | npm |
|---|---|---|
| `all.zip` | 37,5 MB | 219,6 MB |
| entpackt | 81 MB | 389 MB |
| Einträge (ohne zurückgezogene) | 26 124 | 230 207 |
| davon Schadsoftware (`MAL-`) | 11 984 | 222 580 |
| Paketnamen | 13 580 | 225 895 |
| Index auf der Platte | 1,0 MB | 4,6 MB |
| Laden + Index bauen | rund 1 s | rund 5 s |

Heap mit beiden Indizes +112 MB (die Leitstelle allein braucht rund 14 MB). Auf der Platte sind die Spitze 220 MB
während des npm-Downloads, danach bleiben 6 MB. **Ein Container mit 512 MB RAM und wenigen GB freier Platte reicht
dafür.**

**Speicher der Leitstelle:** Ohne Grenze erreicht die Leitstelle beim Bau des npm-Index rund 370 MB RSS, danach
bleiben rund 175 MB. Die Spitze ist Abfall des Baus, nicht der Index. Die Unit setzt deshalb
`GOMEMLIMIT=200MiB`: Der Garbage Collector läuft früher, die Spitze bleibt nahe 200 MiB, ohne Zeitverlust. Es ist
keine harte Grenze; die Leitstelle wird dafür nie beendet. Begründung: [ADR-0073](../docs/adr/0073-soft-memory-limit-for-the-hub.md).

**Abgleich.**
- **Name:** PyPI nach PEP 503 (`Requests` ist `requests`, `zope.interface` ist `zope-interface`), npm genau.
- **Liste:** Steht die Version in der Liste `versions` des Eintrags, ist sie betroffen. Bei PyPI gilt dabei auch
  Gleichwertigkeit (`1.0` ist `1.0.0`).
- **Bereiche** vom Typ `ECOSYSTEM` und `SEMVER` werten wie im OSV-Schema aus:
  - die Ereignisse nach Version sortieren; `introduced: "0"` steht vor allem, bei gleicher Version `introduced` vor
    `fixed`;
  - dann aufwärts: `introduced` ≤ v macht betroffen, `fixed` ≤ v macht unbetroffen, `last_affected` < v ebenso;
  - `limit` begrenzt nach oben.
  - `GIT`-Bereiche nennen Commits und zählen nicht.
  - Daten werden genommen, wie sie dastehen: `dydx-v4-client` hat `introduced 1.1.5.post1`, `fixed 1.1.5`; nach der
    Regel ist damit jede Version ab 1.1.5.post1 betroffen.
- **Reihenfolge der Versionen je Ökosystem:**
  - PyPI nach PEP 440: Epoche zuerst, Release ohne Endnullen (`1.0` = `1.0.0`),
    `.dev` < `a` < `b` < `rc` (= `c`, `pre`) < Release < `.post` (= `-1`, `rev`, `r`), lokale Kennung zuletzt.
  - npm nach SemVer 2.0: Vorabversion vor ihrem Release; numerische Teile als Zahl (jeder Länge) und vor
    alphanumerischen; Build-Metadaten zählen nicht.
- **Was nicht parst, entscheidet nichts.** Ist eine Version (installiert oder im Eintrag) in der Ordnung ihres
  Ökosystems nicht lesbar, ist der Eintrag weder Treffer noch Entwarnung: Er zählt als `undecided` und erscheint in
  HA. Die Liste `versions` bleibt dabei gültig.
- **Schadsoftware** ist ein Eintrag mit `MAL-` als Kennung oder Alias. Zurückgezogene Einträge (`withdrawn`, z. B. ein
  falscher Schadsoftware-Verdacht) zählen nicht.
- **Ergebnis je Host** (`packages` in der Host-Sicht):
  - Orte mit Paketzahl und Code;
  - `malicious` (Pakete mit Schadsoftware-Eintrag) und `vulnerable` (mit anderen Einträgen; Schadsoftware nicht
    doppelt gezählt). Beide `null`, wenn für ein Ökosystem des Hosts Daten fehlen;
  - höchstens 50 Funde, Schadsoftware zuerst, dann nach Bewertung der Quelle (GHSA: CRITICAL … LOW), mit Kennungen,
    Aliasen (CVE, GHSA), `via` und `fixed_in`;
  - `fixed_in` ist die kleinste Version, die alle Einträge des Pakets behebt; leer, wenn einer keine Behebung nennt.
    Bei Schadsoftware bleibt es immer leer: Das Paket gehört entfernt, nicht aktualisiert.
  - **Wo die Behebung liegt** (`reach`, ADR-0129), je verwundbarem Fund. Ein Halt (`holds`) hält das Paket unter
    `fixed_in`, wenn er ein Bündel ist oder sein Bereich keine Version ab `fixed_in` zulässt; `held_by` nennt ihn.
    | `reach` | Bedeutung | Problem |
    |---|---|---|
    | `update` | ein direktes Paket, dessen Kandidat oder Korrektur (ADR-0111) `fixed_in` erreicht, an einem Ort, den HA aktualisieren darf | ja |
    | `update_via` | gehalten, aber ein direktes Paket, das es hält oder mit dem es kam, hat ein Update bereit (es bringt die Behebung vielleicht mit) | ja |
    | `refresh` | nichts hält es; die Abhängigkeiten des Orts lassen sich auffrischen (ADR-0104) | ja |
    | `by_hand` | nichts am Host hält es, aber Vördr aktualisiert dort nicht oder darf nicht | ja |
    | `held` | ein Halt hält es unter `fixed_in`, und nichts darüber hat ein Update bereit | nein |
    | `no_fix` | kein Release behebt alle Einträge | nein |
    | `too_new` | die Behebung ist erschienen, aber jünger als das Mindestalter | nein |
  - `waiting`: wie viele verwundbare Pakete warten (`held`, `no_fix`, `too_new`); gesetzt mit `vulnerable`.
- **Problem:** `malicious_package` steht vor allen anderen Gründen, es ist das Dringendste. `vulnerable_package` steht
  hinten und zählt nur Funde, bei denen jetzt jemand etwas tun kann (`vulnerable` > `waiting`, ADR-0129). Reparaturtext
  und Ereignis `vordr_problem` nennen diese Pakete (bis fünf, `{packages}`). Kommt hinter demselben Grund ein weiteres
  Paket dazu, meldet HA erneut. Wartende Funde stehen am Sensor, mit `reach` und `held_by`.
- Das Ereignisprotokoll der Leitstelle hält vom Bestand nur Zahlen fest, keine Paketlisten.

**Gegenprobe des Abgleichs:**

- Ein Bestand mit knapp 3000 Paketen ergibt eine Nachricht von rund 270 kB, weit unter der Grenze.
- Der Abgleich ist gegen unabhängige Versionsvergleiche geprüft: `packaging` (PyPA) für PyPI, npm's eigenes `semver`
  für npm, dazu eine getrennt geschriebene Bereichsauswertung. Über 1000 Paare aus Paket und Eintrag ergaben
  **keine Abweichung**.
- Die Gegenprobe ist selbst geprüft: Ein absichtlich verfälschtes Ergebnis meldet sie als Abweichung.

**In HA:** Sensoren „Schädliche Pakete“ und „Verwundbare Pakete“ je Host, sobald der Agent einen Bestand schickt. Ältere
Agenten bekommen sie nicht, vorhandene bleiben. Dazu der Diagnosesensor „Schwachstellendaten“ an der Leitstelle.

### 11.3 Veraltete Pakete

Begründung des Mindestalters: [ADR-0068](../docs/adr/0068-minimum-age-for-target-versions.md).

Für jedes direkte Paket (11.2) fragt der Agent das Register, welche Releases es gibt und wann sie erschienen sind. Das
tut er selbst; `packages.outdated: false` schaltet es ab (unten). Hinaus gehen nur Name und Version, wie bei pip und
npm. Die Paketliste geht sonst nirgends hin.

- **Register:** PyPI über `https://pypi.org/pypi/<name>/json`, npm über das volle Dokument
  `https://registry.npmjs.org/<name>` (nur dort steht `time`, die Veröffentlichungszeit je Version). Spiegel gehen über
  `packages.registries`.
- **Gelesen Schlüssel für Schlüssel:** Das npm-Dokument enthält das `package.json` jeder Version, bei `npm` selbst
  Dutzende MB. Als Ganzes eingelesen braucht der Agent dafür rund 90 MB, gestreamt (es liegt immer nur ein Wert im
  Speicher) rund 14 MB. Das zählt auf Hosts mit wenig RAM.
- **Zwei Versionen je Paket:**
  - `latest`: das neueste Release, das ein Update erreichen kann. Stabil, nicht zurückgezogen (PyPI: alle Dateien
    `yanked`; npm: `deprecated`, ausser leer gesetzt). Bei npm nicht über dem Tag `latest`, denn eine höhere Version
    ohne den Tag gehört zu einer anderen Linie. Neuer als das installierte.
  - `candidate`: das neueste solche Release, das seit mindestens `packages.min_age_days` (Standard 7, 1–90,
    abschalten geht nicht) öffentlich ist. **Nur ein Kandidat wird je installiert (11.4).** Ist `latest` jünger,
    nennt `latest_at` den Zeitpunkt; HA kann sagen, ab wann es Kandidat wird.
  - Das Alter eines PyPI-Release zählt ab seiner **neuesten** Datei: Zu einem alten Release kann später ein Wheel
    hochgeladen werden, und genau das wäre ein Angriffsweg.
  - `major`: der Sprung zu `latest` ändert die Hauptversion (wie bei winget, 11.1).
  - `security_candidate` (seit Agent 0.14.0,
    [ADR-0111](../docs/adr/0111-shorter-minimum-age-for-security-fixes.md)): das neueste solche Release, das seit
    `packages.min_age_days_security` (Standard 2, 1 bis `min_age_days`) öffentlich ist, nur wenn es neuer ist als
    `candidate`. Ob es ein Fund behebt, weiss nur die Leitstelle.
- **Wie oft:** höchstens alle `packages.outdated_hours` (Standard 24), höchstens 500 Namen je Lauf, je Name eine
  Anfrage, auch wenn er an mehreren Orten liegt (PyPI-Namen normalisiert). Dazwischen nimmt der Agent die letzten
  Antworten. Ein Paket, dessen Version sich inzwischen geändert hat, wartet auf den nächsten Lauf und behält keine alte
  Antwort.
  - Die letzten Antworten liegen auch im Zustandsordner (`outdated-packages.json`, für die Images
    `outdated-images.json`). Ein Neustart (Selbst-Update, Reboot) fragt deshalb nicht vorzeitig wieder.
  - Es ist nur ein Cache: Fehlt die Datei, ist sie unlesbar, stammt sie von anderen Einstellungen (Mindestalter,
    Registries) oder trägt sie eine Zeit nach jetzt (die Uhr lief zurück), fragt der Agent wie ohne sie.
- **Zustand statt Fehlertext:**
  - Ein Register, das nicht antwortet oder mit Fehler antwortet, zählt am Ort als `outdated_failed`.
  - Kennt das Register das Paket nicht (HTTP 404: aus einer Datei, einem Git-URL, einem privaten Index installiert),
    ist das kein Fehler, sondern `not_in_registry`.
- `packages.outdated: false` schaltet die Abfragen ab. Dann ist `outdated` unbekannt, nicht null.
- **Leitstelle:**
  - zählt `outdated` und listet bis zu 100 Pakete in `updates`; zuerst die, die ein Update jetzt nehmen kann, dann die,
    die warten;
  - prüft jeden Kandidaten gegen die OSV-Daten: `candidate_flagged` nennt die Einträge, die auf den Kandidaten selbst
    zutreffen. Ein Update dorthin lehnt der Agent ab (11.4). Beispiel: `axios` 1.14.1 war Schadsoftware, ein frisches
    Release genau der Art, die das Mindestalter fernhält;
  - setzt `fix` auf den `security_candidate`, wenn die installierte Version einen OSV-Eintrag hat und der
    Sicherheitskandidat keinen (ADR-0111). Ein solches Paket zählt als bereit; HA nennt es „Sicherheitskorrektur,
    kürzere Frist“.
- **Veraltet ist kein Problem:** Der Problemsensor bleibt aus. HA zeigt es am Sensor `sensor.vordr_<id>_outdated_packages`
  „Veraltete Pakete“ mit dem Attribut `updates`.

**Was das Mindestalter bewirkt:** Bei einem guten Teil der veralteten Pakete ist `latest` jünger als 7 Tage; dann ist
der Kandidat das Release davor (z. B. ein Release vom Vortag → Kandidat das vorige), bei manchen gibt es noch gar
keinen. Die Host-Sicht listet höchstens 100 Pakete (`MaxUpdates`), die Zahlen sind vollständig.

### 11.4 Aktualisieren per Knopf

Begründungen: [ADR-0069](../docs/adr/0069-package-updates-by-button-only.md) (nur per Knopf),
[ADR-0070](../docs/adr/0070-hub-vets-and-signs-the-update-plan.md) (signierte Prüfung des Plans),
[ADR-0071](../docs/adr/0071-no-code-during-install.md) (kein Code beim Installieren),
[ADR-0068](../docs/adr/0068-minimum-age-for-target-versions.md) (Mindestalter).

pip- und npm-Pakete lassen sich per Knopf aktualisieren, je Paket oder je Ort, ausgelöst in HA. Lieferkettenangriffe
kommen meist als bösartige neue Version, des Pakets selbst oder einer Abhängigkeit. Darum ist jeder Schritt eine Schranke,
und die Schranken stehen im Agenten (die Leitstelle kann nur anfragen):

| Schutz | Wie |
|---|---|
| **Nur erlaubte Orte** | `packages.update` in `agent.yaml` nennt die Orte (Kennungen, `*` erlaubt). Standard: keiner. Nur `venv` (auch pipx) und npm (`npm-global`, `npm-dir`); pip ausserhalb einer Umgebung bleibt beim System oder beim Benutzer. Das Wartungsfenster (`install.window`) gilt wie für apt, samt der Ausnahme mit Grund (6.4). |
| **Kein automatisches Update** | Es gibt keinen Weg dazu: `install.auto` kennt nur `security` und `all`; die Leitstelle startet Paket-Updates nie selbst. |
| **Mindestalter** | Jedes Ziel ist der Kandidat (11.3), am Lauf neu vom Register erfragt. Die **Abhängigkeiten** auch: npm löst mit `--before <jetzt − min_age_days>` auf, also nur Releases, die damals schon öffentlich waren. pip kennt das nicht verlässlich; der Agent prüft jede Version des Plans selbst am Register (neueste Datei, nicht zurückgezogen). Zu jung → `precheck_version_too_new`. **Ausnahme Sicherheitskorrektur** ([ADR-0111](../docs/adr/0111-shorter-minimum-age-for-security-fixes.md)): Pakete, die die Leitstelle im signierten Auftrag als `security_fix` nennt (direkte Pakete mit `fix`, beim Auffrischen die Abhängigkeiten mit Fund), nimmt der Agent mit `min_age_days_security`. Alles andere im Plan bleibt bei `min_age_days`: npm löst dann mit `--before` der kurzen Frist auf, und der Agent prüft jeden neuen Eintrag am Register. Ein Auftrag für einen ganzen Ort mit Korrekturen und anderen Paketen läuft in zwei Teilen: zuerst die Korrekturen allein mit der kurzen Frist, dann der Rest mit `min_age_days`. Beim Auffrischen gilt die kurze Frist nur für das Release, das an die Stelle einer Kopie mit Fund tritt (gleicher Pfad im Lock). |
| **Ganzer Plan vor jeder Änderung** | pip: `install --dry-run --report`; npm: `install --package-lock-only` in einem leeren Ordner (global) oder in einer Kopie von `package.json` und Lock (Projekt, Node-RED). Der Plan ist alles, was danach installiert wäre und vorher nicht war (Name und Version, nicht der Pfad: npm verschiebt Pakete im Baum). Global gibt es keine alte Lock-Datei; „vorher“ ist dort, was der Ort installiert hat ([ADR-0105](../docs/adr/0105-global-npm-plan-leaves-out-what-is-installed.md), seit Agent 0.12.1). |
| **Nur aus der Registry** | Jeder Eintrag des Plans hat in der Lock-Datei des Plans keine `resolved`-URL (im Paket seines Elternteils gebündelt) oder eine auf der Registry: gleiches Schema, gleicher Host, Pfad darunter. Ein anderer Host, git oder eine Datei → `precheck_not_from_registry`, vor der Prüfung durch die Leitstelle und vor jeder Änderung. Dafür ruft der Agent npm mit `--allow-remote=all` auf: npm 12 hält manche Registry-Tarballs für entfernte und lehnte sonst etwa ein globales Node-RED ab ([ADR-0128](../docs/adr/0128-check-npm-tarball-origins-and-let-npm-12-fetch-them.md)); npm 11 übergeht die Option. |
| **Nie eine Version, die OSV kennt** | Der Agent schickt den Plan (`vet`, nur an eine Leitstelle mit Merkmal `vet`). Die Leitstelle prüft jeden Eintrag gegen ihre OSV-Daten und antwortet mit einer **signierten Aussage** (Kontext `vordr/vet/v1`, gebunden an Auftrag und Host, höchstens 5 min alt). Der Agent nimmt nur eine Aussage an, die jeden Eintrag des Plans nennt und keinen sonst. Ein markierter Eintrag → `precheck_version_flagged`. Keine oder veraltete Daten, keine Antwort → `precheck_vetting_unavailable`. Die Leitstelle antwortet nur für einen laufenden Paket-Auftrag dieses Hosts. Eine Version, die OSVs Ordnung nicht lesen kann, gilt nicht als sauber. |
| **Genau der geprüfte Plan** | pip: `install --no-deps` mit allen Versionen des Plans gepinnt; nichts wird neu aufgelöst. npm: dasselbe `--before`, also dieselbe Auflösung. |
| **Kein Code beim Installieren** | npm mit `--ignore-scripts`, pip nur Wheels (`--only-binary=:all:`), es sei denn `packages.allow_scripts` bzw. `packages.allow_sdist` nennen den Ort. Install-Skripte sind der übliche Weg bösartiger npm-Pakete. |
| **Nachprüfen** | Den Ort neu lesen: jedes Ziel in seiner Version, sonst `failed_verification`. pip zusätzlich `pip check`. Danach die `health:`-Prüfungen wie bei apt. |
| **Als Besitzer** | Unter Linux laufen pip und npm als Besitzer des Orts (uid/gid, `HOME`), damit z. B. ein Node-RED-Benutzer seine Dateien behält. Unter Windows läuft der Dienst als LocalSystem; was er in ein Benutzerprofil schreibt, erbt dessen Rechte. |
| **Namen nie als Option** | Ein Name beginnt mit Buchstabe oder Ziffer (PEP 508, npm), sonst kommt er weder in den Bestand noch in einen Auftrag. Sonst wäre `--index-url=…` ein „Paketname“. |

- **Auftrag:** `install` mit Umfang `pkg:<ort>` (alle Kandidaten des Orts) oder `pkg:<ort>/<name>`. Ort und Name stammen
  aus dem Bestand des Agenten. Die Leitstelle signiert nur, was der Host als aktualisierbar gemeldet hat (`update` am
  Ort) und, bei einem Namen, wo der Kandidat sauber ist. Die Version wählt der Agent, nie die Leitstelle.
- **Ältere Agenten** verstehen `pkg:` nicht; ihnen schickt die Leitstelle nichts, weil sie keinen Ort mit `update`
  melden.
- **Ablauf im Agenten:** Vorprüfung (5 %), Plan, Prüfung durch die Leitstelle, dann Laufjournal und Installation (50 %),
  Nachprüfung, `health:` (97 %). Bis zur Installation ändert sich nichts.
- **HA:**
  - je aktualisierbarem Ort eine Update-Entität `update.vordr_<id>_packages_<ort>` („Pakete <ort>“): installiert
    „<n> Pakete“, neueste z. B. „3 veraltet, 1 bereit“; ohne bereiten Kandidaten (alle zu neu oder markiert) ist sie `off`
    ([ADR-0106](../docs/adr/0106-update-entity-on-only-when-vordr-would-install.md));
  - die Zusammenfassung zeigt zuerst, was ein Update jetzt nimmt, dann „zu neu“ und „von OSV markiert“;
  - Installieren aktualisiert den Ort, der Dienst `vordr.update_package` (Ziel: diese Entität, Feld `package`) ein
    Paket; mit dem Feld `reason` auch ausserhalb des Fensters (6.4).
- **Trockenlauf** (`vordr-agent packages --plan <ort>[/<name>]` = alles bis vor die Prüfung, ändert nichts). Was er
  typisch zeigt:
  - Ein globales npm-Paket, dessen neuere Releases alle jünger als das Mindestalter sind: `precheck_version_too_new`.
  - Eine ganze Python-Umgebung mit einer Abhängigkeit, die es nur als Quellpaket gibt: „nur Wheels“ blockiert damit den
    ganzen Ort. Dann je Paket aktualisieren oder `allow_sdist` setzen.
  - Eine Linux-venv auf einem Windows-Laufwerk: Der Windows-Agent findet keinen Interpreter und meldet das, statt zu
    raten.
- **Nicht gebaut:** pip ausserhalb von Umgebungen (`pip-site`), `npm link`-Pakete, Rückbau eines Updates (`npm`/pip
  haben keinen; der vorige Stand ist im Laufprotokoll und im Backup).

**Abhängigkeiten mit Fund auffrischen** ([ADR-0104](../docs/adr/0104-refresh-indirect-dependencies-within-their-ranges.md),
Schritt 1 seit Agent 0.12.0, Integration 0.19.0):

- Die Kandidaten oben gelten für Pakete, die ein Ort um ihrer selbst willen hat. Ihre Abhängigkeiten bewegen sich nur
  mit ihnen, und gerade dort liegen die meisten Funde. Der Dienst `vordr.refresh_dependencies` (Ziel: die Update-Entität
  des Orts, Feld `reason` für ausserhalb des Fensters) hebt sie innerhalb der Bereiche, die ihre Abnehmer erlauben.
- **Wo:** bisher nur npm-Ordner mit Lock-Datei (Projekt, Node-RED-Benutzerordner). Der Agent meldet das je Ort im
  Bestand (`deps`), die Leitstelle zeigt es als `capabilities.dependency_refresh` und nimmt den Befehl
  `refresh_deps:<ort>` nur dafür an. Auftrag: `install` mit Umfang `deps:<ort>`. Globales npm und pip-Umgebungen folgen.
- **Ablauf im Agenten**, mit denselben Schranken wie ein Update:
  1. Welche: Der Agent bietet der Leitstelle alle Abhängigkeiten an, die dort installiert sind (`vet`), die signierte
     Antwort markiert die mit Fund.
  2. Plan: `npm update <namen> --package-lock-only` mit demselben `--before` in einer Kopie von `package.json` und Lock.
     Der Plan ist, was die neue Lock-Datei hat und die alte nicht. Jeder Eintrag muss aus der Registry kommen, sonst
     `precheck_not_from_registry` (ADR-0128, wie beim Update).
  3. Prüfung durch die Leitstelle: Ist die geplante Version einer genannten Abhängigkeit weiter markiert, bleibt sie
     draussen, und der Plan entsteht noch einmal ohne sie. Markiert die Leitstelle etwas, das das Update nur mitbringt,
     endet der Lauf vor jeder Änderung mit `precheck_version_flagged`.
  4. Laufjournal, dann `npm update <namen>` am Ort, mit demselben `--before` und ohne Install-Skripte (ausser
     `allow_scripts` nennt den Ort): npm löst gleich auf.
  5. Nachprüfen: Jeder Eintrag des Plans ist installiert, sonst `failed_verification`.
- **Was bleibt, wird genannt:** Eine Abhängigkeit, die keine erlaubte Version hebt, steht im Ergebnis des Laufs mit den
  Paketen, die sie festhalten („needs a new release of …“); ebenso eine, deren neueste erlaubte Version selbst einen
  Fund hat. Der Lauf ist dann trotzdem `ok`.
- **Trockenlauf:** `vordr-agent packages --plan-deps <ort>:<name>[,<name>…]` plant für die genannten Abhängigkeiten,
  ohne Leitstelle und ohne etwas zu ändern; die Lock-Datei bleibt unverändert. Eine Abhängigkeit, die innerhalb ihrer
  Bereiche nicht steigen kann, steht dort mit dem Abnehmer, der sie festhält.

### 11.5 Container-Images: veraltete und verwundbare melden, Neubauten per Knopf

Eigene Quelle statt updates2mqtt, zuerst nur melden
([ADR-0081](../docs/adr/0081-container-images-own-source.md)). Gebaut sind: welche Images veraltet sind
([ADR-0113](../docs/adr/0113-report-outdated-container-images.md)), welche bekannten Schwachstellen in ihnen stecken
([ADR-0120](../docs/adr/0120-known-vulnerabilities-inside-container-images.md), unten „Pakete in Images“) und das
Aktualisieren von Compose-Diensten auf den Neubau ihres Tags per Knopf
([ADR-0119](../docs/adr/0119-update-container-images-by-button.md), unten „Per Knopf aktualisieren“). Ohne einen
`image:`-Eintrag in `install.allowed_scopes` meldet Vördr nur und zieht, startet oder ändert keinen Container.

- **Einschalten je Host:** `packages.containers: true` in agent.yaml, Standard aus (neuer Netzverkehr vom Host).
  Nur unter Linux und nur mit `packages.inventory` an; die Images sind Teil des Bestands. `check-config` lehnt sonst ab.
- **Bestand (Agent, Paket `internal/images`):** die laufenden Container über die Engine-API am lokalen Socket
  (`/var/run/docker.sock`; eine Engine mit derselben API, etwa Podmans kompatibler Socket, passt an dieser Stelle),
  nur `GET`, kein CLI:
  - `GET /containers/json`, je Container `GET /containers/<id>/json` (Referenz, mit der er angelegt wurde,
    `Config.Image`) und je Image `GET /images/<id>/json` (`RepoDigests`, `RepoTags`, `Created`, Plattform,
    `Config.Labels`).
  - Ein Eintrag je Image und Tag, mit den Namen der Container. `name` ist das Repository samt Registry
    (`docker.io/library/nginx`), `version` der Tag, `digest` der Repo-Digest, mit dem es gezogen wurde.
  - Festgelegter Digest (`app@sha256:…`): verglichen wird unter dem Tag, den das Image auf dem Host für
    dasselbe Repository trägt (`RepoTags`; bei mehreren zuerst einer mit Versionsform, sonst der alphabetisch erste;
    [ADR-0117](../docs/adr/0117-compare-pinned-images-under-their-tag.md)). Ohne solchen Tag ist `version` der Digest,
    und das Image wird nur aufgeführt. Zwei Referenzen auf dasselbe Image unter demselben Tag sind ein Eintrag.
  - Lokal gebaut: gemeldet, nie verglichen, keine Registry wird gefragt. Lokal heisst ohne Repo-Digest (klassischer
    Image-Speicher) oder von Compose gebaut (Label `com.docker.compose.project` in der Image-Konfiguration): Der
    containerd-Image-Speicher gibt auch lokalen Builds einen Repo-Digest
    ([ADR-0115](../docs/adr/0115-recognize-images-compose-built-on-the-host.md)). Ein Container, der aus einer Image-ID
    angelegt wurde, hat keine Referenz und zählt als `skipped`.
  - Zustand statt Fehlertext: `no_engine` (kein Socket), `engine_unreachable` (Socket da, Engine antwortet nicht).
    Ein Container, der zwischen Liste und Abfrage verschwindet (404), fehlt nur selbst.
- **Ort im Bestand:** `containers`, Art `container`, Ökosystem `oci`. Der Agent schickt ihn nur einer Leitstelle, die
  das Merkmal `containers` ankündigt ([ADR-0067](../docs/adr/0067-hub-announces-features.md)): Eine ältere Leitstelle
  zählte die Images sonst zu den pip/npm-Paketen und hielte die Funde für unbekannt.
- **Registry, anonym:** höchstens alle `outdated_hours`, nur mit `outdated`, über die Distribution-API und HTTPS.
  - Eigener Zeitplan: Ein Check, der die Engine nicht lesen konnte oder dessen Leitstelle noch keine Images annahm,
    vergleicht nichts und hält den nächsten Vergleich nicht auf. Der Ort sagt, wann verglichen wurde (`compared_at`);
    bis dahin sind `outdated` und `ready` an der Leitstelle unbekannt.
  - Antwortet die Registry mit 401 und `WWW-Authenticate: Bearer`, holt der Agent beim genannten Realm (nur `https`)
    ein Token für Lesezugriff, ohne Zugangsdaten. Ein 401 auf ein Token einer früheren Anfrage holt einmal ein frisches
    (die von Docker Hub laufen nach fünf Minuten ab). Erst 401/403 mit frischem Token oder 404 heisst
    `not_in_registry` (privat oder weg). 429, 5xx und Netzfehler zählen als `outdated_failed`.
  - Docker Hub (`docker.io`) antwortet unter `registry-1.docker.io`.
- **Downloads, die als Pull zählen:** `HEAD` zählt bei Docker Hub nicht, ein Manifest-Download schon, auf dieselbe
  anonyme Grenze wie die Pulls des Hosts selbst, oft geteilt mit allen Hosts hinter derselben öffentlichen Adresse.
  - Konfiguration und Bauzeit je Digest und Plattform behält der Agent in `<state_dir>/image-cache.json` (bis 4096,
    die am längsten ungenutzten fallen weg). Ein Digest ändert seinen Inhalt nie: Weder spätere Läufe noch ein
    neu gestarteter Agent laden dieselben Manifeste noch einmal.
  - Höchstens 20 Manifest-Downloads je Registry und Lauf; der Rest zählt als `outdated_failed` und kommt im nächsten
    Lauf dran.
- **Neu gebaut:** `HEAD` auf das Manifest des Tags. Weicht der Digest vom Repo-Digest ab, liest der Agent das Image
  für seine Plattform: Index → Manifest der Plattform → Konfiguration (`created`).
  - Neu gebaut ist es, wenn das eine andere Konfiguration ist als die laufende, unabhängig von der Bauzeit:
    reproduzierbare Builds (distroless, ko, Bazel, Nix) behalten eine feste. Ein Index ändert sich auch, wenn nur eine
    andere Plattform oder eine Attestierung dazukommt; das ist kein Neubau.
  - Die laufende Konfiguration: beim klassischen Image-Speicher der Engine die Image-ID. Beim containerd-Speicher ist
    die ID der gezogene Digest; dann liest der Agent die Konfiguration dahinter aus der Registry. Hat die Registry
    diesen Digest nicht mehr, gilt der Tag als neu gebaut.
  - `rebuilt_candidate`: mindestens `min_age_days` alt.
  - Ein Container mit festem Digest bleibt auf seinem Build. Unter seinem Tag verglichen heisst `rebuilt` dann: Der
    Tag ist seit dem festgelegten Build weitergezogen, die Festlegung ist überholt (ADR-0117).
  - Antwortet `HEAD` auf den laufenden Tag mit 404 (Aufbewahrungsregel hat ihn gelöscht), liest der Agent trotzdem
    die Tag-Liste; erst wenn auch die verschlossen ist oder der Tag keine Form hat, zählt das Image als
    `not_in_registry`.
- **Neuerer Tag derselben Form:** optionales `v`, Zahlen mit Punkten, gleiche Anzahl Zahlen, gleiche Endung (`-`, `_`
  oder `+` und der Rest): `2.4.1-gpu` vergleicht nur mit `x.y.z-gpu`, `16` mit `17`.
  - Eine Zahl mit sechs oder mehr Ziffern ist ein Datum oder eine Build-Nummer (`20250108`), keine Version, und
    vergleicht nur mit einer ebensolchen: `alpine:3` findet das Datums-Tag `20250108` nicht neuer.
  - Tags ohne Zahl (`latest`, `stable`) haben keine Form, ein laufender Vorabversions-Tag (`rc`, `beta` …) auch nicht:
    nur „neu gebaut“.
  - Die Tag-Liste kommt seitenweise (`Link: …; rel="next"`, nur auf demselben Host), höchstens 60 Seiten zu 1000 und
    je 1 MiB, behalten werden nur gültige Tags, höchstens 60000; eine längere gilt als nicht beantwortet, die neuesten
    Tags könnten auf einer ungelesenen Seite stehen. Für weitere Images desselben Repositorys hält ein Lauf höchstens
    120000 Tags. Die Grenze von 60 Seiten:
    [ADR-0121](../docs/adr/0121-read-tag-lists-of-up-to-60000-tags.md).
  - Von den neueren Tags, neueste zuerst, liest der Agent höchstens 5 bis zum ersten, der alt genug ist: `latest` der
    neueste, `candidate` der neueste mit mindestens `min_age_days`, `latest_at` dessen Bauzeit, solange er jünger ist.
    `major`: Die erste Zahl ändert sich. Ein Tag ohne Image für die Plattform des Hosts zählt nicht.
  - Der kleinere Schritt daneben
    ([ADR-0131](../docs/adr/0131-name-the-newest-tag-of-the-running-major-version.md)): Ist `candidate` eine neue
    Hauptversion, oder `latest` eine und kein Tag alt genug, liest der Agent auch die neueren Tags mit der laufenden
    ersten Zahl, neueste zuerst, wieder höchstens 5: `same_major` ist der erste davon mit mindestens `min_age_days`
    (ein Image auf 3.5: `candidate` 4.2, `same_major` 3.9). Liegt `candidate` schon in der laufenden Hauptversion,
    entfällt der zweite Gang. Eine Frage ohne Antwort zählt darin wie jede andere (`compare_failed`).
- **Alter:** `created` der Konfiguration. Vor 2013 (reproduzierbare Builds schreiben 1970) ist das Alter unbekannt:
  gemeldet (neu gebaut oder neuerer Tag), nie Kandidat.
- **Schranken:** höchstens 300 Anfragen je Lauf, Tokens eingerechnet; was danach offen ist, zählt als
  `outdated_failed`.
- **Warum keine Antwort** ([ADR-0126](../docs/adr/0126-say-why-an-image-comparison-got-no-answer.md)): Jedes Image,
  das als `outdated_failed` zählt, trägt im Bestand den Grund (`compare_failed`), aus der Antwort selbst, nie aus
  einem Fehlertext: `rate_limited` (429), `request_budget` (die 300 Anfragen), `manifest_share` (die 20
  Manifest-Downloads der Registry), `tag_list_too_long` (mehr Seiten, Tags oder Bytes als oben), sonst `no_answer`
  (Netz, 5xx, unlesbare Antwort, Zeit um). Es zählt der erste Grund des Images. Die Leitstelle zeigt sie als
  `compare_failed` (Image, Tag, Container, Grund; höchstens 100), HA als Attribut der Entität „Container-Images“,
  nicht im Recorder. Ein Agent davor meldet nur die Zahl.
- **Leitstelle:** bewahrt den Ort wie jeden Bestand, ohne OSV-Abgleich, und zeigt ihn getrennt von den Paketen in der
  Host-Sicht als `containers` (`images`, `local`, `outdated`, `ready`, `compared_at`, `updates` mit höchstens 100
  Images, die mit Kandidat zuerst). Solange die Images nie verglichen wurden (`compared_at` fehlt), sind `outdated` und
  `ready` unbekannt, nicht null. Veraltete Images sind kein Problem des Hosts. Ein Image nur mit `same_major` (die neue
  Hauptversion noch zu neu) zählt als `ready`: Es gibt etwas, das alt genug ist (ADR-0131).
- **Home Assistant:** eine Update-Entität je Host „Container-Images“ (`update.vordr_<id>_containers`). An, solange
  ein Image einen Kandidaten hat. Ohne erlaubte Dienste ohne Installieren, und die Zusammenfassung beginnt mit „Nur
  gemeldet, Vördr aktualisiert keine Images“; mit erlaubten Diensten siehe „Per Knopf aktualisieren“. Neben einer
  neuen Hauptversion nennt sie den kleineren Schritt: „app 2.2.0 → 3.1.0 (neue Hauptversion, oder 2.4.0 in
  derselben Hauptversion)“. Die Liste steht im Attribut `updates` (mit `same_major`), nicht im Recorder
  ([ADR-0101](../docs/adr/0101-package-lists-stay-out-of-the-recorder.md)).
- Zu den veralteten pip/npm-Paketen (11.3): Liegt ein Paket an zwei Orten, etwa älter per `pip --user` und neuer im
  Interpreter, zeigt `pip list --outdated` nur das erste, Vördr beide Orte.

**Pakete in Images** ([ADR-0120](../docs/adr/0120-known-vulnerabilities-inside-container-images.md)):

- **Einschalten:** `Debian` und/oder `Alpine` in `osv.ecosystems` der Leitstelle. Dann kündigt sie das Merkmal
  `image_contents` an, und nur dann lesen Agenten die Pakete in ihren Images.
- **Agent:** je Image einen seiner Container, über `GET /containers/{id}/archive` der Engine, kein `exec`, kein Pull:
  - `/etc/os-release` (dem Link einmal folgend, sonst `/usr/lib/os-release`): Distribution und Release;
  - `/var/lib/dpkg/status`, bei distroless `/var/lib/dpkg/status.d/`, sonst `/lib/apk/db/installed`: die installierten
    Pakete mit Quellpaket (dpkg `Source`, apk `origin`).
  - Mit jedem Vergleich (`outdated_hours`), ohne `outdated` im selben Takt ohne Vergleich; dazwischen nur für ein
    Image, zu dem der Agent noch nichts hat, und das einmal. Die Antworten bleiben mit dem Vergleich im Zustandsordner.
  - Schranken: os-release 64 KB, Paketdatenbank 16 MB, 5000 Pakete je Image (`too_many_packages`). Passt die
    Inventur nicht in eine Nachricht, fallen zuerst die Pakete des Images mit den meisten weg (Code
    `too_many_packages` ohne Pakete, das Image bleibt); gekürzt wird nach Grösse, nicht nach Anzahl.
  - Zustand statt Fehlertext: `no_package_db` (distroless ohne Datenbank, scratch), `unreadable`, `unknown_release`.
- **Leitstelle:**
  - Sie lädt den Dump einer Distribution nur, solange ein Image eines ihrer Releases läuft, und baut nur für diese
    Releases einen Index (`Debian:13`, `Alpine:v3.23`).
  - Vorher prüft sie den Platz, Dump plus 256 MB (`no_space`).
  - Die Indexe bleiben auf der Platte. Für einen Abgleich lädt sie einen, gleicht jedes Image dieses Releases ab,
    behält die Ergebnisse und verwirft ihn wieder, einen nach dem anderen. Ein Debian-Release braucht im Speicher
    rund 60 MB, ausserhalb des Speichers unter 1 MB.
  - Jedes Release hat seinen eigenen Stand: Ein Index gilt nur für den Dump (ETag) und die Indexversion, aus denen er
    gebaut ist; sonst holt sie den Dump sofort. Ein Release ohne Einträge im Dump ist ein Zustand (`no_entries`), kein
    Fehler. Ein unglaubwürdig geschrumpftes behält seinen alten Index (`implausible_dump`), die anderen nehmen den
    neuen Dump. Ein unlesbarer Index wird vergessen und neu gebaut. Auch das Bauen wartet auf die Abgleichsperre.
  - Abgleich nach Quellpaket und Quellversion, wie Debian und Alpine ihre Meldungen schreiben. Gezählt wird je CVE:
    einmal, auch wenn Debian ihn als DEBIAN-CVE und als DSA veröffentlicht; eine DSA für mehrere CVEs zählt jeden.
    „unimportant“ zählt für dieses Paket nicht, gleich welche Meldung den CVE nennt.
  - Die Quellpakete des Kernels zählen nicht
    ([ADR-0122](../docs/adr/0122-kernel-advisories-in-images-do-not-count.md)): Debian `linux` und die
    versionierten `linux-<version>` (`linux-6.12`), Alpine `linux-lts` und `linux-grsec`. Ein Container läuft auf dem
    Kernel des Hosts; das Image trägt höchstens dessen Header (`linux-libc-dev`). Ihre Meldungen stehen je Image als
    `kernel`, ausser ein anderes Paket des Images hat denselben CVE, dann zählt er dort. Pakete, die nur `linux` im
    Namen tragen (`util-linux`, `linux-pam`, `linux-base`), zählen wie alle.
  - Je Image `fixable` (ein neueres Paket behebt es) und `unfixed` (noch keine Korrektur), mit den IDs; ein Image
    ohne Abgleich mit Grund (`not_read`, `unsupported_distribution`, `unsupported_release`, `no_data`,
    `too_many_packages`). Ein Image nur mit Meldungen des Kernels steht mit `kernel` in der Liste und zählt weder als
    `fixable` noch als `unfixed`.
  - Ob ein neueres Image bereitsteht
    ([ADR-0130](../docs/adr/0130-image-entity-needs-attention-only-with-a-newer-image.md)): je Image `newer`, wenn es
    einen Kandidaten hat (Neubau oder neuerer Tag, alt genug). `fixable_newer` zählt die Images mit `fixable` und
    Kandidat; nil, solange `fixable` nil ist oder die Images nie mit ihren Registries verglichen wurden. Images mit
    `fixable` und Kandidat stehen zuerst. Ein behobenes Paket der Distribution heisst noch nicht, dass ein neueres Image
    da ist: Das kommt erst, wenn der Herausgeber neu baut.
  - In der Datenübersicht (`osv`) erscheinen nur Releases, die ein Image braucht.
- **Home Assistant:** dieselbe Entität „Container-Images“.
  - Schwachstellen allein schalten sie nicht an, auch behebbare nicht: an nur mit einem Kandidaten (ADR-0130, wie
    ADR-0106 für die anderen Update-Entitäten). Mit Kandidat nennt `latest_version` dazu, für wie viele Images mit
    behebbaren Meldungen ein neueres Image bereitsteht („1 durch neueres Image behebbar“).
  - Die Zusammenfassung sagt je Image, ob ein neueres Image bereit ist: „2 behebbar, neueres Image bereit“ oder „2 in
    der Distribution behoben, noch kein neueres Image“; Meldungen ohne Korrektur wie bisher. Eine Leitstelle vor
    ADR-0130 sendet kein `fixable_newer`; dann bleiben die Zahlen ohne diesen Zusatz.
  - Die Liste steht im Attribut `vulnerable`, nicht im Recorder, je Image mit `kernel`, wo es Meldungen des Kernels
    gibt, und `newer`; die Zusammenfassung nennt die Meldungen des Kernels nicht.
  - Kein Problem des Hosts.
- **Ubuntu** ist noch nicht dabei: OSV schreibt seine Releases anders (`Ubuntu:24.04:LTS`), und sein Dump ist zehnmal
  so gross wie Debians. Ubuntu-Images zeigen `unsupported_distribution`.

**Per Knopf aktualisieren** ([ADR-0119](../docs/adr/0119-update-container-images-by-button.md)):

- **Erlauben je Dienst:** `image:<projekt>/<dienst>` in `install.allowed_scopes`, `*` für einen beliebigen Teil
  (`image:web/*`). Standard: keiner. Braucht `packages.containers` und `packages.outdated`; `default_scope`
  bleibt für die Systempakete und ist nie ein `image:`-Eintrag. `check-config` lehnt sonst ab und nennt die Dienste.
- **Was geht:** nur der Neubau des Tags, den die Compose-Datei nennt, und nur ab `min_age_days`, und nur das Image,
  nie die Konfiguration. Handarbeit bleiben:
  - ein neuerer Tag (1.2 → 1.3) und ein gepinnter Digest: beides braucht eine geänderte Compose-Datei;
  - ein Dienst mit mehr als einem Container (Compose skalierte ihn auf seine Datei);
  - ein Tag, den noch ein anderer Container nutzt (er würde mitwandern). Ausgenommen ist ein gestoppter Container
    desselben Compose-Projekts (`exited`, `created`, `dead`), etwa ein einmaliger Helfer, der vor dem Dienst Rechte
    setzt: Er behält sein Image und nimmt das neue erst, wenn Compose ihn mit dem Projekt neu erstellt
    ([ADR-0123](../docs/adr/0123-stopped-containers-of-the-project-do-not-share-the-tag.md)). Der Lauf startet ihn
    nicht neu (`--no-deps`); einen Dienst, dessen Helfer vor jeder neuen Version laufen muss (Datenbank-Migration),
    nicht erlauben;
  - ein Container ohne Compose-Labels oder von `docker compose run`, ein auf dem Host gebautes Image, ein Projekt,
    dessen Ordner oder Dateien fehlen oder nicht absolut sind;
  - ein Dienst, dessen Compose-Dateien oder Variablen sich seit seinem Start geändert haben (anderer
    Konfigurations-Hash): ihn einmal von Hand hochfahren.
- **Bestand:** Der Agent liest die laufenden Container und meldet je Image die Dienste, die er aktualisieren darf und
  kann (`updatable`, ohne die Hash-Prüfung, die erst der Lauf macht); die Leitstelle bietet nur diese an
  (`capabilities.image_updates`), zeigt sie je Image (`services`) und zählt, was der Knopf nähme (`installable`: ein
  Neubau, alt genug, eines solchen Dienstes).
- **Aufträge:** `image:<projekt>/<dienst>` für einen Dienst, `images` für jeden erlaubten (der Agent wählt). Der
  Auftrag nennt nur Namen, kein Image, keinen Tag, kein Argument. `min_interval_h` hält sie nicht auf (wie pip/npm),
  ein Lauf mit Änderung zählt aber als Installationslauf. Die Sicherung vor dem Lauf (ADR-0030, ADR-0096) gilt wie für
  jedes `install`.
- **Lauf je Dienst,** jede Prüfung vor der ersten Änderung:
  1. Compose ist da und mindestens 2.22 (`up --pull`), sonst `rejected_unsupported`. Compose spricht immer mit der
     Engine des Agenten (`DOCKER_HOST`), gleich welcher Docker-Kontext für root gilt.
  2. Der Dienst lässt sich bewegen (siehe oben), sonst `rejected_scope_not_allowed` mit Grund.
  3. `docker compose … config --hash <dienst>` gibt den Hash aus dem Label des Containers, sonst
     `rejected_scope_not_allowed`.
  4. Die Registry jetzt fragen, worauf der Tag zeigt (nicht die Antwort der letzten Inventur; ihr Cache wird nur
     gelesen): kein Neubau → `ok` ohne Änderung; jünger als `min_age_days` → `precheck_version_too_new`; keine Antwort
     → `precheck_repo_unreachable`.
  5. Platz: das Vierfache des Downloads plus 256 MB im Datenordner der Engine (und bei containerd-Image-Store in
     dessen Ordner, wo es ihn gibt); zu wenig oder nicht lesbar → `precheck_disk_space`.
  6. Den Neubau über die Engine-API **nach Digest** für die geprüfte Plattform ziehen. Scheitert das, ist nichts
     geändert (`failed_package_manager`).
  7. Lauf-Journal schreiben. Ab hier läuft der Dienst zu Ende oder zurück, mit eigenem Zeitrahmen (30 min), auch
     wenn `install.timeout_min` abläuft oder der Agent stoppt.
  8. Das laufende Image als `<repo>:vordr-previous.<projekt>.<dienst>` behalten, den Tag der Compose-Datei auf den
     Neubau legen und `docker compose --project-name … --project-directory … --file … up --detach --no-deps --no-build
     --pull never <dienst>` mit den Dateien aus den Labels.
  9. Gesund: Der Container läuft mit dem neuen Image, mit Healthcheck `healthy`, und dann 30 s ohne Neustart, binnen
     3 min; dann `health` aus agent.yaml. Ein Container, der nach Sekunden stirbt und neu gestartet wird, kommt nie
     dahin.
  10. Sonst zurück: Das alte Image bekommt den Tag wieder, Compose erzeugt den Dienst erneut, das Behalten-Tag geht
      zurück (an den Rückweg des Laufs davor, falls es ihn gab), das neue Image wird entfernt; `failed_health` (bei
      gescheitertem Compose `failed_package_manager`) mit „rolled back“ im Detail.
  11. Gelingt es, entfernt der Agent das Image des Laufs davor, wenn es kein Name und kein Container mehr hält: eine
      Generation bleibt als Rückweg.
- `images` arbeitet die Dienste nacheinander ab. Nicht bewegbare, zu neue und ohne Antwort bleiben liegen und stehen
  mit Grund im Detail; ein Fehler nach einer Änderung wird zurückgenommen und beendet den Lauf. Ist nichts geschehen,
  sagt der Code warum (`precheck_disk_space`, `precheck_repo_unreachable`).
- **Home Assistant:** „Container-Images“ installiert (`install_images`), wenn der Host Dienste erlaubt und die
  Leitstelle etwas zählt, das der Knopf nähme (`installable`), mit Fortschritt; die Zusammenfassung beginnt dann mit
  „Neu gebaute Images der freigegebenen Dienste nimmt der Installieren-Knopf, neuere Tags bleiben Handarbeit“. An
  bleibt die Entität auch für Handarbeit. Die Aktion `vordr.update_image` (Feld `service`: `<projekt>/<dienst>`)
  aktualisiert einen Dienst, mit `reason` auch ausserhalb des Fensters, ebenso `vordr.install_outside_window` für alle.

## 12. Leitstelle

- Go, eine Binary, systemd-Dienst. Zustand in Dateien wie im Vorbild-Projekt: Register (`registry.json`), Ereignisprotokoll
  (`events.jsonl`, nur anhängend), Einstellungen und Ringe (`hub.yaml`). Keine Datenbank
  ([ADR-0011](../docs/adr/0011-state-in-files.md); Ort: [ADR-0054](../docs/adr/0054-hub-in-own-container.md)).
- Schnittstellen: Tunnel-Endpunkt (WebSocket über TLS, Zertifikat wird bei Erneuerung ohne Neustart nachgeladen),
  Home-Assistant-Endpunkt `/v1/ha` auf demselben Port ([ha-integration.de.md](ha-integration.de.md)), Download der
  Agent-Binaries (HTTPS, Einmal-Token). Keine Weboberfläche.
- Verwaltung nur lokal über einen Unix-Socket (`admin_socket`, Modus 0600 in einem 0700-Ordner): `vordr-hub list |
  approve <node> --fingerprint <fp> | reject <node> | forget <node> | check <node> | backup <node> | events`, für
  Home-Assistant-Clients (eigenes Register `clients.json`) `clients | approve-client <name> --fingerprint <fp> |
  reject-client <name> | forget-client <name> | client-role <name> --role <rolle>`. Ohne Socket: `key`, `check-config`,
  `version`. Noch nicht gebaut: ein Befehl für die Ringe (`rings`)
  ([ADR-0010](../docs/adr/0010-administration-only-local.md)).
- **Beenden** (SIGTERM): Die Leitstelle nimmt nichts Neues mehr an, schliesst die Verbindungen der Agents und
  Home-Assistant-Clients und wartet bis zu 10 s, bis fertig ist, was sie angestossen haben (eine Meldung, die gerade
  gespeichert wird, das Ergebnis eines Jobs). Das Warten auf Job-Ergebnisse endet dabei mit `hub_stopping`, ohne
  etwas einzutragen: der Lauf bleibt offen, der nächste Hello des Agents klärt ihn
  ([ADR-0142](../docs/adr/0142-the-hub-waits-for-its-sessions-work-when-it-stops.md)).
- **Backup-Prüfung ansehen** (`vordr-hub backup <node>`): was die Prüfung vor einem Install jetzt entschiede (10.2);
  fragt den Backup-Server, schickt nichts.
- **Ereignisprotokoll lesen** (`vordr-hub events [<node>] [--event <name>] [--since <wann>] [--limit <n>]`):
  - Die Leitstelle liest `events.jsonl` selbst und gibt die passenden Zeilen über den Socket zurück. Wer lesen darf,
    entscheidet der Socket, nicht die Dateirechte des Zustandsordners.
  - Gezeigt werden die neuesten passenden Zeilen, standardmässig 50, höchstens 5000, älteste oben.
  - `--since` nimmt eine Dauer zurück (`90m`, `24h`, `7d`), ein Datum (Mitternacht Ortszeit) oder eine Zeit nach
    RFC 3339.
  - Gelesen wird ohne die Schreibsperre: Die Leitstelle schreibt weiter. Eine Zeile, die gerade entsteht, ist dann
    unvollständig und wird als unlesbar gezählt und genannt, nicht übersprungen ohne Wort.
- **Rollen der HA-Clients** ([ADR-0039](../docs/adr/0039-ha-client-roles.md)):
  - `observe`: nur Sichten.
  - `operate`: Prüfen, Installieren, Agent-Update. Das ist der Standard.
  - `admin`: zusätzlich Neustart.

  Die Richtlinie des Hosts hat ohnehin das letzte Wort. Die Rolle begrenzt, was ein übernommenes Home Assistant
  die Leitstelle signieren lassen kann. Eine Ablehnung heisst `role_forbids`, HA zeigt dafür einen eigenen Text.
- **Warteschlange je Host** ([ADR-0043](../docs/adr/0043-queue-per-host.md),
  [ADR-0044](../docs/adr/0044-same-command-joins-running-run.md)):
  Ein Agent führt einen Auftrag nach dem anderen aus. „Alle aktualisieren“
  in HA drückt aber mehrere Knöpfe eines Hosts in derselben Sekunde; ohne Warteschlange liefe etwa das Agent-Update,
  und das Installieren endete mit `rejected_busy`.
  - Kommt ein HA-Befehl, während auf dem Host noch ein Lauf geht, wartet er, statt gesendet zu werden.
  - Ein Befehl, der schon wartet, wartet nicht doppelt; höchstens 3 warten (sonst `queue_full`).
  - Ein Befehl gleich dem laufenden (Verb und Umfang) wartet gar nicht. Die Antwort ist `ok` mit der `job_id` des
    laufenden Laufs, das Ereignisprotokoll vermerkt `command_already_running`. Sonst wartete etwa ein zweiter Tipp
    auf „Jetzt neu starten“ und endete nach dem ersten Neustart mit `rejected_rate_limited`.
  - Nach dem Ende des Laufs startet die Leitstelle nach 30 s den nächsten. Ein Agent, der eben neu gestartet hat,
    prüft zuerst.
  - Die Host-Sicht zeigt die Warteschlange (`queued`), HA am Sensor „Letzter Lauf“.
  - Ein Lauf, der älter ist als sein Auftrag dauern darf, hält die Warteschlange nicht auf.
  - Dieselbe Sicht „beschäftigt“ gilt für das automatische Installieren und die Agent-Verteilung: ein Lauf ohne
    Ergebnis, höchstens so lange, wie sein Auftrag dauern darf, oder ein Hypervisor, der gerade einen Gast sichert.
    Ohne diese Frist hielte ein Lauf, dessen Ergebnis verloren ging, den Host für immer von beiden fern.
  - Ein Ergebnis, auf das niemand mehr wartet, schliesst den Lauf ab und startet den nächsten wartenden Befehl. Das
    kommt vor, wenn die Leitstelle während des Laufs neu startete oder der Tunnel abriss und der Lauf weiterging.
  - Meldet ein wartender Befehl beim Start `already_in_state` (der Agent hat die Release schon), kommt der nächste
    dran.
  - Die Backup-Prüfung vor dem Installieren gilt zum Zeitpunkt des Befehls.
  Ergebnisse als feste Codes (`ok`, `unknown_node`, `fingerprint_mismatch`, `not_connected`, `not_approved`,
  `verb_unsupported_by_agent`, `timeout`, …). `vordr-hub key` zeigt den öffentlichen Hub-Schlüssel für
  `agent.yaml`.
- Browser-Verbindungen (mit `Origin`-Kopfzeile) werden abgewiesen. Höchstens `max_pending` Hosts warten auf Freigabe;
  ein bekannter Host mit anderem Schlüssel wird abgewiesen, bis er mit `forget` entfernt ist.
- Prüft HA-Befehle gegen die gemeldete Richtlinie des Hosts, bevor er signiert.
- Meldet Problemfälle: Host stumm (`offline`), `stale`, wartende Freigaben, gescheiterte Agent-Updates.

## 13. Plattformen und Bau

- Go 1.27, `CGO_ENABLED=0`, `-trimpath -ldflags "-s -w"`: statische Binaries ohne Laufzeitumgebung.
- Ziele des Agenten: `linux/amd64`, `linux/arm64`, `linux/arm` (GOARM=7), `windows/amd64`. Hub: `linux/amd64`.
- Grössenordnung bei der Wahl der Sprache: Ein Kern mit TLS, WebSocket, Ed25519 und JSON ergab 6,2–7,0 MB je Ziel
  (gzip 2,6–2,9 MB) und 12 MB RSS nach einer TLS-Verbindung.
- Rust wäre kleiner (geschätzt 1,5–3 MB und 2–5 MB RSS, nicht gemessen). Das lohnt sich erst auf Geräten mit sehr
  wenig Speicher, und die bekommen hier keinen Agenten (Nicht-Ziele in Abschnitt 1). Go erlaubt dagegen, die erprobten
  Teile des Vorbild-Projekts zu übernehmen (Abschnitt 14; [ADR-0003](../docs/adr/0003-go.md)).
- Windows-Binaries bleiben ohne Authenticode-Signatur; ihre Echtheit sichert das Betreiber-Manifest
  ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)).

## 14. Übernahme aus dem Vorbild-Projekt

Kopiert und angepasst, ohne gemeinsame Bibliothek, damit beide Projekte unabhängig releasen
([ADR-0012](../docs/adr/0012-copy-parts-of-the-earlier-agent.md)). Ein gemeinsames Modul
lohnt sich erst, wenn beide parallel weiterwachsen.

| Vorbild-Projekt (`agent/`) | Vördr | Anpassung |
|---|---|---|
| `identity.go` | Agent-Identität | Ed25519, DPAPI unter Windows, unverändert |
| `tunnel.go` | Tunnel-Client | Challenge/`hello`, Backoff; statt Proxy-Datenströmen signierte Aufträge und Meldungen |
| `updater.go` | Selbst-Update | Fehlerarten von Anfang an englisch |
| `service_windows.go`, `install_windows.go`, `setup_windows.go` | Windows-Dienst, Einrichtung mit `install-service` (11.1) | Dienst unter LocalSystem (Windows Update verlangt es); keine Einrichtung per Doppelklick |
| `mqtt.go` (`syncDiscovery`) | nur das Muster | Tri-State beim Anlegen und Löschen von Entitäten (8.4); der MQTT-Code selbst ist entfernt ([ADR-0026](../docs/adr/0026-remove-mqtt.md)) |
| `sign.ps1` | – | nicht übernommen: Windows-Binaries bleiben ohne Authenticode-Signatur ([ADR-0084](../docs/adr/0084-windows-self-update-without-code-signing.md)) |

Von der Server-Seite des Vorbild-Projekts übernimmt der Hub nur Muster (Register mit Freigabe, Rollout mit
Kanarienvogel, Ereignisprotokoll), keinen Code.

## 15. Ausbaustufen

| Stufe | Inhalt | Stand |
|---|---|---|
| 1 | Agent mit Quelle `apt` und Verb `check`; Hub mit Anmeldung, Freigabe und HA-Entitäten | gebaut |
| 2 | `install` und `reboot` mit Richtlinie, Laufjournal und den Codes aus 7.3 | gebaut (10.2) |
| 3 | Selbst-Update, Ringe, `install.auto: hub` | gebaut (10.3, 10.4); `install.auto: local` noch nicht |
| 4 | Quellen `winget` und `winupdate` | gebaut (11.1); Neustart unter Windows noch nicht |
| 5 | Apps, HA-Blueprint inklusive „hängt“ für Integrationen | Apps per Knopf gebaut (8.1); Update-Entität je App und Blueprint noch nicht (8.5) |
| 6 | pip, pipx, npm: Bestand und OSV-Funde, veraltete Pakete, Aktualisieren per Knopf; Container-Images | gebaut (11.2–11.5) |

## 16. Früher offene Fragen

1. `install.auto` je Host-Klasse: Wer löst aus, und welcher Umfang (nur Sicherheit oder alles)?
   → [ADR-0058](../docs/adr/0058-site-decision.md),
   [ADR-0059](../docs/adr/0059-own-auto-scope.md), [ADR-0088](../docs/adr/0088-hypervisor-updates-by-button-only.md),
   [ADR-0108](../docs/adr/0108-automatic-installs-take-every-update-on-linux-guests.md)
2. Neustart: Welche Hosts dürfen selbst neu starten? Proxmox-Hosts vermutlich nie.
   → Ablauf: [ADR-0041](../docs/adr/0041-reboot-with-window-and-proof.md); der Hypervisor nie über Vördr
   ([ADR-0088](../docs/adr/0088-hypervisor-updates-by-button-only.md))
3. Welche Hosts sind Kanarienvögel? → je Standort in `auto_install.canary` und `agent_update.canary` der `hub.yaml`;
   Muster: [ADR-0058](../docs/adr/0058-site-decision.md)
4. Wo läuft der Hub? → [ADR-0054](../docs/adr/0054-hub-in-own-container.md); ein MQTT-Konto braucht er nicht mehr
   ([ADR-0026](../docs/adr/0026-remove-mqtt.md))
5. unattended-upgrades: ablösen oder daneben laufen lassen?
   → [ADR-0080](../docs/adr/0080-security-updates-everywhere-unattended-upgrades-off.md)
6. Docker: eigene Quelle `docker-image` oder updates2mqtt einbinden?
   → [ADR-0081](../docs/adr/0081-container-images-own-source.md)
7. Lizenz und Veröffentlichung → [ADR-0082](../docs/adr/0082-repo-stays-private-for-now.md),
   [ADR-0100](../docs/adr/0100-publish-as-a-new-repository-with-one-start-commit.md), MIT
   ([ADR-0135](../docs/adr/0135-mit-license.md)), Arbeit im eigenen Repo mit geprüften Ständen auf GitHub
   ([ADR-0140](../docs/adr/0140-work-stays-private-the-public-repository-gets-snapshots.md))
8. Appliances ohne Agent (NAS, Firewall, Mesh-WLAN, Gateways)? → über einen Stellvertreter-Agenten in eigenem
   Container, nicht über die Leitstelle, mit Konfigurationssicherung vor jedem Update: Entwurf
   [proxy-agent.de.md](proxy-agent.de.md), [ADR-0089](../docs/adr/0089-appliances-through-a-proxy-agent.md),
   [ADR-0090](../docs/adr/0090-appliance-installs-need-a-fresh-config-backup.md)
