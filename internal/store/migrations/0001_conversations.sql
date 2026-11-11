CREATE TABLE IF NOT EXISTS conversations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  text        NOT NULL DEFAULT 'default',
    title      text        NOT NULL DEFAULT '新对话',
    model      text        NOT NULL DEFAULT '',
    preview    text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS conversations_list_idx
    ON conversations (tenant_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    role            text NOT NULL CHECK (role IN ('user', 'assistant', 'system')),
    content         text NOT NULL DEFAULT '',
    model           text NOT NULL DEFAULT '',
    tokens_in       integer NOT NULL DEFAULT 0,
    tokens_out      integer NOT NULL DEFAULT 0,
    latency_ms      integer NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS messages_by_conversation_idx
    ON messages (conversation_id, created_at);
