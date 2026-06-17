# Parts Tracker — Data Schema

The data model rests on one primitive: **the lot, and draws against it.** Everything
— kit explosion, project demand, cost basis, provenance, contention — runs through it.

Three identities are kept strictly distinct:

- **offering** — a buyable listing from one source (a DigiKey SKU, an AliExpress/Temu listing). What a purchase references.
- **canonical_part** — the physical component, source-independent (MPN + manufacturer). What inventory is counted in and what projects depend on.
- **kit** — an offering that yields *quantities of multiple canonical parts*. A decomposition rule between an offering and the parts it resolves to.

Postgres is the single system of record. Relationships that a graph engine would hold
(kit→part, project→part, part→part substitution) are stored as explicit Postgres edge
tables; no graph database is used (see Part 2 for the rationale and the Neo4j promotion
path if ever needed).

---

## Conventions (Postgres)

- **PK strategy is mixed, by design:**
  - **Shared catalog** (`canonical_part`, `offering`) uses `UUID` (PG18 `uuidv7()`, or
    `gen_random_uuid()` pre-18 — but prefer v7 for index locality). Rationale: globally
    shared, IDs may surface in URLs/API responses, opacity + federation-safety wanted.
  - **Scoped / high-churn tables** (`purchase_order`, `order_line`, `lot`, `project`,
    `project_requirement`, `draw`, `breakage_event`) use
    `BIGINT GENERATED ALWAYS AS IDENTITY`. Never federated, never opaque-exposed, insert-
    heavy → narrower keys, better B-tree locality than random/scattered UUIDs.
  - **Tenancy** (`user`, `team`, `inventory_scope`): `UUID` (`user`/`team` may be exposed,
    opacity wanted); `team_membership` is a join, no surrogate.
  Do NOT "uniformize" to UUID-everywhere — that's the lazy default this avoids.
- **Money:** `NUMERIC(12,4)` + a currency owner (see below). Never float, never bare
  `numeric` without scale.
- **Time:** `TIMESTAMPTZ` only (never `timestamp`). `now()` for defaults.
- **Strings:** `TEXT` (+ `CHECK (length(...) <= n)` if a bound is needed); never
  `varchar(n)`/`char(n)`.
- **Quantities:** integer (`BIGINT`/`INTEGER`) by default with `CHECK > 0`. Fractional
  only where a part is genuinely measured (wire/solder by length) — those are `NUMERIC`,
  flagged per-column. Component counts are integers.
- **Enums:** evolving business statuses/sources are `TEXT + CHECK (... IN (...))`, not bare
  text and not PG `ENUM` (CHECK is easy to evolve). `offering.source` may graduate to a
  lookup table later (new sources are data, not code) — CHECK is the minimal start.
- **FK indexes are manual.** Postgres does not auto-index FK columns. Every FK column used
  in a join/filter gets an explicit index (see Indexes per table).
- **Currency owner:** `order_line.currency` is authoritative for a purchase. `lot.currency`
  is an intentional audit snapshot copied at lot creation (cost basis must stay stable even
  if anything upstream is corrected) — documented denormalization, not drift.
- **`updated_at` is trigger-maintained, not app-maintained.** Every table with an
  `updated_at` (`purchase_order`, `lot`, `project`, `draw`) gets a `BEFORE UPDATE` trigger
  so the column can't go stale when the app forgets. Guard it to only bump on real changes
  (avoids no-op churn and interference with `ON CONFLICT DO UPDATE`):

  ```sql
  CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
  BEGIN
    IF row(NEW.*) IS DISTINCT FROM row(OLD.*) THEN
      NEW.updated_at := now();
    END IF;
    RETURN NEW;
  END $$ LANGUAGE plpgsql;

  -- per table:
  CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON lot
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
  ```
  (`updated_at` is sync/debug metadata, not business-critical — but trigger-maintained so
  it's trustworthy when you do need it.)

### Cross-row invariants enforced in the write path (not expressible as CHECK)

- **No oversell (CORE CONCURRENCY INVARIANT — highest behavioral risk).** Within a scope,
  Σ active-claim qty for a part ≤ its `on_hand`. Two concurrent draw-activations against the
  same lot(s) must not *both* succeed past available stock — this is a real correctness bug
  under concurrent load that single-threaded tests will NOT catch. Required pattern:

  ```sql
  BEGIN;
  -- lock the candidate lots for this part+scope so concurrent txns serialize on them
  SELECT id, remaining_qty
  FROM lot
  WHERE canonical_part_id = $part AND /* scope via join */ remaining_qty > 0
  FOR UPDATE;
  -- compute current active claims against those lots, verify headroom ≥ requested qty
  -- (re-read active claims INSIDE the locked txn — values pre-lock are stale)
  -- only then INSERT/accumulate the draw
  COMMIT;
  ```
  `SELECT ... FOR UPDATE` on the lot rows is what forces concurrent activations to serialize.
  **This pattern must be implemented once in the data-access layer and covered by an explicit
  concurrent test** (two goroutines racing to over-claim the same lot; exactly one wins).
  tdd note: write that concurrent test first — it's the one invariant accidental coverage misses.
- **Breakage ≤ claim:** a `breakage_event.qty` for a draw ≤ that draw's remaining `qty -
  consumed_qty`. Enforced in the reconciliation transaction.
- **State transitions are atomic:** a project status change that reconciles its draws
  (release / consume / record breakage) happens in one transaction; draws never observed
  out of sync with their project. (See draw lifecycle.)
- **`available` may legitimately surface as negative — do NOT floor it in the source view.**
  If `available` ever goes negative, the no-oversell invariant was violated upstream and you
  want that *visible* (it's a smoke alarm). `GREATEST(0, ...)` belongs only in the UI display
  layer, never in `inventory_available` itself. The reconciliation view below exists to catch
  exactly this.

### RLS-readiness (RLS intentionally NOT used yet)
Tenant isolation is enforced in the Go data-access layer (repositories require a
`(user_id, scope_id)` context). RLS is skipped for now — on a plain-Postgres-behind-a-Go-
API setup it's redundant with the DAL and adds per-transaction `SET LOCAL` plumbing. The
schema is kept **RLS-ready**: every scoped table reaches `scope_id` directly or in one hop,
so policies can be added later as a pure migration without changing table shapes. Revisit
if Postgres is ever exposed more directly (PostgREST, direct frontend access).

---

## Tenancy & scope model

Multi-tenant with **team-based (shared) ownership**. Within a team, inventory is bounded by
an **inventory_scope** — the boundary parts, purchases, and projects live inside. A part is
usable only by a project in the **same scope**.

**Today: exactly one scope per team** (auto-created, "default"). The scope layer exists now
as a *seam* so sub-grouping a team into multiple scopes later is additive/data-only, not a
query rewrite. Ownership already routes through the scope.

Three data classes:

- **Shared catalog (global, ownerless):** `canonical_part`, `offering`, `kit_content`,
  resolved datasheets. Objective facts — same for everyone. No team/scope/owner. Scoped
  tables reference these by FK. Scope never applies to the catalog.
- **Scoped data:** `project` and `purchase_order` are aggregate roots carrying `scope_id`.
  Children (`order_line`, `lot`, `project_requirement`, `draw`, `breakage_event`) inherit
  scope through their parent — **no `scope_id`/`team_id` of their own.**
- **Tenancy:** `user`, `team`, `team_membership`, `inventory_scope`.

**Scope is the unit of inventory and contention.** Every `lot` is born scoped (via
`order_line` → `purchase_order` → `scope_id`), so inventory splits cleanly if a team is
divided later.

**Authorization (data-access layer):** a user may act on a scoped row iff they hold a
`team_membership` in the team owning that scope AND `role` permits it. Repositories take a
`(user_id, scope_id)` context (team derived from scope) and refuse rows outside it. Both
sides of a `draw` must be the same scope.

### `user`
```sql
CREATE TABLE app_user (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),  -- uuidv7() on PG18+
  email       TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX app_user_email_lower_uq ON app_user (LOWER(email));
```
(`user` is a reserved-ish word; table named `app_user`. Auth columns omitted — out of scope.)

### `team`
```sql
CREATE TABLE team (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### `team_membership`
```sql
CREATE TABLE team_membership (
  team_id     UUID NOT NULL REFERENCES team(id) ON DELETE CASCADE,
  user_id     UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  role        TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin','member')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_membership_user_idx ON team_membership (user_id);  -- "teams for a user" (PK prefix is team_id)
```

### `inventory_scope`
```sql
CREATE TABLE inventory_scope (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  team_id     UUID NOT NULL REFERENCES team(id) ON DELETE CASCADE,
  name        TEXT NOT NULL DEFAULT 'default',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX inventory_scope_team_idx ON inventory_scope (team_id);
```
One per team today; multi-per-team is the future additive change.

---

## Part 1 — Relational schema (Postgres)

### Shared catalog (global — no owner, no scope)

#### `canonical_part`  *(shared, global, UUID)*
```sql
CREATE TABLE canonical_part (
  id                     UUID PRIMARY KEY DEFAULT gen_random_uuid(),  -- uuidv7() PG18+
  mpn                    TEXT,            -- nullable for no-name junk
  manufacturer           TEXT,
  description            TEXT,
  package                TEXT,            -- e.g. SOIC-8, 0603
  datasheet_url          TEXT,
  datasheet_resolved_at  TIMESTAMPTZ,     -- null = never attempted; retry only the unresolved
  created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One canonical row per real part; multiple NULLs must NOT collapse (no-name parts stay distinct)
CREATE UNIQUE INDEX canonical_part_mpn_mfr_uq
  ON canonical_part (mpn, manufacturer);   -- NULLS DISTINCT (default): no-name parts never auto-merge
```
Datasheet resolved once, globally (one Octopart hit for everyone).

#### `offering`  *(shared, global, UUID)*
```sql
CREATE TABLE offering (
  id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source             TEXT NOT NULL CHECK (source IN ('digikey','mouser','lcsc','aliexpress','temu')),
  source_external_id TEXT NOT NULL,
  canonical_part_id  UUID REFERENCES canonical_part(id),   -- nullable until resolved
  is_kit             BOOLEAN NOT NULL DEFAULT false,
  url                TEXT,
  seller             TEXT,
  title              TEXT,
  last_seen_price    NUMERIC(12,4),
  currency           TEXT,                -- offering's listed currency (display only; lot/order_line own cost truth)
  last_seen_at       TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  -- a kit has no single canonical part; a non-kit should map to one (once resolved)
  CHECK (NOT is_kit OR canonical_part_id IS NULL)
);
CREATE UNIQUE INDEX offering_source_uq ON offering (source, source_external_id);  -- ingestion upsert key
CREATE INDEX offering_canonical_part_idx ON offering (canonical_part_id);
-- find still-unmapped offerings (resolution queue) cheaply:
CREATE INDEX offering_unmapped_idx ON offering (id) WHERE canonical_part_id IS NULL AND is_kit = false;
```

#### `kit_content`  *(shared, global)*
```sql
CREATE TABLE kit_content (
  offering_id        UUID NOT NULL REFERENCES offering(id) ON DELETE CASCADE,
  canonical_part_id  UUID NOT NULL REFERENCES canonical_part(id),
  qty_per_unit       INTEGER NOT NULL CHECK (qty_per_unit > 0),
  PRIMARY KEY (offering_id, canonical_part_id)
);
CREATE INDEX kit_content_canonical_part_idx ON kit_content (canonical_part_id);  -- "which kits yield this part"
```

#### `part_attribute`  *(shared, global — minimal stub now, populated later)*
Typed, directional parametric axes on a canonical part. Enables **parametric
satisfiability** (a 20V-rated op-amp satisfies a 12V need; a 5V part does NOT satisfy a
strict-3.3V ESP32 supply need) to be *computed* later by a Go resolver — no graph, just
row comparison. Built as a stub now (table exists, shape fixed) so imported/declared specs
have a home; population and the resolver are deferred.

```sql
CREATE TABLE part_attribute (
  id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  canonical_part_id  UUID NOT NULL REFERENCES canonical_part(id) ON DELETE CASCADE,
  axis               TEXT NOT NULL,            -- e.g. 'vmax_rating','supply_voltage','current_rating','current_draw','package'
  -- comparison semantics for THIS axis — the ESP32/amperage insight: betterness is not global
  direction          TEXT NOT NULL CHECK (direction IN ('higher_ok','lower_ok','range','exact')),
  --   higher_ok = capacity/rating (candidate ≥ required satisfies: vmax, current handling)
  --   lower_ok  = candidate ≤ required satisfies (e.g. tolerance, leakage)
  --   range     = value must fall within [value_min,value_max] (supply voltage windows)
  --   exact     = must match exactly (package, pinout) — no ordering
  value_num          NUMERIC,                  -- point value (for higher_ok/lower_ok/exact numeric)
  value_min          NUMERIC,                  -- range lower bound
  value_max          NUMERIC,                  -- range upper bound
  value_text         TEXT,                     -- for exact non-numeric (package code, etc.)
  unit               TEXT,                     -- 'V','A','Ω','°C', …
  source             TEXT NOT NULL DEFAULT 'user'  -- 'user' | 'octopart' | 'digikey' | 'computed' | …
                       CHECK (source IN ('user','octopart','digikey','lcsc','computed')),
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX part_attribute_part_idx ON part_attribute (canonical_part_id);
CREATE INDEX part_attribute_axis_idx ON part_attribute (axis);
```
Parametric satisfiability is **never a graph problem** — it's row-wise attribute comparison
in Postgres. This table never feeds Neo4j.

#### `part_substitution`  *(shared, global — directed FUNCTIONAL edges only)*
The Kind-B store: declared/imported equivalences that **cannot** be derived from specs
(LM339 quad satisfies an LM393 dual need). **Directed and asymmetric** — the quad satisfies
the dual, NOT vice versa. Surrogate PK so a `draw` can FK a specific edge. This table is the
explicit edge list that *is* the substitution graph in relational form — and the only thing
that would ever project into Neo4j (see promotion path at end of file).

```sql
CREATE TABLE part_substitution (
  id                  BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  satisfying_part_id  UUID NOT NULL REFERENCES canonical_part(id) ON DELETE CASCADE,  -- the part you HAVE
  satisfied_part_id   UUID NOT NULL REFERENCES canonical_part(id) ON DELETE CASCADE,  -- the requirement it can FILL
  source              TEXT NOT NULL DEFAULT 'user'  -- 'user' | 'octopart' | … (provenance, not a 2nd mechanism)
                        CHECK (source IN ('user','octopart','digikey','lcsc')),
  note                TEXT,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (satisfying_part_id <> satisfied_part_id),                   -- no self-edge
  UNIQUE (satisfying_part_id, satisfied_part_id)                     -- one directed edge per ordered pair
);
CREATE INDEX part_substitution_satisfied_idx  ON part_substitution (satisfied_part_id);   -- "what can fill THIS requirement"
CREATE INDEX part_substitution_satisfying_idx ON part_substitution (satisfying_part_id);  -- "what does THIS part cover"
```
Only **functional** edges live here. Parametric satisfiability is computed from
`part_attribute`, never stored as edges (the edges would be infinite and brittle).
Transitive closure (A→B→C) is the deferred resolver's job; this table holds direct edges only.

---

### Scoped: purchases (the facts that create inventory)

#### `purchase_order`  *(root — BIGINT, carries `scope_id`)*
```sql
CREATE TABLE purchase_order (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  scope_id        UUID NOT NULL REFERENCES inventory_scope(id),
  source          TEXT NOT NULL CHECK (source IN ('digikey','mouser','lcsc','aliexpress','temu')),
  source_order_id TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'ordered'
                    CHECK (status IN ('ordered','shipped','delivered','cancelled')),
  placed_at       TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX purchase_order_dedupe_uq ON purchase_order (scope_id, source, source_order_id);
CREATE INDEX purchase_order_scope_idx ON purchase_order (scope_id);
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON purchase_order
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```

#### `order_line`  *(inherits scope via `purchase_order`; currency owner)*
```sql
CREATE TABLE order_line (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  order_id     BIGINT NOT NULL REFERENCES purchase_order(id) ON DELETE CASCADE,
  offering_id  UUID NOT NULL REFERENCES offering(id),
  qty          INTEGER NOT NULL CHECK (qty > 0),         -- whole offerings/kits; kits not exploded yet
  unit_price   NUMERIC(12,4) NOT NULL CHECK (unit_price >= 0),
  currency     TEXT NOT NULL,                            -- AUTHORITATIVE currency for this purchase
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX order_line_order_idx ON order_line (order_id);
CREATE INDEX order_line_offering_idx ON order_line (offering_id);
```

#### `lot` — **inventory truth**  *(inherits scope via `order_line` → `purchase_order`)*
A lot is created when an order line lands. A **kit order line explodes into N lots** (one
per kit_content), all tagged with the same `source_order_line_id` for provenance. Inventory
is per-scope; every lot is born scoped.

```sql
CREATE TABLE lot (
  id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  canonical_part_id     UUID NOT NULL REFERENCES canonical_part(id),
  source_order_line_id  BIGINT NOT NULL REFERENCES order_line(id),
  qty_received          INTEGER NOT NULL CHECK (qty_received > 0),
  remaining_qty         INTEGER NOT NULL CHECK (remaining_qty >= 0),
  unit_cost             NUMERIC(12,4) CHECK (unit_cost IS NULL OR unit_cost >= 0),  -- per-part cost; NULL for kit-derived lots (no per-part allocation)
  currency              TEXT NOT NULL,    -- audit snapshot of order_line.currency at creation (intentional copy)
  received_at           TIMESTAMPTZ,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (remaining_qty <= qty_received)
);
CREATE INDEX lot_canonical_part_idx ON lot (canonical_part_id);
CREATE INDEX lot_order_line_idx ON lot (source_order_line_id);
-- hot path: availability only ever sums lots with stock left
CREATE INDEX lot_in_stock_idx ON lot (canonical_part_id) WHERE remaining_qty > 0;
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON lot
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```

`remaining_qty` = physical stock = `qty_received` − permanently consumed. **Active reversible
claims do NOT touch `remaining_qty`** — they reduce *availability* (the view), not stock.

**Lot creation rule (resolution):**
- non-kit offering → 1 lot: `qty_received = order_line.qty`, via `offering.canonical_part_id`; `unit_cost = order_line.unit_price`.
- kit offering → N lots, one per `kit_content`: `qty_received = order_line.qty × kit_content.qty_per_unit`; **`unit_cost = NULL`** — kit cost is NOT split across parts. The kit's spend lives on `order_line` (`unit_price × qty`) and is the source of truth for what a kit cost; per-part cost is genuinely unknown, so it's null rather than a fabricated allocation.

**Cost rollup:** project/scope spend sums `order_line` (purchase-level truth), not `lot.unit_cost`. `lot.unit_cost` is a convenience for non-kit per-part costing and is null wherever a kit makes per-part cost meaningless. Don't `SUM(lot.unit_cost)` for total spend — it omits kit lots; sum `order_line` instead.

---

### Scoped: demand (projects)

#### `project`  *(root — BIGINT, carries `scope_id`)*
```sql
CREATE TABLE project (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  scope_id    UUID NOT NULL REFERENCES inventory_scope(id),
  name        TEXT NOT NULL,
  status      TEXT NOT NULL DEFAULT 'planning'
                CHECK (status IN ('planning','active','paused','cancelled','built','archived')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX project_scope_idx ON project (scope_id);
-- contention/availability joins filter active projects constantly:
CREATE INDEX project_active_idx ON project (scope_id) WHERE status = 'active';
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON project
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```
**Project status is the single source of truth for whether a draw is a live claim** — see
the lifecycle. There is no separate stored draw-status to drift from it.

#### `project_requirement`  *(inherits scope via `project`)*
```sql
CREATE TABLE project_requirement (
  id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id         BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  canonical_part_id  UUID NOT NULL REFERENCES canonical_part(id),
  qty_required       INTEGER NOT NULL CHECK (qty_required > 0),
  UNIQUE (project_id, canonical_part_id)
);
CREATE INDEX project_requirement_project_idx ON project_requirement (project_id);
CREATE INDEX project_requirement_canonical_part_idx ON project_requirement (canonical_part_id);
```

#### `draw` — claim against a lot  *(state DERIVED from project; not stored)*
A draw links a requirement to a specific lot. **Its live/claimed meaning is read from the
parent project's status — there is no stored `status` column to drift.** A draw row simply
records "this requirement claims `qty` from this lot." Whether that claim is *active*
(blocking others), *released*, or *consumed* is a function of the project's status plus
whether the qty has been permanently consumed (tracked via `consumed_qty`).

```sql
CREATE TABLE draw (
  id                      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_requirement_id  BIGINT NOT NULL REFERENCES project_requirement(id) ON DELETE CASCADE,
  lot_id                  BIGINT NOT NULL REFERENCES lot(id),
  qty                     INTEGER NOT NULL CHECK (qty > 0),       -- claimed quantity (accumulated)
  consumed_qty            INTEGER NOT NULL DEFAULT 0 CHECK (consumed_qty >= 0),  -- permanently gone (built or broken)
  -- Substitution justification: NULL when the lot's part == the requirement's part (exact match).
  -- When a user knowingly fulfills a requirement with a different part, record WHY it was legal.
  satisfied_via_kind         TEXT CHECK (satisfied_via_kind IN ('functional','parametric')),
  satisfied_via_substitution BIGINT REFERENCES part_substitution(id),  -- the exact edge, iff functional
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (consumed_qty <= qty),
  -- discriminated reference: exact match | functional (edge FK) | parametric (computed, no edge)
  CHECK (
       (satisfied_via_kind IS NULL        AND satisfied_via_substitution IS NULL)
    OR (satisfied_via_kind = 'functional' AND satisfied_via_substitution IS NOT NULL)
    OR (satisfied_via_kind = 'parametric' AND satisfied_via_substitution IS NULL)
  ),
  -- ONE row per (requirement, lot): a draw is a current-state fact ("this requirement
  -- claims N from this lot"), not an event. Repeated claims accumulate qty in the same row.
  -- History lives in event tables (breakage_event, build records), not in duplicate draws.
  -- This UNIQUE is also load-bearing: it makes the claim upsert atomic (see below).
  CONSTRAINT draw_req_lot_uq UNIQUE (project_requirement_id, lot_id)
);
CREATE INDEX draw_lot_idx ON draw (lot_id);
CREATE INDEX draw_requirement_idx ON draw (project_requirement_id);
CREATE INDEX draw_substitution_idx ON draw (satisfied_via_substitution)
  WHERE satisfied_via_substitution IS NOT NULL;  -- "every draw that used edge X"
-- availability subtracts claims belonging to ACTIVE projects; that filter lives on project,
-- so the hot join is draw -> project_requirement -> project (active). Index both FK sides (above).
CREATE TRIGGER trg_set_updated_at BEFORE UPDATE ON draw
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
```

**Substitution is user-driven at draw time, never automatic.** When fulfilling a
requirement, the resolver (deferred) offers candidates — the exact part plus parts that
satisfy it (parametric via `part_attribute`, functional via `part_substitution`) — and the
**user picks**. The draw is created against the chosen part's lot; if that part ≠ the
requirement's part, `satisfied_via_kind` records why it was legal (`functional` + the edge
FK for full traceability, or `parametric` for a computed match). Exact matches leave both
NULL. Strict `project_coverage` stays exact-match; a substitute-aware coverage view is
additive and deferred.

**Claim write-path is an upsert** (atomic via `draw_req_lot_uq`); runs inside the
no-oversell `FOR UPDATE` transaction:
```sql
INSERT INTO draw (project_requirement_id, lot_id, qty)
VALUES ($req, $lot, $qty)
ON CONFLICT (project_requirement_id, lot_id)
DO UPDATE SET qty = draw.qty + EXCLUDED.qty;   -- trigger bumps updated_at
```

**Release (partial) decrements `qty`; if it would reach 0, DELETE the row** rather than
leave a zero-qty draw — keeps `draw` to live claims only (`qty > 0` CHECK also forbids the
zero row). Release only ever reduces the *non-consumed* portion: `qty` can't drop below
`consumed_qty` (consumed parts are gone, not releasable).

**Why no stored `draw.status`:** the previous design stored a derived status, which could
drift from `project.status`. Now "is this an active claim?" is answered by joining to the
project. The only *fact* a draw needs beyond `qty` is how much has been **permanently
consumed** (`consumed_qty`) — that's not derivable from project status (a paused project
might have 3 broken units consumed and the rest released), so it's stored, and audited in
`breakage_event` / build records.

#### Draw lifecycle (driven by `project.status`, applied atomically)
| project transition | effect (one transaction) |
|---|---|
| → `active` | its draws become **hard claims** — counted against availability. No row change needed; the active filter does it. |
| `active` → `paused`/`cancelled` | reconcile each draw: optionally record broken/used qty (→ `breakage_event`, increment `consumed_qty`, decrement `lot.remaining_qty`); the rest is implicitly released (project no longer active ⇒ its draws stop counting against availability). |
| `active` → `built` | mark each draw's remaining claimed qty as consumed: set `consumed_qty = qty`, decrement `lot.remaining_qty` accordingly (record consumption). |

No discretionary "commit." Permanent consumption is always the outcome of a build or a
breakage report. `lot.remaining_qty` only ever decreases via `consumed_qty` increments,
inside these transitions.

#### `breakage_event` — audit trail for permanently lost parts
```sql
CREATE TABLE breakage_event (
  id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  draw_id      BIGINT NOT NULL REFERENCES draw(id),
  qty          INTEGER NOT NULL CHECK (qty > 0),
  kind         TEXT NOT NULL DEFAULT 'broken' CHECK (kind IN ('broken','used','build_consumed')),
  reason       TEXT,
  reported_by  UUID NOT NULL REFERENCES app_user(id),
  reported_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX breakage_event_draw_idx ON breakage_event (draw_id);
```
Generalized with a `kind` so it doubles as the consumption audit (breakage *and* build
consumption flow through it). `lot_id` intentionally omitted as redundant — reachable via
`draw_id → lot_id`; the audit fact is the event + its draw, and the draw's lot is immutable.

---

### Derived views (scope-scoped)

`remaining_qty` is physical stock. Availability additionally subtracts **active hard claims**
(draws whose project is `active`, minus already-consumed qty). The previous correlated
subquery is replaced with a grouped `LEFT JOIN LATERAL` to avoid per-lot re-execution.

#### `inventory_available`
```sql
CREATE VIEW inventory_available AS
SELECT
  po.scope_id,
  l.canonical_part_id,
  SUM(l.remaining_qty) AS on_hand,
  SUM(l.remaining_qty - COALESCE(ac.active_claim, 0)) AS available
FROM lot l
JOIN order_line ol     ON ol.id = l.source_order_line_id
JOIN purchase_order po ON po.id = ol.order_id
LEFT JOIN LATERAL (
  SELECT SUM(d.qty - d.consumed_qty) AS active_claim
  FROM draw d
  JOIN project_requirement pr ON pr.id = d.project_requirement_id
  JOIN project p              ON p.id  = pr.project_id
  WHERE d.lot_id = l.id
    AND p.status = 'active'
) ac ON true
WHERE l.remaining_qty > 0
GROUP BY po.scope_id, l.canonical_part_id;
```
`on_hand` = physical stock in scope. `available` = on_hand minus active (uncommitted) claims
— what another project in the scope can take now.

#### `project_coverage`
```sql
CREATE VIEW project_coverage AS
SELECT
  p.scope_id,
  pr.project_id,
  pr.canonical_part_id,
  pr.qty_required,
  COALESCE(ia.available, 0) AS available,
  COALESCE(ia.available, 0) >= pr.qty_required AS satisfied
FROM project_requirement pr
JOIN project p ON p.id = pr.project_id
LEFT JOIN inventory_available ia
  ON ia.canonical_part_id = pr.canonical_part_id
 AND ia.scope_id = p.scope_id;
```
Scale note: if `project_coverage` becomes a read hotspot on a large dataset, promote to a
**materialized view** refreshed on draw/lot/project-status change. Not now (premature) — the
indexes above keep the live view cheap at current scale.

**Predicate-pushdown caveat (verify with EXPLAIN).** `project_coverage` selects from the
`inventory_available` view, which has a `GROUP BY`. Postgres usually pushes filters down
through views, but **aggregation can block pushdown** — a query filtered to one
part/scope may force `inventory_available` to aggregate *all* inventory before the filter
applies. Before relying on either view in a hot path, run `EXPLAIN (ANALYZE, BUFFERS)` on a
realistic scoped query. If the planner materializes the full aggregate, inline the
availability computation into `project_coverage` (one query, filter applied before the
GROUP BY) or materialize. Don't assume pushdown — measure it.

#### Contention (who holds a contended part — within scope)
```sql
SELECT po.scope_id, l.canonical_part_id, proj.id AS holding_project, (d.qty - d.consumed_qty) AS held
FROM draw d
JOIN project_requirement pr ON pr.id = d.project_requirement_id
JOIN project proj           ON proj.id = pr.project_id AND proj.status = 'active'
JOIN lot l                  ON l.id = d.lot_id
JOIN order_line ol          ON ol.id = l.source_order_line_id
JOIN purchase_order po      ON po.id = ol.order_id
WHERE d.qty > d.consumed_qty;
```

#### `lot_reconciliation` (audit / drift detection)
Catches inventory drift: for each lot, physical stock should equal received minus
everything permanently consumed against it. Any row where this fails — or where active
claims exceed physical stock — is a corruption signal (e.g. a missed `remaining_qty`
decrement, or the no-oversell invariant breached). This is the view that *surfaces* the
negatives item-7 deliberately does not floor.

```sql
CREATE VIEW lot_reconciliation AS
SELECT
  l.id AS lot_id,
  l.canonical_part_id,
  l.qty_received,
  l.remaining_qty,
  COALESCE(SUM(d.consumed_qty), 0)               AS total_consumed,
  COALESCE(SUM(d.qty - d.consumed_qty)
           FILTER (WHERE p.status = 'active'), 0) AS active_claims,
  -- expected physical stock = received - permanently consumed
  l.qty_received - COALESCE(SUM(d.consumed_qty), 0) AS expected_remaining,
  (l.remaining_qty <> l.qty_received - COALESCE(SUM(d.consumed_qty), 0)) AS remaining_drift,
  (COALESCE(SUM(d.qty - d.consumed_qty) FILTER (WHERE p.status = 'active'), 0)
     > l.remaining_qty) AS oversold
FROM lot l
LEFT JOIN draw d                ON d.lot_id = l.id
LEFT JOIN project_requirement pr ON pr.id = d.project_requirement_id
LEFT JOIN project p              ON p.id = pr.project_id
GROUP BY l.id;
-- healthy system: zero rows with remaining_drift OR oversold true.
-- wire this into a periodic check / test assertion.
```

---

## Part 2 — Relationships & traversal (Postgres now; Neo4j only if/when)

**There is no graph database in this design.** The relationships a graph would hold already
exist as Postgres edge tables: `kit_content` (kit→part, "YIELDS"), `project_requirement`
(project→part, "REQUIRES"), and `part_substitution` (part→part, directed "SATISFIES"). The
queries a graph was meant to answer are flat or shallow here, so Postgres handles them:

- **"Which projects does a kit touch / would it unblock?"** — fixed 2-hop join over
  `kit_content` × `project_requirement` × `project_coverage`. Not recursive (a kit yields
  parts; parts aren't kits → no nesting). Plain SQL.
- **"What can fulfill this requirement?"** (substitution resolver, deferred) — parametric
  via row comparison over `part_attribute`; functional via 1–2 hop follow of
  `part_substitution`. Shallow enough for `WITH RECURSIVE` if chaining is ever needed;
  electronics substitution chains are typically 1–2 hops.
- **"Minimum offerings to cover project X's unmet needs"** — set-cover is NP-hard and solved
  by an **algorithm in Go** over candidates pulled by a flat query. Neither Postgres nor a
  graph DB solves the optimization; a graph DB would not help the hard part.

### Neo4j: when, and only when (promotion path)
Adopt Neo4j **only if both**: (a) the functional-substitution network (`part_substitution`)
becomes genuinely **deep and dense** (long transitive chains queried by traversal), AND
(b) recursive-CTE traversal over it is a **measured** bottleneck. Parametric satisfiability
is never a graph problem (row comparison) and never motivates Neo4j.

Because `part_substitution` is stored as **explicit, directed, typed edges**, promotion is
additive and trivial — project rows into Neo4j as a read-optimized index; Postgres stays the
source of truth:
```
// one-time / incremental projection — NOT a redesign
for each row r in part_substitution:
    MERGE (a:CanonicalPart {id: r.satisfying_part_id})
    MERGE (b:CanonicalPart {id: r.satisfied_part_id})
    MERGE (a)-[:SATISFIES {source: r.source}]->(b)
```
This is the insurance against "switching will be hard": the *data model* (directed, typed,
one-edge-per-row, its own table) is what makes the engine swap cheap. Getting the relational
edges right now is the design decision; the graph engine is a deferrable accelerator. If
adopted, it inherits the dual-write/sync burden (invariant #5) — another reason to defer
until measured need.

---

## Open decisions (don't silently pick — flag them)

- **Roles enum / permission matrix**: `team_membership.role` starts `admin`|`member`. Define
  who can transition project status and file breakage/consumption (the irreversible, shared-
  inventory actions) before writing authorization. Expand the enum only when a real
  distinction appears.
- **Fractional quantities**: confirm which parts (if any) are measured (wire/solder length)
  and switch only those qty columns to `NUMERIC`; everything else stays integer.
- **Scope membership (future)**: when multiple scopes per team arrive, whether membership
  gates which members see/use which scopes. Seam exists; not modeled.
- **Coverage materialization (future)**: promote `project_coverage` to a materialized view
  if it becomes a read hotspot. Indexes suffice for now.
- **Substitution resolver (deferred)**: the Go logic that, given a requirement, returns
  candidate parts — parametric (compare `part_attribute` per axis/direction) + functional
  (follow `part_substitution` edges). Data lives now; resolver and the substitute-aware
  coverage view are built later. User picks a candidate at draw time; strict
  `project_coverage` stays exact-match.
- **`part_attribute` population**: the table is a stub; the axis vocabulary
  (`vmax_rating`, `supply_voltage`, `current_draw`, …) and import mapping are TBD when the
  resolver is built.
- **Unmapped offerings**: arrive with `canonical_part_id = null`, resolved later via the
  `offering_unmapped_idx` queue; ingestion never blocks on resolution. (Once ingestion exists.)
