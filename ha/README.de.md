# Vördr – Home-Assistant-Integration

[English](README.md) · Deutsch

`custom_components/vordr` macht Home Assistant zu einem Client der Leitstelle (`vordr-hub`) über `/v1/ha`
([design/ha-integration.de.md](../design/ha-integration.de.md)). Die Leitstelle signiert jeden Auftrag und gibt Hosts
frei; nur wo ihre `hub.yaml` `ha_approves_hosts` setzt, darf ein Client mit der Rolle `admin` einen wartenden Host aus
Home Assistant freigeben
([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)). Die
Integration zeigt die Sichten der Leitstelle und fragt Befehle an.

## Einrichten

1. Über HACS, in Home Assistant als Marketplace angezeigt: nach „Vördr“ suchen, installieren und Home Assistant neu
   starten. HACS zeigt neue Versionen als Update. Fehlt „Vördr“ in der Liste, zuerst dieses Repository als
   benutzerdefiniertes Repository (Typ Integration) hinzufügen. Ohne HACS: `custom_components/vordr` nach
   `/config/custom_components/vordr` kopieren und Home Assistant neu starten.
2. Integration „Vördr“ hinzufügen: Adresse `wss://<leitstelle>:8443/v1/ha`, Name des Clients, bei eigener CA deren
   Zertifikat. Die Integration prüft die Erreichbarkeit in Stufen (Name, Verbindung, TLS, Endpunkt, Challenge) und
   nennt die scheiternde.
3. Sie zeigt Fingerprint und Befehl; an der Leitstelle freigeben:
   `vordr-hub approve-client <name> --fingerprint <fp>`. Danach erscheinen Geräte und Entitäten. Der Dialog wartet
   höchstens eine Stunde und darf geschlossen werden: Die Integration mit demselben Namen erneut hinzufügen setzt die
   Anfrage mit demselben Schlüssel fort, auch nach einem Neustart von Home Assistant
   ([ADR-0156](../docs/adr/0156-the-config-flow-keeps-its-key-until-the-client-is-set-up.md)).

## Was sie anlegt

| Gerät | Entitäten |
|---|---|
| „Vördr-Leitstelle“ bzw. „Vördr hub“ (Systemsprache) | `binary_sensor.vordr_hub_online`, `sensor.vordr_hub_hosts_online`, `_hosts_with_problem`, `_hosts_with_updates`, `_hosts_with_security_updates`, `_hosts_needing_reboot`, `_pending_approvals`, `_connection` (Diagnose) |
| „Vördr Home Assistant“ | `sensor.vordr_ha_updates_ready` „Updates bereit“: Updates von Home Assistant selbst, die die Regeln von Vördr jetzt durchlassen; `sensor.vordr_ha_updates_last_run` „Letzter Lauf“: was Vördr zuletzt installiert hat und wie es endete; `switch.vordr_ha_updates_paused` „Home-Assistant-Updates pausieren“. Nichts wird installiert, bevor es eingeschaltet ist (siehe unten) |
| „Vördr <node>“ je Host | `binary_sensor.vordr_<id>_online`, `_problem`, `_reboot_required`; `sensor.vordr_<id>_last_report`, `_last_run`; mit Paketquelle `update.vordr_<id>_system`, `sensor.vordr_<id>_pending_updates`, `_security_updates`; Knöpfe je nach Fähigkeit (`_check`, `_install_security`, `_reboot`, `_agent_update`, `_install_app_<app>`); mit `packages.containers` `update.vordr_<id>_containers` „Container-Images“, gemeldet; installiert nur, wenn agent.yaml Compose-Dienste erlaubt (`image:<projekt>/<dienst>` in `install.allowed_scopes`), und dann nur den Neubau ihres Tags (einen Dienst allein per Aktion `vordr.update_image`); „an“ nur, solange dieser Installieren-Knopf etwas nähme, ein neuerer Tag oder ein nicht erlaubter Dienst lässt sie also aus; `sensor.vordr_<id>_outdated_images` „Veraltete Images“ zählt jedes veraltete Image, auch die, die Handarbeit bleiben (Attribute `ready`, `installable`, `updates`); mit `Debian` oder `Alpine` in `osv.ecosystems` der Leitstelle auch bekannte Schwachstellen in den Images (Attribut `vulnerable`; „an“ nur, wenn ein neueres Image zum Installieren da ist, Schwachstellen allein schalten nicht an; die Zusammenfassung sagt je Image, ob ein neueres Image bereit ist; Meldungen zum Kernel des Images zählen nicht und stehen je Image als `kernel`) |

Unbekanntes bleibt unbekannt: eine gescheiterte Prüfung macht `update.*_system` und die Zähler „unbekannt“, nie
„aktuell“; ein Host ohne Neustart-Signal (Container) hat `reboot_required` „unbekannt“, nie „aus“.

**Ist alles aktuell?** Das Leitstellen-Gerät zählt über alle Hosts: `sensor.vordr_hub_hosts_with_updates` (Updates des
Systems, veraltete Images oder pip/npm-Pakete), `_hosts_with_security_updates` und `_hosts_needing_reboot`, je mit dem
Attribut `hosts`. Ein Host, dessen System-Prüfung scheiterte oder der noch nicht berichtet hat, steht im Attribut
`unknown` und zählt nicht; Images und Pakete zählen, wo der Host sie vergleicht. „0“ mit leerem `unknown` heisst, jeder
Host wurde geprüft und hat nichts zu aktualisieren, z. B. als Template-Bedingung
`{{ is_state('sensor.vordr_hub_hosts_with_updates', '0') and not state_attr('sensor.vordr_hub_hosts_with_updates', 'unknown') }}`.

**Für Automationen:**
- Ereignis `vordr_problem` `{node, active, reason, reasons, check_failed_code, message}` bei jedem neuen, geänderten
  oder beendeten Problem; nach einem HA-Neustart einmal für jedes noch bestehende.
- Ereignis `vordr_run_finished` `{node, job_id, verb, scope, state, message, summary}`, wenn ein Lauf endet; `summary`
  sagt bei einem Lauf, der Pakete angefasst hat, was er getan hat (`package_count`, `packages`, `seconds`, `log_file`
  am Host, `outdated_services`), sonst `{}`.
- `message` ist der Text des Reparaturhinweises in jeder mitgelieferten Sprache, z. B.
  `{{ trigger.event.data.message.de }}`; leer, wenn es nichts zu sagen gibt (Problem beendet, Lauf `ok`).
- Je Host-Problem ein Reparaturhinweis; er verschwindet mit dem Problem.
- Braucht ein Host einen Neustart, steht das ab der ersten Meldung unter den Reparaturhinweisen, bis er keinen mehr
  braucht. Wo Vördr neu starten darf, startet „Beheben“ den Neustart (wie der Neustart-Knopf); sonst sagt der Hinweis,
  dass von Hand neu zu starten ist. Nach `reboot_pending_after_days` der Leitstelle (Vorgabe 7) wird er zum Fehler und ersetzt den Problem-Hinweis `reboot_pending`
  ([ADR-0132](../docs/adr/0132-a-needed-reboot-is-a-repair-from-the-first-report.md)).
- Ausserhalb des Neustart-Fensters eines Hosts schicken der Neustart-Knopf und „Beheben“ nichts und sagen, ab wann der
  Neustart läuft; der Knopf zeigt das Fenster und die Uhrzeit als Attribute (`reboot_window`, `next_reboot`). Bei
  einem Host mit `reboot.window_only` hilft auch ein Grund nicht; bei jedem anderen startet die Aktion
  `vordr.press_outside_window` mit einem Grund sofort neu
  ([ADR-0187](../docs/adr/0187-the-agent-reports-its-reboot-window-and-home-assistant-says-when-a-reboot-can-run.md)).
- Ein gescheiterter Install, den die Leitstelle als Problem festhält, bis ein Install durchläuft, steht als
  Reparaturhinweis „erneut installieren“, wo Vördr installieren darf: Ist die Ursache behoben, installiert „Beheben“
  erneut, und ein Lauf, der durchläuft, löscht ihn, auch wenn nichts mehr zu installieren ist
  ([ADR-0148](../docs/adr/0148-a-kept-failed-install-is-a-repair-that-installs-again.md)).
- Ein Host, der an der Leitstelle auf Freigabe wartet, steht als Reparaturhinweis (eine Warnung) mit dem Fingerprint,
  den die Leitstelle gesehen hat, und dem Befehl für die Leitstelle, `vordr-hub approve <node> --fingerprint <fp>`.
  Zuerst den Fingerprint mit `vordr-agent identity` auf dem Host vergleichen. Der Hinweis verschwindet, sobald der Host
  freigegeben, abgelehnt oder vergessen ist
  ([ADR-0157](../docs/adr/0157-a-waiting-host-is-a-repair-that-names-the-approval-command.md)). Wo die `hub.yaml` der
  Leitstelle `ha_approves_hosts: true` setzt und dieses Home Assistant die Rolle `admin` hat
  (`vordr-hub client-role <name> --role admin`), nennt die Leitstelle `approve_host` in ihrem Status, und der Hinweis
  lässt sich hier beheben: „Beheben“ fragt nach dem Fingerprint, den `vordr-agent identity` auf dem Host ausgegeben hat
  (Leerzeichen und Grossbuchstaben spielen keine Rolle), lehnt einen ab, der von dem der Leitstelle abweicht, ohne etwas
  zu senden, und bittet sonst die Leitstelle, den Host freizugeben. Ohne das kann Home Assistant nicht freigeben;
  Ablehnen und Vergessen bleiben in jedem Fall an der Leitstelle
  ([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)).
- Verwundbare pip/npm-Pakete machen nur dann ein Problem, wenn jetzt jemand die Behebung nehmen kann: per Knopf, per
  Auffrischen der Abhängigkeiten oder von Hand. Wartet ein Fund auf andere (ein Paket oder eine Projektdatei hält die
  Version fest, es gibt noch keine Behebung, oder sie ist jünger als das Mindestalter), steht er nur am Sensor
  `_vulnerable_packages`: je Fund `reach` und `held_by`, dazu `waiting`
  ([ADR-0129](../docs/adr/0129-findings-say-where-their-fix-is.md)).
- Mit einem Abschnitt `kev:` in `hub.yaml` der Leitstelle trägt ein Fund, dessen CVE im CISA-Katalog der Known
  Exploited Vulnerabilities steht, `known_exploited: true`, `kev_cves`, `kev_added` (wann CISA sie aufnahm) und, bei
  Einsatz in Ransomware-Kampagnen, `kev_ransomware: true`; solche Funde stehen gleich nach der Schadsoftware.
  `_vulnerable_packages` zählt sie je Host im Attribut `known_exploited`, und `sensor.vordr_hub_vulnerability_data`
  zeigt den Zustand des Katalogs in `kev` (`stale`: älter als `stale_hours`, neuere Einträge fehlen vielleicht). Die
  Markierungen ändern kein Problem und keinen Zeitpunkt
  ([ADR-0168](../docs/adr/0168-known-exploitation-comes-from-the-kev-catalog-fetched-whole-at-the-hub.md)); eine
  Automation kann sie nutzen, z. B. `{{ state_attr('sensor.vordr_<id>_vulnerable_packages', 'known_exploited') }}`.
- Ereignis `vordr_ai_explanation` mit der freiwilligen KI-Erklärung eines gescheiterten Laufs (siehe
  „KI-Erklärungen gescheiterter Läufe“).

## Updates von Home Assistant selbst

Home Assistant kann seine eigenen Updates installieren – Core, OS, Apps, HACS, ESPHome, Geräte-Firmware –, aber ohne
Regeln. Unter *Einstellungen → Geräte & Dienste → Vördr → Konfigurieren → Home-Assistant-Updates* werden die Regeln von
Vördr dafür festgelegt
([ADR-0158](../docs/adr/0158-home-assistants-own-updates-follow-vordrs-rules-in-the-integration.md)):

- **Erlaubte Arten** (Core, OS, Supervisor, Apps, HACS, ESPHome, Geräte-Firmware, Firmware des Funkadapters, andere)
  und einzelne Update-Entitäten. Nichts gewählt, nichts erlaubt.
- **Fenster:** Tage, Beginn und Ende in der Zeitzone von Home Assistant; über Mitternacht gehört es zum Tag, an dem es
  beginnt.
- **Mindestalter:** standardmässig 7 Tage (1–90), gezählt ab dem Zeitpunkt, an dem Vördr die Version zum ersten Mal
  gesehen hat, weil Home Assistant kein Erscheinungsdatum nennt
  ([ADR-0159](../docs/adr/0159-the-minimum-age-of-a-home-assistant-update-counts-from-its-first-sight.md)). Was beim
  ersten Lauf dieser Version der Integration offen ist, beginnt sein Alter dann. Eine neuere Version derselben Linie
  (gleiche erste zwei Zahlen, etwa 2026.11.2 nach 2026.11.1) zählt ab dem Zeitpunkt, an dem Vördr die Linie zum ersten
  Mal gesehen hat, muss aber selbst 2 Tage alt sein
  ([ADR-0179](../docs/adr/0179-a-newer-release-of-a-version-line-inherits-the-lines-age-with-two-days-of-its-own.md)).
  Geräte-Firmware und die Firmware des Funkadapters warten mindestens 14 Tage
  ([ADR-0180](../docs/adr/0180-device-firmware-and-the-radio-adapter-wait-at-least-14-days.md)).
- **Automatisch installieren:** standardmässig aus. Bis es eingeschaltet ist, plant Vördr nur.

**Es installiert erst, wenn es eingeschaltet ist.** Der Sensor `sensor.vordr_ha_updates_ready` (Gerät „Vördr Home
Assistant“) zählt die Updates, die die Regeln jetzt durchlassen, auch solange das Installieren aus ist. Seine Attribute
sagen, wie viele es je Grund sind, und je Update-Entität, warum nicht und ab wann (`updates`: `code`, `earliest`,
`first_seen`). Nie angefasst: die Entitäten von Vördr selbst, eine in Home Assistant übersprungene Version (`skipped`),
ein Update, das sich selbst installiert
(`self_updating`), und eine neue Hauptversion, die Handarbeit bleibt (`major`,
[ADR-0160](../docs/adr/0160-a-new-major-version-of-a-home-assistant-update-stays-manual.md)). Der Sensor arbeitet ohne
Leitstelle.

**Installieren** („Automatisch installieren“ im selben Formular, standardmässig aus; [ADR-0162](../docs/adr/0162-home-assistants-own-updates-install-only-when-switched-on-and-a-pause-holds-them.md)):

- Ein Update nach dem anderen, im Fenster, nur was in diesem Moment `ready` ist: Der Plan wird direkt vor jeder
  Installation nochmals geprüft. Andere Arten zuerst; Core, OS und Supervisor zuletzt, eines davon je Öffnung des
  Fensters, weil sie Home Assistant oder die Maschine neu starten ([ADR-0164](../docs/adr/0164-home-assistant-updates-install-one-at-a-time-with-core-os-and-supervisor-last.md)).
- Vor jedem ein Backup: `update.install` mit `backup`, wo sich die Entität selbst sichern kann (Core, OS, Apps); für
  die übrigen (HACS, ESPHome, der Supervisor) muss das letzte automatische Backup von Home Assistant höchstens
  26 Stunden alt sein, sonst erstellt Vördr eins mit `backup.create_automatic`. Ohne Backup keine Installation
  ([ADR-0163](../docs/adr/0163-a-backup-before-every-automatic-home-assistant-update.md)). Geräte-Firmware und
  Funkadapter brauchen keines: Ein Backup von Home Assistant kann ein Gerät nicht zurückholen
  ([ADR-0181](../docs/adr/0181-device-firmware-and-the-radio-adapter-install-without-a-backup-of-home-assistant.md)).
- Der Schalter `switch.vordr_ha_updates_paused` „Home-Assistant-Updates pausieren“ hält jede Installation an, die noch
  nicht begonnen hat, auch über Neustarts. Nur ein Vördr-Eintrag (einer je Leitstelle) darf installieren.
- Nach der Installation wartet Vördr, bis die Entität die neue Version meldet: 30 Minuten, Geräte-Firmware und
  Funkadapter 2 Stunden
  ([ADR-0183](../docs/adr/0183-device-firmware-and-the-radio-adapter-get-two-hours-to-report-their-version.md)). Ein
  Core- oder OS-Update startet Home Assistant neu: Der Lauf ist festgehalten, und nach dem Neustart wird zuerst sein Ergebnis geprüft.
- Der Sensor `sensor.vordr_ha_updates_last_run` „Letzter Lauf“ zeigt die Phase, dann das Ergebnis (`ok`, `failed`,
  `not_reached`, `timeout`, `backup_failed`, `skipped_paused`, `skipped_not_ready`), die Versionen, das Backup und was
  das Installieren gerade tut (`installer`), mit den bereiten Updates in ihrer Reihenfolge (`queue`).
- Ereignis `vordr_ha_update_finished` `{entity_id, kind, from_version, to_version, installed_version, result, reason,
  backup, started, finished, restarted}`, wenn ein Lauf endet.
- Eine gescheiterte Installation (`failed`, `not_reached`, `timeout`) ist ein Reparaturhinweis und wird nicht wieder
  versucht, bis eine neuere Version kommt, bis sie von Hand installiert oder der Hinweis bestätigt ist: Dann
  installiert Vördr sie sofort, ausserhalb des Fensters, aber unter allen anderen Regeln. Läuft gerade eine andere
  Installation, ist das Installieren pausiert oder aus, oder hält eine Regel es auf, sagt der Hinweis warum, und nichts
  wird in eine Warteschlange gestellt
  ([ADR-0182](../docs/adr/0182-the-repairs-fix-installs-a-failed-home-assistant-update-at-once.md)). Der Hinweis
  verschwindet, sobald eine Installation von ihr durchläuft
  ([ADR-0165](../docs/adr/0165-a-failed-home-assistant-update-stays-until-an-install-of-it-goes-through.md)). Ein gescheitertes Backup ist ein eigener Hinweis, bis Home Assistant ein neueres automatisches Backup hat.
- Noch nicht: zuerst ein Gerät je Modell vor den übrigen dieses Modells (eine spätere Änderung).

## Den Agenten auf einem neuen Linux-Host installieren

Home Assistant kann einen neuen Host selbst einrichten: Es erreicht ihn einmal über SSH, lässt ihn das signierte
Agenten-Release von der Leitstelle holen, prüft es und führt dort `vordr-agent install` aus. Vom SSH-Zugang wird nichts
gespeichert
([ADR-0161](../docs/adr/0161-home-assistant-installs-the-agent-once-over-ssh-and-keeps-no-credentials.md)), und auch
der Host nicht: Die Liste der Hosts führt die Leitstelle, und der Host erscheint in Home Assistant, sobald er dort
freigegeben ist
([ADR-0185](../docs/adr/0185-the-agent-install-starts-from-an-add-host-button-and-stores-no-subentry.md)). Für
Linux mit systemd und apt (Debian, Ubuntu, Raspberry Pi OS) auf x86_64, aarch64 oder armv7.

Was es braucht:

- eine Leitstelle, die das Agenten-Release herausgibt (ihr Status nennt `agent_download`), mit einem signierten Release
  darauf (`deploy/README.de.md`, „Agent-Releases signieren“);
- dieses Home Assistant an der Leitstelle mit der Rolle `admin` freigegeben: `vordr-hub client-role <name> --role
  admin`;
- den Betreiberschlüssel (und seinen Ersatz, falls es einen gibt), einmal eingegeben unter *Einstellungen → Geräte &
  Dienste → Vördr → Konfigurieren → Betreiberschlüssel*, wie `vordr-release public-key` sie ausgibt. Home Assistant
  prüft jedes Release dagegen und nimmt nie einen Schlüssel von der Leitstelle;
- auf dem Host: SSH, `root` oder einen Benutzer mit `sudo`, `curl` oder `wget`, und die Leitstelle erreichbar unter der
  Adresse, die diese Integration verwendet;
- freiwillig, für die Freigabe im selben Dialog: `ha_approves_hosts: true` in der `hub.yaml` der Leitstelle.

Dann auf der Seite der Integration *Host hinzufügen* drücken (*Einstellungen → Geräte & Dienste → Vördr*; bei mehreren
Leitstellen beim Eintrag der Leitstelle, zu der der Host gehören soll). Fehlt etwas, das es braucht, sagt der Dialog
das, bevor er nach etwas fragt: die Integration läuft nicht, kein Betreiberschlüssel, die Leitstelle nicht verbunden
oder sie gibt das Release nicht heraus, die SSH-Bibliothek fehlt. Sonst:

1. Host, SSH-Port, Benutzer und Name des Knotens (leer: der eigene Name des Hosts);
2. der Host-Schlüssel, den der Server zeigt: an der Konsole des Hosts mit dessen eigenem vergleichen
   (der Dialog nennt die Datei, z. B. `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`; er fragt zuerst nach ED25519)
   und nur bestätigen, wenn beide gleich sind;
3. ein Passwort oder ein privater Schlüssel (mit Passphrase) und das Passwort für sudo, falls es vom Passwort der
   Anmeldung abweicht. Wenn möglich einen Schlüssel oder ein Passwort eigens dafür verwenden: Home Assistant hält
   Passwort und Schlüssel der Anmeldung im Speicher, bis es angemeldet ist, das Passwort für sudo, bis die Installation
   beginnt, und speichert sie nirgends. Wird der Dialog während der Anmeldung geschlossen, hört alles auf: Auf dem Host
   läuft nichts;
4. Home Assistant prüft den Host (Linux, Prozessor, systemd, apt, die Werkzeuge, sudo), holt das Release, prüft seine
   Signatur (und dass es nicht älter ist als eines, das eine frühere Installation genommen hat), und der Host lädt als
   eigener Dienst (`systemd-run`), der auch fertig macht, wenn die Verbindung endet, das Binary in einen privaten
   temporären Ordner, vergleicht Grösse und SHA-256 mit dem signierten Manifest und führt erst dann
   `vordr-agent install` aus. Der Ordner wird in jedem Fall entfernt.

Am Ende zeigt der Dialog den Fingerprint des Agenten. Wo die Leitstelle `approve_host` nennt (oben), wartet der Dialog
zuerst bis zu einer Minute, bis die Leitstelle den neuen Host mit genau diesem Fingerprint als wartend aufführt (unter
dem eingegebenen Namen des Knotens, falls einer eingegeben wurde), und
gibt ihn dann frei: Der Fingerprint kam über die SSH-Verbindung, deren Host-Schlüssel du bestätigt hast, der Vergleich
ist also durch den Ablauf gemacht
([ADR-0192](../docs/adr/0192-an-admin-home-assistant-may-approve-a-waiting-host-when-the-hub-allows-it.md)). Sein Ende
sagt dann, dass der Host freigegeben ist, oder, falls die Leitstelle abgelehnt hat, ihren Code. Sonst, oder wenn der
Host nicht rechtzeitig erscheint, wartet der Host auf die Freigabe an der Leitstelle; sobald die Leitstelle ihn
aufführt, zeigt der Reparaturhinweis „Host … wartet auf Freigabe“ den Befehl. Fingerprint vergleichen, dann an der
Leitstelle freigeben (oder den Hinweis beheben, wo die Leitstelle es erlaubt); danach erscheint der Host als Gerät der
Integration. Der Knopf legt für
den Host keinen eigenen Eintrag an, die Seite der Integration führt also keinen auf. Jedes andere Ende nennt, was auf
dem Host geschehen ist und was als Nächstes zu tun ist. Ein Host mit eigener Richtlinie wird weiter von Hand eingerichtet (`deploy/README.de.md`); der so
installierte Agent prüft und meldet und installiert nichts, bis seine `agent.yaml` es erlaubt.

## KI-Erklärungen gescheiterter Läufe

Freiwillig und ab Werk aus
([ADR-0166](../docs/adr/0166-optional-ai-explanations-of-failed-runs-go-through-home-assistants-ai-task-and-only-advise.md)).
Scheitert ein Lauf auf einem Host, erklärt ihn eine selbst gewählte AI-Task-Entität: ein paar Sätze in der Sprache von
Home Assistant, eine wahrscheinliche Ursache und bis zu drei nächste Schritte aus festen Listen und ein Zitat aus dem
Logauszug des Laufs als Beleg. Sie berät nur: Sie ändert nie Fenster, Mindestalter, Umfang, Knöpfe oder
Reparaturhinweise, und die Leitstelle weiss nichts davon.

Nötig ist eine AI-Task-Entität, die Daten erzeugen kann, aus einer beliebigen Integration, die eine anbietet (ein
lokales Modell oder ein Cloud-Dienst). Dann *Einstellungen → Geräte & Dienste → Vördr → Konfigurieren →
KI-Erklärungen gescheiterter Läufe*: die Entität wählen und bestätigen, dass Paketnamen und Logauszüge gescheiterter
Läufe an ihren Anbieter gehen dürfen. Die Entität leeren schaltet es aus.

**Was an den Anbieter geht**, je gescheitertem Lauf: der Ergebniscode, die Aktion, die Paketnamen und das Ende des
Logauszugs (höchstens 2000 Zeichen). Vorher werden die Adresse der Leitstelle, die Namen der Hosts, die die Leitstelle
kennt (mit ihrer Domain), Namen in der Domain der Leitstelle, der Server jeder URL samt Benutzer und Passwort und
IP-Adressen durch Platzhalter ersetzt; die übrigen Wörter des Auszugs gehen unverändert. Ein Anbieter, der ein
Cloud-Dienst ist, sieht sie, auch hinter einer Integration für lokale Modelle: prüfen, wohin die Entität ihre Daten
schickt.

**Welche Läufe:** Fehlschläge, deren Log die Ursache nennt (Paketverwaltung, Prüfung nach dem Update, Verifikation,
Speicherplatz, Paketquelle, Backup, Zeitüberschreitung, Unterbruch). Nicht: Läufe, die mit `ok` endeten, Ablehnungen,
die Entscheidungen der Regeln von Vördr (zu neu, markiert, nicht aus der Registry) und Sicherheitssignale (ein Hash,
ein Zertifikat oder ein Host-Schlüssel, der nicht passt): Die erklärt keine KI weg.

**Das Ergebnis:**

- Ereignis `vordr_ai_explanation` `{node, job_id, state, code, entity_id}`; bei `code` `ok` dazu `likely_cause`,
  `next_steps`, `explanation` und `quote`. Solange eingeschaltet, kommt es für jeden solchen Lauf, auch wenn keine
  Antwort zu haben ist, so wartet eine Automation, die darauf wartet, nicht vergebens; ein Push kann
  `{{ trigger.event.data.explanation }}` anfügen, wenn `code` `ok` ist. Solange aus, wird nichts gesendet und kein
  Ereignis kommt
  ([ADR-0184](../docs/adr/0184-with-ai-explanations-off-nothing-is-sent-and-no-event-is-fired.md)).
- Der Sensor „Letzter Lauf“ des Hosts zeigt `ai_code`, `ai_entity_id` und bei `ok` die Felder (`ai_likely_cause`,
  `ai_next_steps`, `ai_explanation`, `ai_quote`), solange er diesen Lauf zeigt; ausserhalb des Recorders.
- `code`: `ok`; `unverified` (das Zitat steht nicht im Log, wie es gesendet wurde: nichts von der Antwort wird
  gezeigt); `invalid_answer` (ein Feld fehlt, oder ein Wert steht nicht in seiner Liste); `no_answer` (keine innert 60
  Sekunden, oder der Anbieter ist gescheitert); `no_excerpt` (der Lauf hat keinen Logauszug); `unsupported` (die
  Entität kann keine Daten erzeugen, oder AI Task ist nicht eingerichtet); `entity_missing` (die Entität ist weg oder
  nicht verfügbar); `not_configured` (die gespeicherten Optionen sind nicht gültig).

Nur `likely_cause` und `next_steps` stammen aus festen Listen: Eine Automation stützt sich auf diese, nie auf den Text.
Die Erklärung ist KI-Text, nicht weiter geprüft als darauf, dass ihr Zitat im Log steht, und ein Log lässt sich so
bauen, dass ein Modell alles sagt. Eine Frage nach der anderen, jeder Lauf einmal; derselbe Fehlschlag auf mehreren
Hosts wird einmal gefragt.

## Blueprints

Vier Automations-Blueprints in [`blueprints/automation/vordr/`](../blueprints/automation/vordr/) machen aus den
Ereignissen und Entitäten oben Benachrichtigungen
([ADR-0173](../docs/adr/0173-notifications-are-shipped-as-blueprints-the-user-imports.md),
[Entwurf](../design/ha-integration.de.md)). Sie melden nur; keiner installiert oder startet von sich aus neu.

- `problem.yaml`: Das Problem eines Hosts beginnt oder ändert sich, mit Grund und nächstem Schritt; optional
  Wartezeit, ignorierte Gründe, ein Hinweis beim Ende.
- `run_failed.yaml`: Ein Lauf endet mit einem anderen Ergebnis als `ok`.
- `reboot_offer.yaml`: Ein Host braucht einen Neustart; wo Vördr ihn neu starten darf, hat die Benachrichtigung die
  Aktion „Jetzt neu starten“, die auf einen Tipp den Neustart-Knopf des Hosts drückt (Companion-App); bei geschlossenem
  Neustart-Fenster sagt der Tipp stattdessen, ab wann.
- `hub_offline.yaml`: Die Online-Entität der Leitstelle ist seit N Minuten aus.

Import: *Einstellungen → Automatisierungen & Szenen → Blueprints → Blueprint importieren*, mit der Adresse der Datei im
Repository, zum Beispiel `https://github.com/<owner>/<repo>/blob/main/blueprints/automation/vordr/problem.yaml`
([Home Assistant: Blueprints verwenden](https://www.home-assistant.io/docs/automation/using_blueprints/)); oder die
Dateien nach `/config/blueprints/automation/vordr/` kopieren. Dann eine Automation aus dem Blueprint anlegen und den
Benachrichtigungsdienst wählen, zum Beispiel `notify.mobile_app_<Gerät>`. Home Assistant braucht dafür 2024.10 oder
neuer.

## Sprachen

Die Leitstelle schickt nur Codes und Zahlen, die Texte stehen in `translations/*.json` (de, en; `strings.json` =
`en.json`). Welche Sprache wo gilt, legt HA fest:

| Was | Sprache |
|---|---|
| Einrichtung und Optionen, Fehler, Zustände (`last_run`, `hub_connection`, `ha_updates_last_run`), Attributwerte von `_problem` (`reason`, `next_step`, `check_failed_code`) und von `_last_run` (`ai_code`, `ai_likely_cause`, `ai_next_steps`), die Gründe von `ha_updates_ready`, Reparaturhinweise | die Sprache im Profil der lesenden Person |
| Der Text einer KI-Erklärung (`explanation`) | die Systemsprache von HA, in der das Modell schreiben soll |
| Entitäts- und Gerätenamen, `latest_version` und Zusammenfassung der update-Entität | die Systemsprache von HA (Namen setzt HA beim Start) |
| `message` in den Ereignissen | alle mitgelieferten Sprachen; die Automation wählt |

Eine weitere Sprache ist eine weitere Datei in `translations/`; `tests/test_texts.py` prüft, dass jede Datei jeden
Text hat.

## Tests

Der Code liegt in `custom_components/vordr/` an der Wurzel des Repos, wo HACS ihn sucht; dieser Ordner enthält die
Tests und ihre Abhängigkeiten ([ADR-0144](../docs/adr/0144-the-integration-lives-at-the-repository-root.md)). Unter
Linux mit `pytest-homeassistant-custom-component`; welche HA-Version die Tests nehmen, legen die festen Versionen in
`requirements_test.txt` fest. Aufruf von der Wurzel des Repos aus:

```bash
uv venv --python 3.14 ~/vordr-ha-venv
uv pip install --python ~/vordr-ha-venv/bin/python -r ha/requirements_test.txt
~/vordr-ha-venv/bin/ruff check custom_components ha && ~/vordr-ha-venv/bin/ruff format --check custom_components ha \
  && ~/vordr-ha-venv/bin/python -m pytest -q
```

`tests/test_e2e_real_hub.py` startet die echten Binaries aus `../dist` (vorher `./build.sh`): Leitstelle, einen Agenten,
der diese Maschine prüft, und die Integration – Freigabe per CLI, Knopf bis signierter Auftrag, Widerruf.
