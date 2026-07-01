-- +goose Up
CREATE SCHEMA IF NOT EXISTS streaks;

CREATE TABLE streaks.apps (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    api_key_hash TEXT,
    webhook_url TEXT,
    webhook_secret TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Embedded mode runs everything under the implicit default app.
INSERT INTO streaks.apps (name) VALUES ('default');

CREATE TABLE streaks.subjects (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id BIGINT NOT NULL REFERENCES streaks.apps(id),
    external_id TEXT NOT NULL,
    timezone TEXT NOT NULL DEFAULT 'UTC',
    tz_changed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (app_id, external_id)
);
CREATE INDEX subjects_timezone_idx ON streaks.subjects (app_id, timezone);

CREATE TABLE streaks.streaks (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subject_id BIGINT NOT NULL REFERENCES streaks.subjects(id) ON DELETE CASCADE,
    key TEXT NOT NULL,
    config JSONB NOT NULL,
    current_count INT NOT NULL DEFAULT 0,
    longest INT NOT NULL DEFAULT 0,
    last_earned DATE,
    settled_through DATE,
    freezes INT NOT NULL DEFAULT 0,
    freeze_progress INT NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (subject_id, key)
);

-- The earn-once ledger: one row per (streak, period), amounts accumulate.
CREATE TABLE streaks.period_marks (
    streak_id BIGINT NOT NULL REFERENCES streaks.streaks(id) ON DELETE CASCADE,
    local_period DATE NOT NULL,
    amount INT NOT NULL DEFAULT 0,
    tz_at_record TEXT NOT NULL,
    first_recorded TIMESTAMPTZ NOT NULL,
    last_recorded TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (streak_id, local_period)
);

CREATE TABLE streaks.idempotency_keys (
    app_id BIGINT NOT NULL,
    key TEXT NOT NULL,
    streak_id BIGINT NOT NULL,
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (app_id, key)
);
CREATE INDEX idempotency_keys_created_idx ON streaks.idempotency_keys (created_at);

-- Transactional outbox: state changes and their events commit atomically.
CREATE TABLE streaks.events (
    id BIGSERIAL PRIMARY KEY,
    app_id BIGINT NOT NULL,
    streak_id BIGINT NOT NULL,
    subject_external_id TEXT NOT NULL,
    streak_key TEXT NOT NULL,
    type TEXT NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ
);
CREATE INDEX events_undelivered_idx ON streaks.events (app_id, id) WHERE delivered_at IS NULL;
CREATE INDEX events_streak_idx ON streaks.events (streak_id, id);

-- Once-per-local-day reminder dedup (insert claims atomically).
CREATE TABLE streaks.reminder_claims (
    streak_id BIGINT NOT NULL REFERENCES streaks.streaks(id) ON DELETE CASCADE,
    local_day DATE NOT NULL,
    PRIMARY KEY (streak_id, local_day)
);

-- +goose Down
DROP SCHEMA streaks CASCADE;
