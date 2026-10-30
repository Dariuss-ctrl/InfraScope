-- Single-admin authentication with server-side sessions (ADR-0006).

CREATE TABLE users (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      text        NOT NULL UNIQUE CHECK (length(username) BETWEEN 1 AND 128),
    password_hash text        NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Only a SHA-256 hash of each session token is stored, so a leaked database
-- dump cannot be replayed as a cookie.
CREATE TABLE sessions (
    token_hash bytea       PRIMARY KEY CHECK (length(token_hash) = 32),
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
