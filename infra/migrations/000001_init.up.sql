CREATE TABLE pastes (
    paste_id   TEXT PRIMARY KEY,
    s3_key     TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    size_bytes BIGINT NOT NULL,
    is_deleted BOOLEAN NOT NULL DEFAULT false,
    owner_id   TEXT
);
