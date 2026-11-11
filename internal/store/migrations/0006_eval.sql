-- Prompt 版本与评测记录
CREATE TABLE IF NOT EXISTS prompt_versions (
    name           text PRIMARY KEY,
    description    text NOT NULL DEFAULT '',
    template       text NOT NULL,
    retrieval_mode text NOT NULL DEFAULT 'hybrid',
    top_k          integer NOT NULL DEFAULT 3,
    is_active      boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS prompt_versions_active_idx
    ON prompt_versions (is_active) WHERE is_active;

CREATE TABLE IF NOT EXISTS eval_runs (
    id          bigserial PRIMARY KEY,
    version     text NOT NULL,
    cases       integer NOT NULL,
    recall      numeric(4,3) NOT NULL DEFAULT 0,
    mrr         numeric(4,3) NOT NULL DEFAULT 0,
    ndcg        numeric(4,3) NOT NULL DEFAULT 0,
    gate        text NOT NULL DEFAULT 'pending',
    took_ms     integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS eval_runs_version_idx ON eval_runs (version, created_at DESC);
