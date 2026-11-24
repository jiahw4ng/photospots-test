-- Applied by `photospots migrate`. Every statement is idempotent.
CREATE EXTENSION IF NOT EXISTS postgis;

-- One row per photo from any source (Wikimedia Commons, Flickr, ...).
CREATE TABLE IF NOT EXISTS photos (
    source        text        NOT NULL,          -- 'commons' | 'flickr'
    source_id     text        NOT NULL,
    photographer  text        NOT NULL,          -- best guess at the person who took it
    title         text        NOT NULL DEFAULT '',
    taken_local   timestamp,                     -- camera wall-clock time (sources give no zone)
    taken_hour    smallint,                      -- 0-23 local hour, NULL when the source gave a date only
    taken_month   smallint,                      -- 1-12, NULL when unknown
    geom          geometry(Point, 4326) NOT NULL,
    thumb_url     text        NOT NULL DEFAULT '',
    page_url      text        NOT NULL DEFAULT '',
    license       text        NOT NULL DEFAULT '',
    views         integer,                       -- Flickr only
    tags          text[]      NOT NULL DEFAULT '{}',
    fetched_at    timestamptz NOT NULL DEFAULT now(),
    spot_id       integer,                       -- set by `photospots cluster`
    PRIMARY KEY (source, source_id)
);
CREATE INDEX IF NOT EXISTS photos_geom_idx ON photos USING gist (geom);
CREATE INDEX IF NOT EXISTS photos_spot_idx ON photos (spot_id);

-- Spots are rebuilt from scratch by every `photospots cluster` run.
CREATE TABLE IF NOT EXISTS spots (
    id                 integer PRIMARY KEY,
    geom               geometry(Point, 4326) NOT NULL,
    radius_m           real    NOT NULL,
    photo_count        integer NOT NULL,
    photographer_count integer NOT NULL,
    score              real    NOT NULL,
    hour_histogram     integer[] NOT NULL,       -- 24 buckets, local hour
    month_histogram    integer[] NOT NULL,       -- 12 buckets
    label              text    NOT NULL DEFAULT '',
    cover_thumb        text    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS spots_geom_idx ON spots USING gist (geom);

-- Lets incremental ingests resume from where the last run stopped.
CREATE TABLE IF NOT EXISTS ingest_state (
    key   text PRIMARY KEY,
    value text NOT NULL
);
