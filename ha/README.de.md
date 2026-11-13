# Vördr – Home-Assistant-Integration

[English](README.md) · Deutsch

`custom_components/vordr` macht Home Assistant zu einem Client der Leitstelle (`vordr-hub`) über `/v1/ha`
([design/ha-integration.de.md](../design/ha-integration.de.md)). Die Leitstelle bleibt der einzige Ort, der Hosts freigibt
und Aufträge signiert; die Integration zeigt ihre Sichten und fragt Befehle an.

## Einrichten

1. Über HACS: dieses Repository als benutzerdefiniertes Repository (Typ Integration) hinzufügen, „Vördr“
   installieren, Home Assistant neu starten. HACS zeigt neue Versionen als Update. Ohne HACS: `custom_components/vordr`
   nach `/config/custom_components/vordr` kopieren und Home Assistant neu starten.
2. Integration „Vördr“ hinzufügen: Adresse `wss://<leitstelle>:8443/v1/ha`, Name des Clients, bei eigener CA deren
   Zertifikat. Die Integration prüft die Erreichbarkeit in Stufen (Name, Verbindung, TLS, Endpunkt, Challenge) und
   nennt die scheiternde.
3. Sie zeigt Fingerprint und Befehl; an der Leitstelle freigeben:
   `vordr-hub approve-client <name> --fingerprint <fp>`. Danach erscheinen Geräte und Entitäten.

## Was sie anlegt

| Gerät | Entitäten |
|---|---|
| „Vördr-Leitstelle“ bzw. „Vördr hub“ (Systemsprache) | `binary_sensor.vordr_hub_online`, `sensor.vordr_hub_hosts_online`, `_hosts_with_problem`, `_pending_approvals`, `_connection` (Diagnose) |
| „Vördr <node>“ je Host | `binary_sensor.vordr_<id>_online`, `_problem`, `_reboot_required`; `sensor.vordr_<id>_last_report`, `_last_run`; mit Paketquelle `update.vordr_<id>_system`, `sensor.vordr_<id>_pending_updates`, `_security_updates`; Knöpfe je nach Fähigkeit (`_check`, `_install_security`, `_reboot`, `_agent_update`, `_install_app_<app>`); mit `packages.containers` `update.vordr_<id>_containers` „Container-Images“, gemeldet; installiert nur, wenn agent.yaml Compose-Dienste erlaubt (`image:<projekt>/<dienst>` in `install.allowed_scopes`), und dann nur den Neubau ihres Tags (einen Dienst allein per Aktion `vordr.update_image`); „an“ nur, solange dieser Installieren-Knopf etwas nähme, ein neuerer Tag oder ein nicht erlaubter Dienst lässt sie also aus; `sensor.vordr_<id>_outdated_images` „Veraltete Images“ zählt jedes veraltete Image, auch die, die Handarbeit bleiben (Attribute `ready`, `installable`, `updates`); mit `Debian` oder `Alpine` in `osv.ecosystems` der Leitstelle auch bekannte Schwachstellen in den Images (Attribut `vulnerable`; „an“ nur, wenn ein neueres Image zum Installieren da ist, Schwachstellen allein schalten nicht an; die Zusammenfassung sagt je Image, ob ein neueres Image bereit ist; Meldungen zum Kernel des Images zählen nicht und stehen je Image als `kernel`) |

Unbekanntes bleibt unbekannt: eine gescheiterte Prüfung macht `update.*_system` und die Zähler „unbekannt“, nie
„aktuell“; ein Host ohne Neustart-Signal (Container) hat `reboot_required` „unbekannt“, nie „aus“.

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
- Ein gescheiterter Install, den die Leitstelle als Problem festhält, bis ein Install durchläuft, steht als
  Reparaturhinweis „erneut installieren“, wo Vördr installieren darf: Ist die Ursache behoben, installiert „Beheben“
  erneut, und ein Lauf, der durchläuft, löscht ihn, auch wenn nichts mehr zu installieren ist
  ([ADR-0148](../docs/adr/0148-a-kept-failed-install-is-a-repair-that-installs-again.md)).
- Verwundbare pip/npm-Pakete machen nur dann ein Problem, wenn jetzt jemand die Behebung nehmen kann: per Knopf, per
  Auffrischen der Abhängigkeiten oder von Hand. Wartet ein Fund auf andere (ein Paket oder eine Projektdatei hält die
  Version fest, es gibt noch keine Behebung, oder sie ist jünger als das Mindestalter), steht er nur am Sensor
  `_vulnerable_packages`: je Fund `reach` und `held_by`, dazu `waiting`
  ([ADR-0129](../docs/adr/0129-findings-say-where-their-fix-is.md)).

## Sprachen

Die Leitstelle schickt nur Codes und Zahlen, die Texte stehen in `translations/*.json` (de, en; `strings.json` =
`en.json`). Welche Sprache wo gilt, legt HA fest:

| Was | Sprache |
|---|---|
| Einrichtung, Fehler, Zustände (`last_run`, `hub_connection`), Attributwerte von `_problem` (`reason`, `next_step`, `check_failed_code`), Reparaturhinweise | die Sprache im Profil der lesenden Person |
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
