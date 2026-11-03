-- Vision embeddings, basemap tiles for visual localisation, flight telemetry.
-- {{VISION_EMBEDDING_DIM}} is replaced with VISION_EMBEDDING_DIM.

ALTER TABLE keyframes
    ADD COLUMN image_embedding       VECTOR({{VISION_EMBEDDING_DIM}}),
    ADD COLUMN image_embedding_model TEXT,
    ADD COLUMN geo_source            TEXT,
    ADD COLUMN geo_accuracy_m        DOUBLE PRECISION,
    ADD COLUMN geo_candidates        JSONB,
    ADD COLUMN meta                  JSONB NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX keyframes_image_embedding_idx ON keyframes USING hnsw (image_embedding vector_cosine_ops);

-- Square crops of georeferenced basemaps at several ground sizes. Matching
-- loads one basemap's tiles into memory and scores them exactly, so no
-- vector index is needed here.
CREATE TABLE basemap_tiles (
    id              BIGSERIAL PRIMARY KEY,
    asset_id        BIGINT      NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    scale_m         INT         NOT NULL,
    lat             DOUBLE PRECISION NOT NULL,
    lon             DOUBLE PRECISION NOT NULL,
    embedding       VECTOR({{VISION_EMBEDDING_DIM}}) NOT NULL,
    embedding_model TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX basemap_tiles_asset_idx ON basemap_tiles (asset_id, scale_m);

-- Flight log samples (e.g. DJI CSV) aligned to the video timeline.
CREATE TABLE telemetry_points (
    asset_id BIGINT           NOT NULL REFERENCES assets (id) ON DELETE CASCADE,
    t_sec    DOUBLE PRECISION NOT NULL,
    ts       TIMESTAMPTZ      NOT NULL,
    lat      DOUBLE PRECISION NOT NULL,
    lon      DOUBLE PRECISION NOT NULL,
    alt_m    REAL,
    PRIMARY KEY (asset_id, t_sec)
);

-- How many times a task was put back to wait for another task.
ALTER TABLE tasks ADD COLUMN defers INT NOT NULL DEFAULT 0;
