# phoedox-agent

Kleiner Agent für Server, VMs und LXC-Container. Er meldet Systemwerte, Pakete,
Container, Images und Stacks an eine [Phoedox](https://github.com/phoedox/phoedox)-Instanz
und führt freigegebene Aktionen aus (Container neu starten, Updates, Befehle).

- Verbindung **ausgehend** per WebSocket + mTLS – keine offenen Ports auf dem Server.
- Jede Aktion braucht drei Dinge: lokale Freigabe in `agent.yaml`, Bestätigung in der Phoedox-Oberfläche und die Signatur eines Admin-Schlüssels.
- Plattformen: Linux (amd64, arm64), Windows Server, macOS, FreeBSD/OPNsense/TrueNAS.

## Stufe 1: lesende Meldungen (ADR-0033)

Projekt-Besitzer erzeugen in Phoedox unter *Einstellungen → Agenten* einen Code
(24 Stunden gültig, bis zu 20 Hosts). Auf dem Host:

```bash
phoedox-agent enroll https://app.phoedox.com phxe_…
```

```bash
phoedox-agent run
```

`enroll` legt `credentials.json` (Modus 0600) in `/etc/phoedox-agent` ab
(`PHOEDOX_AGENT_DIR` ändert den Ordner). `run` meldet jede Minute per HTTPS:
Hostname, Betriebssystem, CPU, RAM, Datenträger, IP-Adressen, ob der Host eine VM
oder ein LXC ist, installierte Pakete (dpkg, apk, rpm), die Docker-Version und die
Container mit ihren Compose-Projekten (über `/var/run/docker.sock`, nur lesend).
Phoedox baut daraus den Software-Baum: Einträge `server`/`vm`/`lxc`, wichtige Software
(Docker, Proxmox VE, Portainer, Webserver, Datenbanken …) mit `installed_on`, Stacks mit
`runs_on`/`managed_by` und Container mit `part_of`;
von Hand gesetzte Namen und Attribute bleiben erhalten. Gesperrte Agenten beenden sich.

Als Dienst: [`deploy/phoedox-agent.service`](deploy/phoedox-agent.service) nach
`/etc/systemd/system/` kopieren, dann `systemctl enable --now phoedox-agent`.

Weitere Befehle: `phoedox-agent collect` (Stand als JSON ausgeben), `phoedox-agent report`
(eine Meldung senden), `phoedox-agent policy` (erlaubte Aktionen und vertraute Schlüssel).
`DOCKER_HOST=unix:///run/podman/podman.sock` nutzt einen anderen Socket, z. B. Podman.

## Aktionen (ADR-0036)

Container neu starten, Compose-Stacks oder einzelne Dienste aktualisieren
(`docker compose pull` + `up -d`), System-Updates (apt, dnf, apk) und freigegebene Befehle.
Jede Aktion braucht drei Dinge:

1. **Freigabe auf dem Host** in `/etc/phoedox-agent/agent.yaml` unter `allow`
   (Vorlage: [`agent.example.yaml`](agent.example.yaml)).
2. **Bestätigung in Phoedox** auf der Seite des Containers, Stacks oder Hosts.
3. **Signatur** mit einem Ed25519-Schlüssel, den du unter *Sicherheit → Schlüssel für
   Agent-Aufträge* in deinem Browser erzeugst. Der private Schlüssel verlässt den Browser
   nie; den öffentlichen trägst du unter `adminKeys` ein. Phoedox selbst kann keine
   Aufträge signieren.

Aufträge laufen 10 Minuten ab und werden nur einmal ausgeführt (`jobs-done.json`).
Der Agent holt sie per Long-Poll über HTTPS ab; es bleibt bei ausgehenden Verbindungen.

## Lizenz

MIT – siehe [LICENSE](LICENSE).
