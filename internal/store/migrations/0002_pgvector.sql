-- 向量检索：文档 + 分块
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS documents (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  text        NOT NULL DEFAULT 'default',
    name       text        NOT NULL,
    acl        text        NOT NULL DEFAULT '全员可见',
    status     text        NOT NULL DEFAULT 'ready',
    chunk_count integer    NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS chunks (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    tenant_id   text NOT NULL DEFAULT 'default',
    idx         integer NOT NULL,
    content     text NOT NULL,
    tokens      integer NOT NULL DEFAULT 0,
    embedding   vector(768) NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS chunks_tenant_idx ON chunks (tenant_id);
CREATE INDEX IF NOT EXISTS chunks_doc_idx ON chunks (document_id, idx);
-- 余弦距离的 HNSW 索引（pgvector 0.5+）
CREATE INDEX IF NOT EXISTS chunks_embedding_idx
    ON chunks USING hnsw (embedding vector_cosine_ops);
