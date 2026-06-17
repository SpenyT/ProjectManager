CREATE TABLE canonical_part (
  id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  mpn                   TEXT,
  manufacturer          TEXT,
  description           TEXT,
  package               TEXT,
  datasheet_url         TEXT,
  datasheet_resolved_at TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- NULLS DISTINCT (default): multiple no-name parts with NULL mpn/manufacturer stay distinct rows
CREATE UNIQUE INDEX canonical_part_mpn_mfr_uq ON canonical_part (mpn, manufacturer);

CREATE TABLE offering (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source             TEXT NOT NULL CHECK (source IN ('digikey', 'mouser', 'lcsc', 'aliexpress', 'temu')),
  source_external_id TEXT NOT NULL,
  canonical_part_id  UUID REFERENCES canonical_part(id),
  is_kit             BOOLEAN NOT NULL DEFAULT false,
  url                TEXT,
  seller             TEXT,
  title              TEXT,
  last_seen_price    NUMERIC(12,4),
  currency           TEXT,
  last_seen_at       TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- kit has no single canonical part; non-kit maps to one once resolved
  CHECK (NOT is_kit OR canonical_part_id IS NULL)
);
CREATE UNIQUE INDEX offering_source_uq ON offering (source, source_external_id);
CREATE INDEX offering_canonical_part_idx ON offering (canonical_part_id);
-- resolution queue: offerings awaiting canonical_part mapping
CREATE INDEX offering_unmapped_idx ON offering (id)
  WHERE canonical_part_id IS NULL AND is_kit = false;

CREATE TABLE kit_content (
  offering_id       UUID NOT NULL REFERENCES offering(id) ON DELETE CASCADE,
  canonical_part_id UUID NOT NULL REFERENCES canonical_part(id),
  qty_per_unit      INTEGER NOT NULL CHECK (qty_per_unit > 0),
  PRIMARY KEY (offering_id, canonical_part_id)
);
CREATE INDEX kit_content_canonical_part_idx ON kit_content (canonical_part_id);

CREATE TABLE part_attribute (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  canonical_part_id UUID NOT NULL REFERENCES canonical_part(id) ON DELETE CASCADE,
  axis              TEXT NOT NULL,
  direction         TEXT NOT NULL CHECK (direction IN ('higher_ok', 'lower_ok', 'range', 'exact')),
  value_num         NUMERIC,
  value_min         NUMERIC,
  value_max         NUMERIC,
  value_text        TEXT,
  unit              TEXT,
  source            TEXT NOT NULL DEFAULT 'user'
                      CHECK (source IN ('user', 'octopart', 'digikey', 'lcsc', 'computed')),
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX part_attribute_part_idx ON part_attribute (canonical_part_id);
CREATE INDEX part_attribute_axis_idx ON part_attribute (axis);

CREATE TABLE part_substitution (
  id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  satisfying_part_id UUID NOT NULL REFERENCES canonical_part(id) ON DELETE CASCADE,
  satisfied_part_id  UUID NOT NULL REFERENCES canonical_part(id) ON DELETE CASCADE,
  source             TEXT NOT NULL DEFAULT 'user'
                       CHECK (source IN ('user', 'octopart', 'digikey', 'lcsc')),
  note               TEXT,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (satisfying_part_id <> satisfied_part_id),
  UNIQUE (satisfying_part_id, satisfied_part_id)
);
CREATE INDEX part_substitution_satisfied_idx  ON part_substitution (satisfied_part_id);
CREATE INDEX part_substitution_satisfying_idx ON part_substitution (satisfying_part_id);
