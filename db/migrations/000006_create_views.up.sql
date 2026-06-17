-- Availability: physical stock minus active (hard) claims per scope+part.
-- active_claim = draws whose project is active, minus already-consumed qty.
-- NOTE: available may be negative — do NOT floor it here. Negative = oversell bug, must be visible.
CREATE VIEW inventory_available AS
SELECT
  po.scope_id,
  l.canonical_part_id,
  SUM(l.remaining_qty)                              AS on_hand,
  SUM(l.remaining_qty - COALESCE(ac.active_claim, 0)) AS available
FROM lot l
JOIN order_line ol     ON ol.id = l.source_order_line_id
JOIN purchase_order po ON po.id = ol.order_id
LEFT JOIN LATERAL (
  SELECT SUM(d.qty - d.consumed_qty) AS active_claim
  FROM draw d
  JOIN project_requirement pr ON pr.id = d.project_requirement_id
  JOIN project p               ON p.id  = pr.project_id
  WHERE d.lot_id = l.id
    AND p.status = 'active'
) ac ON true
WHERE l.remaining_qty > 0
GROUP BY po.scope_id, l.canonical_part_id;

-- Coverage: per-requirement satisfaction check (exact-match only; substitute-aware view is deferred).
-- Predicate-pushdown caveat: aggregation in inventory_available may block filter pushdown.
-- Run EXPLAIN (ANALYZE, BUFFERS) on scoped queries before relying on this in a hot path.
CREATE VIEW project_coverage AS
SELECT
  p.scope_id,
  pr.project_id,
  pr.canonical_part_id,
  pr.qty_required,
  COALESCE(ia.available, 0)                    AS available,
  COALESCE(ia.available, 0) >= pr.qty_required AS satisfied
FROM project_requirement pr
JOIN project p ON p.id = pr.project_id
LEFT JOIN inventory_available ia
  ON  ia.canonical_part_id = pr.canonical_part_id
  AND ia.scope_id           = p.scope_id;

-- Reconciliation audit: healthy system has zero rows where remaining_drift OR oversold is true.
CREATE VIEW lot_reconciliation AS
SELECT
  l.id AS lot_id,
  l.canonical_part_id,
  l.qty_received,
  l.remaining_qty,
  COALESCE(SUM(d.consumed_qty), 0)                                                    AS total_consumed,
  COALESCE(SUM(d.qty - d.consumed_qty) FILTER (WHERE p.status = 'active'), 0)        AS active_claims,
  l.qty_received - COALESCE(SUM(d.consumed_qty), 0)                                  AS expected_remaining,
  (l.remaining_qty <> l.qty_received - COALESCE(SUM(d.consumed_qty), 0))             AS remaining_drift,
  (COALESCE(SUM(d.qty - d.consumed_qty) FILTER (WHERE p.status = 'active'), 0)
     > l.remaining_qty)                                                               AS oversold
FROM lot l
LEFT JOIN draw d                 ON d.lot_id = l.id
LEFT JOIN project_requirement pr ON pr.id = d.project_requirement_id
LEFT JOIN project p              ON p.id  = pr.project_id
GROUP BY l.id;
