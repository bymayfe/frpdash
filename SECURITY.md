# Security policy

frpdash manages tunnels into private networks and runs as root on the server, so security reports are welcome and
taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Use GitHub's private reporting instead: on this repository, open the
**Security** tab and choose **Report a vulnerability**.

Include what you can of:

- the affected version (`frpdash version`) and component (server, agent, panel, install script),
- steps to reproduce or a proof of concept,
- the impact you expect.

You should get a first answer within a week. Once a fix is released, the report can be published with credit to you
unless you prefer otherwise.

## Supported versions

Only the latest release receives security fixes.

## Design notes for reviewers

- The panel listens on loopback only and is meant to be reached over an SSH tunnel (or behind an access proxy).
- Agents connect to the server over TLS 1.3 and pin its certificate; pairing tokens are single-use and expire after
  15 minutes; secrets are stored only as SHA-256 hashes, passwords with bcrypt.
- The server sends agents structured data only; agents generate and validate the frpc configuration themselves.
- The one-line client installer pins the server's public key with curl and serves files only for a valid, unused
  pairing token.
