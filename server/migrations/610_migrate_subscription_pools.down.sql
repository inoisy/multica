-- Back to runtime_profile.subscriptions / spend_order / share_key /
-- all_workspaces, from the pool as it is now, so edits made after the move are
-- kept. A pool made after the move and granted to several workspaces (or to
-- all) gets a generated share_key so its copies stay one runtime.
DO $$
BEGIN
    UPDATE runtime_profile rp
    SET subscriptions = coalesce((
            SELECT jsonb_agg(s.key ORDER BY m.position)
            FROM subscription_pool_member m
            JOIN subscription s ON s.id = m.subscription_id
            WHERE m.pool_id = p.id
        ), '[]'::jsonb),
        spend_order = p.spend_order,
        all_workspaces = p.all_workspaces,
        share_key = CASE
            WHEN p.legacy_share_key IS NOT NULL THEN p.legacy_share_key
            WHEN p.all_workspaces OR (SELECT count(*) FROM subscription_pool_grant g WHERE g.pool_id = p.id) > 1
                THEN 'pool-' || left(md5(p.id::text), 16)
            ELSE NULL
        END
    FROM subscription_pool p
    WHERE rp.pool_id = p.id;

    UPDATE runtime_profile SET pool_id = NULL WHERE pool_id IS NOT NULL;
    DELETE FROM subscription_pool_grant;
    DELETE FROM subscription_pool_member;
    DELETE FROM subscription_pool;
END $$;
