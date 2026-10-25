-- One limit per window: the cap agents stop at is min(cap, 100 - reserve), and
-- reserve goes away. caps/reserve of every account that has a reserve are
-- saved to subscription_limit_backup first (kept for the rollback), then the
-- reserve is folded into caps. Windows whose limit is 100 are not stored; the
-- effective_caps the daemon reads are the same before and after.
CREATE TABLE IF NOT EXISTS subscription_limit_backup (
    id uuid NOT NULL,
    caps jsonb NOT NULL,
    reserve integer NOT NULL,
    saved_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO subscription_limit_backup (id, caps, reserve)
SELECT s.id, s.caps, s.reserve
FROM subscription s
WHERE s.reserve <> 0
  AND NOT EXISTS (SELECT 1 FROM subscription_limit_backup b WHERE b.id = s.id);

UPDATE subscription s SET
    caps = coalesce((
        SELECT jsonb_object_agg(x.k, x.lim) FROM (
            SELECT w.k, least(coalesce((s.caps ->> w.k)::int, 100), 100 - s.reserve) AS lim
            FROM (
                SELECT jsonb_array_elements_text(s.windows) AS k
                UNION
                SELECT jsonb_object_keys(s.caps) AS k
            ) w
        ) x WHERE x.lim < 100), '{}'::jsonb),
    reserve = 0,
    updated_at = now()
WHERE s.reserve <> 0;
