-- 0001_init: initial schema. Times are unix seconds (INTEGER).

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    totp_secret   TEXT,
    created_at    INTEGER NOT NULL
);

-- Global settings layer (bindPort, token, default port range, ...).
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value_json TEXT NOT NULL CHECK (json_valid(value_json)),
    updated_at INTEGER NOT NULL
);

CREATE TABLE clients (
    id                INTEGER PRIMARY KEY,
    name              TEXT    NOT NULL UNIQUE,
    agent_secret_hash TEXT,                 -- NULL until pairing completes
    port_range_min    INTEGER,
    port_range_max    INTEGER,
    overrides_json    TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(overrides_json)),
    desired_rev       INTEGER NOT NULL DEFAULT 0,
    applied_rev       INTEGER NOT NULL DEFAULT 0,
    last_seen_at      INTEGER,
    created_at        INTEGER NOT NULL,
    CHECK ((port_range_min IS NULL) = (port_range_max IS NULL)),
    CHECK (port_range_min IS NULL OR (port_range_min BETWEEN 1 AND 65535
                                      AND port_range_max BETWEEN port_range_min AND 65535))
);

CREATE TABLE proxies (
    id             INTEGER PRIMARY KEY,
    client_id      INTEGER NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    name           TEXT    NOT NULL,
    mode           TEXT    NOT NULL CHECK (mode IN ('public', 'private')),
    type           TEXT    NOT NULL CHECK (type IN ('tcp', 'udp', 'stcp')),
    local_ip       TEXT    NOT NULL DEFAULT '127.0.0.1',
    local_port     INTEGER NOT NULL CHECK (local_port BETWEEN 1 AND 65535),
    remote_port    INTEGER CHECK (remote_port BETWEEN 1 AND 65535),
    sk             TEXT,
    overrides_json TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(overrides_json)),
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at     INTEGER NOT NULL,
    UNIQUE (client_id, name),
    -- public = tcp/udp with a remote port on the VDS; private = stcp with a secret key, no remote port.
    CHECK (
        (mode = 'public'  AND type IN ('tcp', 'udp') AND remote_port IS NOT NULL AND sk IS NULL)
     OR (mode = 'private' AND type = 'stcp'          AND remote_port IS NULL     AND sk IS NOT NULL)
    )
);

-- A remote port can be used once per protocol on the VDS. This is the
-- database-level guarantee behind the panel's port conflict check.
CREATE UNIQUE INDEX proxies_remote_port ON proxies (type, remote_port)
    WHERE remote_port IS NOT NULL;

CREATE INDEX proxies_client ON proxies (client_id);

-- Which clients may reach a private (stcp) proxy, and on which local port.
CREATE TABLE proxy_visitors (
    proxy_id          INTEGER NOT NULL REFERENCES proxies (id) ON DELETE CASCADE,
    visitor_client_id INTEGER NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    bind_port         INTEGER NOT NULL CHECK (bind_port BETWEEN 1 AND 65535),
    PRIMARY KEY (proxy_id, visitor_client_id),
    UNIQUE (visitor_client_id, bind_port)
);

CREATE TABLE pairing_codes (
    id         INTEGER PRIMARY KEY,
    code_hash  TEXT    NOT NULL UNIQUE,
    client_id  INTEGER NOT NULL REFERENCES clients (id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER,
    created_at INTEGER NOT NULL
);

-- Panel login sessions. Only the SHA-256 of the session token is stored.
CREATE TABLE sessions (
    token_hash TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
