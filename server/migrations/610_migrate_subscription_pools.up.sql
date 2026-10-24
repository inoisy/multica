-- Moves the pool of every runtime profile that spends accounts, or carries a
-- share_key, into subscription_pool. A group is the copies that share one
-- share_key; a profile without a key is its own group (an empty key does not
-- join independent profiles). The earliest copy gives name, description,
-- members and order; every copy gets pool_id and a grant.
--
-- Everything is checked before anything is written. A group whose copies
-- disagree, that lists an account twice, or that has two copies in one
-- workspace raises with its key: align it through the API and run again.
-- A key without an account is skipped (NOTICE); it never took part in a choice.
--
-- Repeatable: copies that already have pool_id are left alone, and a copy added
-- later to a migrated share_key joins the pool created for that key.
DO $$
DECLARE
    bad record;
    grp record;
    src record;
    v_pool uuid;
    missing text;
BEGIN
    FOR bad IN
        SELECT rp.share_key
        FROM runtime_profile rp
        WHERE nullif(rp.share_key, '') IS NOT NULL AND rp.pool_id IS NULL
        GROUP BY rp.share_key
        HAVING count(DISTINCT rp.subscriptions::text) > 1
            OR count(DISTINCT rp.spend_order) > 1
            OR count(DISTINCT rp.protocol_family) > 1
    LOOP
        RAISE EXCEPTION 'share_key % has copies that disagree on subscriptions, spend_order or protocol_family; align them through the API and run the migration again', bad.share_key;
    END LOOP;

    FOR bad IN
        SELECT rp.id, d.key
        FROM runtime_profile rp
        CROSS JOIN LATERAL (
            SELECT e AS key FROM jsonb_array_elements_text(rp.subscriptions) e GROUP BY e HAVING count(*) > 1
        ) d
        WHERE rp.pool_id IS NULL
    LOOP
        RAISE EXCEPTION 'runtime profile % lists subscription % twice; fix the list through the API and run the migration again', bad.id, bad.key;
    END LOOP;

    FOR bad IN
        SELECT rp.share_key, rp.workspace_id
        FROM runtime_profile rp
        WHERE nullif(rp.share_key, '') IS NOT NULL AND rp.pool_id IS NULL
        GROUP BY rp.share_key, rp.workspace_id
        HAVING count(*) > 1
    LOOP
        RAISE EXCEPTION 'share_key % has more than one copy in workspace %; remove the extra copy and run the migration again', bad.share_key, bad.workspace_id;
    END LOOP;

    FOR bad IN
        SELECT coalesce(nullif(rp.share_key, ''), rp.id::text) AS ref, rp.protocol_family
        FROM runtime_profile rp
        WHERE rp.pool_id IS NULL
          AND (jsonb_array_length(rp.subscriptions) > 0 OR nullif(rp.share_key, '') IS NOT NULL)
          AND rp.protocol_family NOT IN ('claude', 'codex')
    LOOP
        RAISE EXCEPTION 'runtime profile or share_key % is a % runtime; a pool runs on claude or codex only', bad.ref, bad.protocol_family;
    END LOOP;

    FOR grp IN
        SELECT CASE WHEN nullif(rp.share_key, '') IS NOT NULL THEN 'shared:' || rp.share_key ELSE 'profile:' || rp.id::text END AS gkey,
               min(nullif(rp.share_key, '')) AS share_key,
               bool_or(rp.all_workspaces) AS all_workspaces,
               min(rp.created_at) AS first_at
        FROM runtime_profile rp
        WHERE rp.pool_id IS NULL
          AND (jsonb_array_length(rp.subscriptions) > 0 OR nullif(rp.share_key, '') IS NOT NULL)
        GROUP BY 1
        ORDER BY min(rp.created_at), 1
    LOOP
        SELECT rp.* INTO src
        FROM runtime_profile rp
        WHERE rp.pool_id IS NULL
          AND (CASE WHEN nullif(rp.share_key, '') IS NOT NULL THEN 'shared:' || rp.share_key ELSE 'profile:' || rp.id::text END) = grp.gkey
        ORDER BY rp.created_at, rp.id
        LIMIT 1;

        v_pool := NULL;
        IF grp.share_key IS NOT NULL THEN
            SELECT p.id INTO v_pool FROM subscription_pool p WHERE p.legacy_share_key = grp.share_key ORDER BY p.created_at, p.id LIMIT 1;
        END IF;

        IF v_pool IS NULL THEN
            v_pool := gen_random_uuid();
            INSERT INTO subscription_pool (id, name, runtime_type, description, spend_order, all_workspaces, revision, legacy_share_key, created_by, created_at, updated_at)
            VALUES (v_pool, left(btrim(src.display_name), 100), src.protocol_family, coalesce(src.description, ''), src.spend_order,
                    grp.all_workspaces, 1, grp.share_key, src.created_by, src.created_at, now());

            SELECT string_agg(e.key, ', ') INTO missing
            FROM jsonb_array_elements_text(src.subscriptions) AS e(key)
            WHERE NOT EXISTS (SELECT 1 FROM subscription s WHERE s.key = e.key);
            IF missing IS NOT NULL THEN
                RAISE NOTICE 'pool "%": subscription key(s) % not found, skipped', src.display_name, missing;
            END IF;

            INSERT INTO subscription_pool_member (pool_id, subscription_id, position)
            SELECT v_pool, x.id, (row_number() OVER (ORDER BY x.ord))::int
            FROM (
                SELECT s.id, e.ord
                FROM jsonb_array_elements_text(src.subscriptions) WITH ORDINALITY AS e(key, ord)
                JOIN subscription s ON s.key = e.key
            ) x;
        END IF;

        UPDATE runtime_profile rp SET pool_id = v_pool
        WHERE rp.pool_id IS NULL
          AND (CASE WHEN nullif(rp.share_key, '') IS NOT NULL THEN 'shared:' || rp.share_key ELSE 'profile:' || rp.id::text END) = grp.gkey;

        INSERT INTO subscription_pool_grant (pool_id, workspace_id)
        SELECT DISTINCT v_pool, rp.workspace_id
        FROM runtime_profile rp
        WHERE rp.pool_id = v_pool
          AND NOT EXISTS (SELECT 1 FROM subscription_pool_grant g WHERE g.pool_id = v_pool AND g.workspace_id = rp.workspace_id);
    END LOOP;
END $$;
