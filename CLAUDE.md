# phoedox-agent

Agent für Server/VMs/LXC. Gesamtentscheidungen: `phoedox/phoedox` → `docs/decisions.md` (Abschnitt Agent).

## Aufbau

- `cmd/phoedox-agent` – CLI (`collect`, `enroll`, `run`, `version`)
- `internal/collect` – Lesen; plattformspezifisch per Build-Tags (`collect_linux.go`, weitere folgen)
- `internal/policy` – Sicherheitsprüfung für Aktionen: lokale Allowlist + ablaufender, ed25519-signierter Auftrag
- Geplant: `internal/conn` (WebSocket + mTLS, Wiederverbinden), `internal/docker`, `internal/proxmox`, `internal/pkg` (apt/dnf/…), `internal/discovery` (abschaltbar), `internal/update` (signiertes Selbst-Update)

## Regeln

- **Sicherheit vor Komfort:** Keine Aktion ohne `policy.Check`. Neue Aktionen bekommen eine Konstante in `policy` und Tests für erlaubt/verboten/abgelaufen/manipuliert.
- Keine eingehenden Ports. Nur ausgehende Verbindungen zur konfigurierten Instanz.
- Muss für linux/amd64, linux/arm64, windows/amd64, darwin/*, freebsd/amd64 bauen (CI prüft das). Kein CGO.
- Lesen darf fehlschlagen, ohne den Agenten zu stoppen: fehlende Werte leer lassen.
- Commits nach Conventional Commits. Lizenz MIT. Code und Kommentare Englisch.
