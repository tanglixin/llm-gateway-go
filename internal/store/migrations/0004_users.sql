-- 用户与租户
CREATE TABLE IF NOT EXISTS users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE,
    name          text NOT NULL DEFAULT '',
    tenant_id     text NOT NULL,
    role          text NOT NULL DEFAULT 'member',
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS users_tenant_idx ON users (tenant_id);
