-- Core schema. {{EMBEDDING_DIM}} is replaced at apply time with EMBEDDING_DIM.

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- Every ingested file: video, image, remote-sensing raster or document.
CREATE TABLE assets (
    id             BIGSERIAL PRIMARY KEY,
    uid            TEXT        NOT NULL UNIQUE,
    type           TEXT        NOT NULL CHECK (type IN ('video', 'image', 'remote_sensing', 'document')),
    format         TEXT        NOT NULL,
    collection     TEXT        NOT NULL DEFAULT 'default',
    filename       TEXT        NOT NULL,
    storage_path   TEXT        NOT NULL,
    source         TEXT        NOT NULL DEFAULT 'upload' CHECK (source IN ('upload', 'register')),
    sidecar_path   TEXT,
    size_bytes     BIGINT      NOT NULL,
    md5            TEXT        NOT NULL,
    status         TEXT        NOT NULL DEFAULT 'pending'
                               CHECK (status IN ('pending', 'processing', 'ready', 'failed')),
    error          TEXT,
    meta           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    width          INT,
    height         INT,
    duration_sec   DOUBLE PRECISION,
    fps            DOUBLE PRECISION,
    page_count     INT,
    captured_at    TIMESTAMPTZ,
    lat            DOUBLE PRECISION,
    lon            DOUBLE PRECISION,
    geo_source     TEXT,
    geo_accuracy_m DOUBLE PRECISION,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX assets_collection_md5_uq ON assets (collection, md5);
CREATE INDEX assets_type_idx ON assets (type);
CREATE INDEX assets_status_idx ON assets (status);
CREATE INDEX assets_captured_idx ON assets (captured_at);
CREATE INDEX assets_latlon_idx ON assets (lat, lon);

-- Durable work queue. One row per (asset, kind); re-running resets the row.
CREATE TABLE tasks (
    id           BIGSERIAL PRIMARY KEY,
    asset_id     BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL,
    status       TEXT        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'running', 'done', 'failed')),
    attempts     INT         NOT NULL DEFAULT 0,
    max_attempts INT         NOT NULL DEFAULT 3,
    progress     REAL        NOT NULL DEFAULT 0,
    message      TEXT,
    error        TEXT,
    run_after    TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at   TIMESTAMPTZ,
    finished_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (asset_id, kind)
);
CREATE INDEX tasks_pending_idx ON tasks (run_after) WHERE status = 'pending';

-- Visual units. A video has many keyframes; an image or raster has exactly one
-- (idx 0), so search, detection and QA treat both the same way.
CREATE TABLE keyframes (
    id                 BIGSERIAL PRIMARY KEY,
    asset_id           BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    idx                INT         NOT NULL,
    ts_sec             DOUBLE PRECISION NOT NULL DEFAULT 0,
    frame_no           INT,
    path               TEXT        NOT NULL,
    width              INT,
    height             INT,
    scene_score        REAL,
    description        TEXT,
    description_source TEXT,
    tags               TEXT[]      NOT NULL DEFAULT '{}',
    search_text        TEXT,
    tsv                TSVECTOR,
    embedding          VECTOR({{EMBEDDING_DIM}}),
    embedding_model    TEXT,
    captured_at        TIMESTAMPTZ,
    lat                DOUBLE PRECISION,
    lon                DOUBLE PRECISION,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (asset_id, idx)
);
CREATE INDEX keyframes_tsv_idx ON keyframes USING GIN (tsv);
CREATE INDEX keyframes_embedding_idx ON keyframes USING hnsw (embedding vector_cosine_ops);
CREATE INDEX keyframes_latlon_idx ON keyframes (lat, lon);

-- Objects found in a keyframe. Box in keyframe pixel coordinates.
CREATE TABLE detections (
    id          BIGSERIAL PRIMARY KEY,
    asset_id    BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    keyframe_id BIGINT      NOT NULL REFERENCES keyframes (id) ON DELETE CASCADE,
    label       TEXT        NOT NULL,
    label_zh    TEXT,
    score       REAL,
    x           REAL,
    y           REAL,
    w           REAL,
    h           REAL,
    source      TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX detections_keyframe_idx ON detections (keyframe_id);
CREATE INDEX detections_asset_label_idx ON detections (asset_id, label);

-- Text of documents, split into chunks for retrieval.
CREATE TABLE doc_chunks (
    id              BIGSERIAL PRIMARY KEY,
    asset_id        BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    seq             INT         NOT NULL,
    page            INT,
    text            TEXT        NOT NULL,
    search_text     TEXT,
    tsv             TSVECTOR,
    embedding       VECTOR({{EMBEDDING_DIM}}),
    embedding_model TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (asset_id, seq)
);
CREATE INDEX doc_chunks_tsv_idx ON doc_chunks USING GIN (tsv);
CREATE INDEX doc_chunks_embedding_idx ON doc_chunks USING hnsw (embedding vector_cosine_ops);

-- One row per AI call: what was asked, what it cost, how long it took.
-- No foreign key on asset_id so the log outlives deleted assets.
CREATE TABLE ai_call_log (
    id                BIGSERIAL PRIMARY KEY,
    endpoint          TEXT        NOT NULL,
    model             TEXT        NOT NULL,
    purpose           TEXT        NOT NULL,
    status            TEXT        NOT NULL CHECK (status IN ('ok', 'error', 'fallback')),
    attempts          INT         NOT NULL,
    latency_ms        INT         NOT NULL,
    prompt_tokens     INT         NOT NULL DEFAULT 0,
    completion_tokens INT         NOT NULL DEFAULT 0,
    total_tokens      INT         NOT NULL DEFAULT 0,
    input_summary     TEXT,
    output_summary    TEXT,
    error             TEXT,
    asset_id          BIGINT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ai_call_log_created_idx ON ai_call_log (created_at DESC);
CREATE INDEX ai_call_log_endpoint_idx ON ai_call_log (endpoint, created_at DESC);
