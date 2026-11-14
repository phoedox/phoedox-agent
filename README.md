# phoedox-agent

Small agent for servers, VMs and LXC containers. It reports system values, packages,
containers, images and stacks to a [Phoedox](https://github.com/phoedox/phoedox) instance
and runs approved actions (restart containers, updates, commands, Wake-on-LAN,
speed tests, web terminal).

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

### Wake-on-LAN, speed test and terminal

| Action | Args | What it does |
| --- | --- | --- |
| `wol` | MAC (`aa:bb:cc:dd:ee:ff`, `aa-bb-…` or `aabbccddeeff`), optional IPv4 broadcast address (default `255.255.255.255`) | Sends the magic packet three times, 100 ms apart, to UDP port 9. |
| `speedtest` | – | Measures ping (median of 5), download and upload between this host and the instance; output `{"down":312.4,"up":95.1,"ping":2.3}` (Mbit/s, ms). |
| `terminal` | session ID | Opens an interactive shell in the browser (see below). |

- **`wol` needs the agent in the same broadcast domain** (LAN/VLAN) as the
  machine to wake: routers do not forward broadcasts. For another subnet, use its
  directed broadcast address (e.g. `192.168.20.255`) if the router allows it, or an
  agent in that network.
- `speedtest` only talks to the instance (`/api/v1/agent/speedtest/…` with the agent
  token): a 2 MiB probe, then a download sized for about 4 seconds (4–100 MiB) and an
  upload of the same size (at most 25 MiB).
- **`terminal` is powerful: it is a shell as the agent's user** – root if the agent runs
  as root. Allow it only deliberately, on hosts where the admins whose keys are under
  `adminKeys` may have a shell anyway. `phoedox-agent policy` shows whether it is
  allowed and as which user. The agent connects **outbound** to
  `wss://<instance>/api/v1/agent/terminal/<session>` with its token and starts
  `terminalShell` (default `/bin/bash`, else `/bin/sh`) as a login shell in a PTY with
  `TERM=xterm-256color`. A session ends after 60 minutes, after 15 minutes without input,
  when the shell exits or the connection closes; then the shell's process group is
  killed. At most 4 sessions run at once, beside other jobs. The agent logs only the
  start and end of a session, never its content. Linux, macOS and FreeBSD only.

## Home Assistant bridge (ADR-0053)

When Home Assistant is not reachable from the Phoedox instance, an agent in the
same network can read it and switch entities. The long-lived token stays in a file
on this host; it is never sent to the instance.

```yaml
allow: [ha.call]            # only needed for switching
adminKeys: [...]
homeassistant:
  url: http://homeassistant.local:8123
  tokenFile: /etc/phoedox-agent/ha-token   # a long-lived access token, chmod 600
  entities: ["switch.*", "light.flur"]     # what ha.call may switch; empty = nothing
```

- Every report carries the states of the entities of devices that have an area
  (numbers with their unit, on/off). The device list is sent when it changes and at
  least every 6 hours. The Home Assistant plugin of the project turns it into rooms and
  devices and records the states as history.
- `ha.call` (args: entity, `turn_on` | `turn_off` | `toggle`) runs only for entities of
  the domains switch, light, fan and input_boolean that match `entities`, and only as
  a signed job like every other action.
- Reading may fail without stopping the agent; the report then names the error.

## License

MIT – see [LICENSE](LICENSE).
