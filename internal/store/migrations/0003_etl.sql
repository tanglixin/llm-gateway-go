-- 文档 ETL：原件位置、阶段、错误
ALTER TABLE documents ADD COLUMN IF NOT EXISTS storage_key text NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN IF NOT EXISTS size_bytes  bigint NOT NULL DEFAULT 0;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS stage       integer NOT NULL DEFAULT 0;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS error       text NOT NULL DEFAULT '';
ALTER TABLE documents ADD COLUMN IF NOT EXISTS updated_at  timestamptz NOT NULL DEFAULT now();
