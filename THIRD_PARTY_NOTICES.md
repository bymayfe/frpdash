# Third-party notices

frpdash is licensed under the [Apache License 2.0](LICENSE). It builds on the following projects.

## Embedded in the frpdash binary

| Component                                                                                                                                             | Where                          | License                                   |
| ----------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------ | ----------------------------------------- |
| [htmx](https://htmx.org) 2                                                                                                                            | `web/static/htmx.min.js`       | Zero-Clause BSD (0BSD)                    |
| [Tailwind CSS](https://tailwindcss.com) v4.3.5                                                                                                        | generated `web/static/app.css` | MIT                                       |
| Go modules listed in [`go.mod`](go.mod), e.g. `modernc.org/sqlite`, `golang.org/x/*`, `github.com/coder/websocket`, `github.com/pelletier/go-toml/v2` | compiled in                    | BSD-3-Clause, MIT or ISC; see each module |

The Go standard library is distributed under the BSD-3-Clause license.

## Downloaded at run time, not included

| Component                                      | How it is used                                                                                                                                 | License    |
| ---------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | ---------- |
| [frp](https://github.com/fatedier/frp) v0.72.0 | `frps` (server) and `frpc` (clients) are downloaded from the official GitHub release and verified against SHA-256 checksums pinned in the code | Apache-2.0 |

frpdash is an independent project and is not affiliated with or endorsed by the frp project.

## Build tools only

The Tailwind CSS standalone CLI (MIT) builds `web/static/app.css` (`make css`); it is not part of the binary.
