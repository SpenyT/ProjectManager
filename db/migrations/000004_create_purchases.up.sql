CREATE TABLE purchase_order (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  scope_id        UUID NOT NULL REFERENCES inventory_scope(id),
  source          TEXT NOT NULL CHECK (source IN ('digikey', 'mouser', 'lcsc', 'aliexpress', 'temu')),
  source_order_id TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'ordered'
                    CHECK (status IN ('ordered', 'shipped', 'delivered', 'cancelled')),
  placed_at       TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX purchase_order_dedupe_uq ON purchase_order (scope_id, source, source_order_id);
CREATE INDEX purchase_order_scope_idx ON purchase_order (scope_id);
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON purchase_order
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE order_line (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id    BIGINT NOT NULL REFERENCES purchase_order(id) ON DELETE CASCADE,
  offering_id UUID NOT NULL REFERENCES offering(id),
  qty         INTEGER NOT NULL CHECK (qty > 0),
  unit_price  NUMERIC(12,4) NOT NULL CHECK (unit_price >= 0),
  currency    TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX order_line_order_idx    ON order_line (order_id);
CREATE INDEX order_line_offering_idx ON order_line (offering_id);

CREATE TABLE lot (
  id                   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  canonical_part_id    UUID NOT NULL REFERENCES canonical_part(id),
  source_order_line_id BIGINT NOT NULL REFERENCES order_line(id),
  qty_received         INTEGER NOT NULL CHECK (qty_received > 0),
  remaining_qty        INTEGER NOT NULL CHECK (remaining_qty >= 0),
  -- NULL for kit-derived lots: per-part cost is unknown, not allocated
  unit_cost            NUMERIC(12,4) CHECK (unit_cost IS NULL OR unit_cost >= 0),
  currency             TEXT NOT NULL,
  received_at          TIMESTAMPTZ,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (remaining_qty <= qty_received)
);
CREATE INDEX lot_canonical_part_idx ON lot (canonical_part_id);
CREATE INDEX lot_order_line_idx     ON lot (source_order_line_id);
-- hot path: availability only sums lots with stock
CREATE INDEX lot_in_stock_idx ON lot (canonical_part_id) WHERE remaining_qty > 0;
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON lot
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
