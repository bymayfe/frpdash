# frpDash — Roadmap

**English** | [Türkçe](ROADMAP.tr.md)

Planned work. Finished items are checked; detailed design decisions are in [`PLAN.md`](PLAN.md) (Turkish).

## Installation

- [x] **One-line client install.** The "Add client" page shows
      `curl ... --pinnedpubkey ... https://VDS:7443/install/<token> | sudo sh`. The script downloads frpdash from the
      VDS (amd64/arm64), pairs and installs the `frpdash-agent` service. The token works once and is valid for 15
      minutes; curl verifies the certificate by its public key pin.
- [x] **First-run setup page (server install without parameters).** `frpdash server --install-service` needs no
      flags; the service starts in "setup pending" mode (frps and 7443 closed). On first open the panel asks for the
      one-time setup code (root-owned `setup-code` file + journal + install output), the admin account and the
      public address (addresses found on the server are suggested, private ones marked). The public address is a
      database setting; `--public-host` and `FRPDASH_ADMIN_*` remain to skip the page.
- [x] **Firewall on the setup page and in Settings.** Lists the ports frpdash needs and opens them in ufw or
      firewalld, or installs ufw (apt-get). SSH ports (from `sshd -T` / `sshd_config`) are always allowed before a
      firewall is enabled; the commands run are shown.
- [x] **Firewall follows services.** Only the ports in use are open: a public service's VDS port is opened when it
      is added and closed when it is deleted (no longer the whole public range). frpdash remembers what it opened and
      closes only that; SSH and hand-made rules are never touched.
- [x] **Deploy from a checkout.** `make deploy-server` builds, copies over SSH and installs or updates; it asks for
      the address when none is given and works for root logins without sudo.
- [ ] Editing ports (frps bind, agent, public range) on the setup page and in Settings.
- [ ] Changing the public address in Settings and pushing it to paired clients (today only via `--public-host`;
      existing clients keep the old address).
- [ ] **Single install entry point with role detection.** One `install.sh` / `frpdash setup`: if the machine has an
      agent.json, offer "continue as installed client / re-pair / remove"; if it has a server database, "update";
      otherwise ask "Server (VDS, frps) or Client (frpc)?" and, for a client, ask for the code from the panel.
- [ ] **`frpdash status`.** Shows the machine's role and state: on a client the server address, connection,
      revision, frpc state and services; on the server frps and connected clients. Read-only.
- [x] **One-command VDS install (`make deploy-server VDS=vds`).** Builds, copies over SSH, runs
      `frpdash server --install-service` and puts the other architecture's binary in `/var/lib/frpdash/dist/`.
      Running it again updates.
- [x] **One-line server install.** `install.sh` downloads the release binaries, verifies their checksums and
      installs, updates (`--version`) or removes (`--uninstall`, `--purge`) frpdash. Releases: `make release`.
- [ ] **Install a client from the panel over SSH.** Enter the client's SSH address and user in the panel; the server
      connects over SSH and runs the install line itself, so nothing is typed on the client. Open questions: where
      the SSH key lives (preferably not stored at all, only an agent/password for that request), host key
      verification, the sudo password.
- [ ] Server update: when a new binary is deployed, update the clients' agents too.

## Connection and settings

- [ ] Editing global/client settings in the panel; applying connection settings (bindPort, token, protocol) with a
      **30-second automatic rollback**. The protocol and the layer model are ready.
- [ ] Scope selector ("server only / all clients / selected clients") and a preview before applying.
- [ ] Per-machine tokens through an frps plugin hook.

## Client-side management

Everything in this section sits behind **Developer mode**.

- [ ] **Developer mode.** A switch at the bottom of Settings, **off** by default. Turning it on adds a "Developer"
      section to the menu with the extra options. Turning it off does not just hide them: the server stops honouring
      them (e.g. client edit permissions stop working); settings are kept and come back when it is turned on again.
      Switching is logged. Future experimental/advanced options go here too.
- [ ] **Two switches: server + client.** Client-side features work only when Developer mode is on both on the server
      and on that client. Turning it on at the client asks the server: if the server has it off or the client is
      offline, it cannot be turned on ("enable it on the server first"). Turning it off on the server ends the effect
      on every client at once (the server notifies them and refuses their requests). The server always decides.
- [ ] **Visibility.** Each client reports its switch to the server. Panel: the server switch and the clients that
      have it on in Settings; a "Dev" column in the client list (state + permission); "Server: on · This client: on"
      and the permission selector on the client page. Client: a banner in the local panel, a "Developer" line in
      `frpdash status`; switch with `frpdash dev on|off` or in the local panel.
- [ ] **Client permission level** (per client in the panel, most restrictive by default): _None_ (status only) /
      _View_ (its own services, ports, logs) / _Edit own services_ (add, remove, local port, VDS port within the
      allowed range). Global settings, other clients, its own port range and adding itself as a visitor are never
      allowed. The permission is checked on the server for every request.
- [ ] **Changes go through the server.** A change made on a client is sent to the server as a request over the agent
      connection; the server checks the permission and ports/range, stores it, bumps the revision and pushes the
      config back. The server database is the single source of truth; no changes while the client is offline. Every
      change is logged as "made by the client".
- [ ] **Client local panel and CLI.** On the client's `127.0.0.1`, only that machine's services; CLI commands for
      the same tasks.
- [ ] **Reaching the VDS panel from a client.** A per-client switch (off by default): a tunnel through the agent
      connection, `localhost:8090` on the client → the VDS panel. Panel login is still required; login attempts are
      rate-limited.

## Panel

- [x] UI built with Tailwind CSS v4 (standalone CLI, `make css`; output embedded in the binary, no CDN).
- [ ] Listening port scanner (list open ports on a client to make adding services easier).
- [ ] Agent/frpc log view in the panel.
- [ ] Rate limiting for login attempts.
- [ ] Two-step login with TOTP.
- [ ] Change history.

## Later

- [ ] HTTP/domain support.
- [ ] Service templates.
- [ ] Windows agent.
