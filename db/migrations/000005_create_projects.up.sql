CREATE TABLE project (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  scope_id   UUID NOT NULL REFERENCES inventory_scope(id),
  name       TEXT NOT NULL,
  status     TEXT NOT NULL DEFAULT 'planning'
               CHECK (status IN ('planning', 'active', 'paused', 'cancelled', 'built', 'archived')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX project_scope_idx ON project (scope_id);
-- availability joins filter active projects constantly
CREATE INDEX project_active_idx ON project (scope_id) WHERE status = 'active';
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON project
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE project_requirement (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id        BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  canonical_part_id UUID NOT NULL REFERENCES canonical_part(id),
  qty_required      INTEGER NOT NULL CHECK (qty_required > 0),
  UNIQUE (project_id, canonical_part_id)
);
CREATE INDEX project_requirement_project_idx       ON project_requirement (project_id);
CREATE INDEX project_requirement_canonical_part_idx ON project_requirement (canonical_part_id);

CREATE TABLE draw (
  id                         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_requirement_id     BIGINT NOT NULL REFERENCES project_requirement(id) ON DELETE CASCADE,
  lot_id                     BIGINT NOT NULL REFERENCES lot(id),
  qty                        INTEGER NOT NULL CHECK (qty > 0),
  consumed_qty               INTEGER NOT NULL DEFAULT 0 CHECK (consumed_qty >= 0),
  satisfied_via_kind         TEXT CHECK (satisfied_via_kind IN ('functional', 'parametric')),
  satisfied_via_substitution BIGINT REFERENCES part_substitution(id),
  created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (consumed_qty <= qty),
  CHECK (
       (satisfied_via_kind IS NULL        AND satisfied_via_substitution IS NULL)
    OR (satisfied_via_kind = 'functional' AND satisfied_via_substitution IS NOT NULL)
    OR (satisfied_via_kind = 'parametric' AND satisfied_via_substitution IS NULL)
  ),
  -- one row per (requirement, lot): claim is a current-state fact, not an event log
  CONSTRAINT draw_req_lot_uq UNIQUE (project_requirement_id, lot_id)
);
CREATE INDEX draw_lot_idx          ON draw (lot_id);
CREATE INDEX draw_requirement_idx  ON draw (project_requirement_id);
-- "every draw that used substitution edge X"
CREATE INDEX draw_substitution_idx ON draw (satisfied_via_substitution)
  WHERE satisfied_via_substitution IS NOT NULL;
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON draw
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE breakage_event (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  draw_id     BIGINT NOT NULL REFERENCES draw(id),
  qty         INTEGER NOT NULL CHECK (qty > 0),
  kind        TEXT NOT NULL DEFAULT 'broken' CHECK (kind IN ('broken', 'used', 'build_consumed')),
  reason      TEXT,
  reported_by UUID NOT NULL REFERENCES app_user(id),
  reported_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX breakage_event_draw_idx ON breakage_event (draw_id);
