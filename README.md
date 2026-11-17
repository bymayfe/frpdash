# frpDash

**English** | [Türkçe](README.tr.md)

A self-hosted management tool with a web interface for [frp](https://github.com/fatedier/frp) tunnels. It installs,
configures and monitors `frps` on your server and the `frpc` clients on machines behind NAT from a single panel,
**without editing configuration files by hand**.

- One Go binary: `server` (VDS), `agent` (client machines) and an embedded web panel
- **Add client → one line `curl ... | sudo sh`** installs and pairs a machine; the agent downloads frpc itself and
  sets up a systemd service
- **Public** services (tcp/udp, a free port on the VDS is assigned automatically) and **private** services (stcp,
  never exposed to the internet, reachable only by clients granted access as visitors)
- Port conflict checks: ports already used by frpdash, by other services or by other programs on the VDS are refused
- The firewall follows the services: only the ports in use are open
- Online/offline status; changes made in the panel reach the agent within seconds
- No domain needed: the agent connection uses self-signed TLS with certificate fingerprint pinning

**Quick start** (on the VDS):

```bash
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh
```

Then follow [Installation](#installation).

## How it works

```
          (SSH tunnel)                                  Internet
 Browser ────────────────► 127.0.0.1:8090  Panel ┐
                                                 │  frpdash server (VDS)
 frpdash agent ──── wss://VDS:7443 ──► Agent API ┤   ├─ SQLite
   (home, behind NAT)  TLS + pin                 │   └─ frps  :7000  ──► public ports 20000-20999
   └─ frpc ─────────────────────────────────────────►┘
```

- The panel listens **on loopback only** and is never exposed to the internet. You reach it over an SSH tunnel.
- The agent opens the connection itself, so it works behind NAT. The server keeps a **desired config** and a
  revision number per client; the agent receives it on connect or whenever the panel changes it, checks it with
  `frpc verify`, applies it and acknowledges.
- User input is never written straight into an frp config: the panel sends structured data, and the agent
  validates it and generates the TOML with a library.
- frp binaries are downloaded at a pinned version (**v0.72.0**) and verified against SHA-256 sums built into the
  code.

## Roles

The same `frpdash` binary runs in two places, in two roles:

|                 | **Server**                                 | **Client**                                                 |
| --------------- | ------------------------------------------ | ---------------------------------------------------------- |
| Where           | VDS (a server reachable from the internet) | Home PC, any machine behind NAT                            |
| frp side        | **frps**                                   | **frpc**                                                   |
| Command         | `frpdash server`                           | `frpdash connect` (once), then the `frpdash agent` service |
| systemd service | `frpdash-server`                           | `frpdash-agent`                                            |
| What it does    | Runs the panel, installs and manages frps  | Installs frpc and runs it with the config from the panel   |

Flow: **install the server on the VDS → "Add client" in the panel → run the line it shows on the client → the
client connects to the VDS.** Nothing is typed back into the panel from the client.

## Installation

Requirements: a Linux server (amd64 or arm64) with systemd, reachable from the internet (a VDS), and SSH access to it.

### 1. Server: install on the VDS (frps side)

On the VDS:

```bash
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh
```

The script downloads the latest release, verifies its SHA-256 checksums, installs `frpdash` to `/usr/local/bin` and
starts the `frpdash-server` service. It prints a one-time **setup code**.

**Finish the setup in the browser.** The panel never listens on the internet; open it from your own computer through
an SSH tunnel:

```bash
ssh -L 8090:127.0.0.1:8090 user@your-vds     # then open http://localhost:8090
```

The first page asks for:

1. the **setup code** (also in `/var/lib/frpdash/setup-code` on the VDS, readable only by root),
2. the **admin account**,
3. the **public address** clients connect to (the addresses found on the server are suggested; behind a provider's
   NAT, enter the public IP),
4. the **firewall**: with the box checked, frpdash opens the ports it needs in ufw or firewalld (or installs ufw on
   Debian/Ubuntu). It always allows the SSH port before enabling a firewall, and from then on opens a public
   service's port when you add it and closes it when you delete it.

Ports used on the VDS: `7443/tcp` (clients and the client install script), `7000/tcp` and `7000/udp` (frps), and the
port of each public service. If your provider has its own firewall in its web panel, open them there too.

### Updating and removing

Run the installer again to update; the database, clients, services and settings are kept.

```bash
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh -s -- --version v0.1.0   # a specific release
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh -s -- --uninstall        # remove, keep the data
curl -fsSL https://raw.githubusercontent.com/bymayfe/frpdash/main/install.sh | sudo sh -s -- --purge            # remove everything
```

`--uninstall` and `--purge` also work on client machines (they remove `frpdash-agent`).

### 2. Client: connect a home PC (frpc side)

Once per machine:

1. **In the panel** (VDS): **Add client** → give it a name (e.g. `home-pc`). The page shows a one-line command.
2. **On the home PC**: paste that line into a terminal:
   ```bash
   curl -fsSL -k --pinnedpubkey 'sha256//...' 'https://<VDS_IP>:7443/install/<code>' | sudo sh
   ```
   It downloads the matching `frpdash` from the VDS, pairs with it, writes the credentials to
   `/etc/frpdash/agent.json` (0600) and installs the `frpdash-agent` service, which downloads and runs frpc.
3. **In the panel** the client shows as **online**. From now on you add services in the panel; nothing else is
   needed on the client.

The code works once and is valid for 15 minutes. The certificate is self-signed, hence `-k`; what ties curl to
your VDS is `--pinnedpubkey`, and if the pin does not match nothing is downloaded.

Without curl: copy the `frpdash` binary to the client yourself and run the `sudo ./frpdash connect <code>`
command from the "Without curl" section of the page.

### 3. Adding services

On the client page, **Add service**:

- **public**: tcp or udp. Leave the VDS port empty to get the first free port in the range.
  Reach it at `VDS_IP:port`.
- **private**: stcp. No port is opened on the VDS. Pick the client that may access it and the local port to use on
  that machine; on that client, `127.0.0.1:<port>` connects to the service.

## Commands

| Command                                                 | Where                  | What it does                                     |
| ------------------------------------------------------- | ---------------------- | ------------------------------------------------ |
| `frpdash server [--install-service] [--public-host IP]` | Server (VDS, frps)     | Panel + client connections + frps                |
| `frpdash passwd <user>`                                 | Server (VDS, frps)     | Changes a panel password (recovery if forgotten) |
| `frpdash connect [--no-install] <code>`                 | Client (home PC, frpc) | Pairs and installs the agent service             |
| `frpdash agent`                                         | Client (home PC, frpc) | Manages frpc (normally run by systemd)           |
| `frpdash version`                                       | Anywhere               | Version                                          |

Flags: `frpdash <command> -h`. Default directories: server `/var/lib/frpdash`, agent `/var/lib/frpdash-agent`.
Logs: `journalctl -u frpdash-server` / `journalctl -u frpdash-agent` (frps/frpc output is included).

## Security

- Passwords use bcrypt (cost 12); sessions, pairing codes and agent secrets are stored only as SHA-256 hashes.
- Database and key files are 0600, directories 0700.
- Panel: HttpOnly + SameSite=Strict session cookie, cross-site POSTs rejected, strict CSP, non-loopback addresses
  refused.
- Agent API: TLS 1.3 only; pairing, connect and install endpoints. The agent verifies the server by certificate
  fingerprint (on resumed TLS sessions too). The install script and binary can only be downloaded with a valid,
  unused pairing code.
- On the frps side, only clients granted access in the panel (`allowUsers`) can reach private services.

## Building from source

Requires Go 1.26+.

```bash
make build          # bin/frpdash
make build-linux    # bin/frpdash-linux-amd64 and bin/frpdash-linux-arm64
make deploy-server VDS=user@your-vds   # build here, copy over SSH and install or update on the VDS
make css            # rebuild web/static/app.css after changing templates (Tailwind CSS standalone CLI)
```

Maintainers publish releases with `make release VERSION=vX.Y.Z` (needs the GitHub CLI); `install.sh` downloads
from them.

## Limitations and roadmap

The MVP is complete. Next steps and ideas: [`docs/ROADMAP.md`](docs/ROADMAP.md).

Known behaviour: when a client's config changes, that client's frpc restarts (open connections drop briefly). The
agent runs as root.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

frpdash is an independent project built around [frp](https://github.com/fatedier/frp) (Apache-2.0). It does not
include frp; it downloads the official release and verifies its checksum. It is not affiliated with or endorsed by
the frp project.
