-- OTel span 落库（自建 exporter 写入）
CREATE TABLE IF NOT EXISTS spans (
    trace_id       text NOT NULL,
    span_id        text NOT NULL,
    parent_span_id text NOT NULL DEFAULT '',
    name           text NOT NULL,
    service        text NOT NULL DEFAULT 'llm-gateway',
    kind           text NOT NULL DEFAULT 'internal',
    start_at       timestamptz NOT NULL,
    end_at         timestamptz NOT NULL,
    duration_ms    integer NOT NULL DEFAULT 0,
    status         text NOT NULL DEFAULT 'ok',
    attributes     jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (trace_id, span_id)
);

CREATE INDEX IF NOT EXISTS spans_start_idx ON spans (start_at DESC);
CREATE INDEX IF NOT EXISTS spans_trace_idx ON spans (trace_id, start_at);
