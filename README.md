# phoedox-agent

Small agent for servers, VMs and LXC containers. It reports system values, packages,
containers, images and stacks to a [Phoedox](https://github.com/phoedox/phoedox) instance
and runs approved actions (restart containers, updates, commands).

- Connection is **outgoing** via WebSocket + mTLS – no open ports on the server.
- Every action needs three things: local approval in `agent.yaml`, confirmation in the Phoedox interface and the signature of an admin key.
- Platforms: Linux (amd64, arm64), Windows Server, macOS, FreeBSD/OPNsense/TrueNAS.

## Stage 1: read-only reports (ADR-0033)

Project owners create a code in Phoedox under *Settings → Agents*
(valid for 24 hours, up to 20 hosts). On the host:

```bash
phoedox-agent enroll https://app.phoedox.com phxe_…
```

```bash
phoedox-agent run
```

`enroll` stores `credentials.json` (mode 0600) in `/etc/phoedox-agent`
(`PHOEDOX_AGENT_DIR` changes the folder). `run` reports every minute via HTTPS:
host name, operating system, CPU, RAM, disks, IP addresses, whether the host is a VM
or an LXC, installed packages (dpkg, apk, rpm), the Docker version and the
containers with their Compose projects (via `/var/run/docker.sock`, read-only).
From this, Phoedox builds the software tree: entries `server`/`vm`/`lxc`, important software
(Docker, Proxmox VE, Portainer, web servers, databases …) with `installed_on`, stacks with
`runs_on`/`managed_by` and containers with `part_of`;
names and attributes set by hand are kept. Blocked agents shut down.

As a service: copy [`deploy/phoedox-agent.service`](deploy/phoedox-agent.service) to
`/etc/systemd/system/`, then run `systemctl enable --now phoedox-agent`.

More commands: `phoedox-agent collect` (print the state as JSON), `phoedox-agent report`
(send one report), `phoedox-agent policy` (allowed actions and trusted keys).
`DOCKER_HOST=unix:///run/podman/podman.sock` uses a different socket, e.g. Podman.

## Actions (ADR-0036)

Restart containers, update Compose stacks or individual services
(`docker compose pull` + `up -d`), system updates (apt, dnf, apk), approved commands and its own update (`agent.update`: the binary comes from the instance, its SHA-256 is part of the signed job; the old binary stays as `.old`).
Every action needs three things:

1. **Approval on the host** in `/etc/phoedox-agent/agent.yaml` under `allow`
   (template: [`agent.example.yaml`](agent.example.yaml)).
2. **Confirmation in Phoedox** on the page of the container, stack or host.
3. **Signature** with an Ed25519 key that you create in your browser under *Security → Keys for
   agent jobs*. The private key never leaves the browser;
   you enter the public key under `adminKeys`. Phoedox itself cannot
   sign jobs.

Jobs expire after 10 minutes and are run only once (`jobs-done.json`).
The agent fetches them via long poll over HTTPS; connections stay outgoing only.

## License

MIT – see [LICENSE](LICENSE).
