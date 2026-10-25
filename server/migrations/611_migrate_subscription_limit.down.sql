-- Gives back caps and reserve from the backup where caps were not edited since
-- the move; an edited caps stays as it is, with reserve 0.
UPDATE subscription s SET
    caps = b.caps,
    reserve = b.reserve,
    updated_at = now()
FROM subscription_limit_backup b
WHERE b.id = s.id
  AND s.reserve = 0
  AND s.caps = coalesce((
        SELECT jsonb_object_agg(x.k, x.lim) FROM (
            SELECT w.k, least(coalesce((b.caps ->> w.k)::int, 100), 100 - b.reserve) AS lim
            FROM (
                SELECT jsonb_array_elements_text(s.windows) AS k
                UNION
                SELECT jsonb_object_keys(b.caps) AS k
            ) w
        ) x WHERE x.lim < 100), '{}'::jsonb);

DROP TABLE IF EXISTS subscription_limit_backup;
