# phoedox-agent

Kleiner Agent für Server, VMs und LXC-Container. Er meldet Systemwerte, Pakete,
Container, Images und Stacks an eine [Phoedox](https://github.com/phoedox/phoedox)-Instanz
und führt freigegebene Aktionen aus (Container neu starten, Updates, Befehle).

- Verbindung **ausgehend** per WebSocket + mTLS – keine offenen Ports auf dem Server.
- Jede Aktion braucht drei Dinge: lokale Freigabe in `agent.yaml`, Bestätigung in der Phoedox-Oberfläche und die Signatur eines Admin-Schlüssels.
- Plattformen: Linux (amd64, arm64), Windows Server, macOS, FreeBSD/OPNsense/TrueNAS.

```bash
phoedox-agent collect   # aktuellen Stand als JSON ausgeben
```

Konfiguration: siehe [`agent.example.yaml`](agent.example.yaml).

## Lizenz

MIT – siehe [LICENSE](LICENSE).
