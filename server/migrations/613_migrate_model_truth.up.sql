-- One source of the model choice: every model agent runs on a profile, and
-- the agent's runtime_id/runtime_mode/model/thinking_level are the stored
-- projection of row 1 of that profile.
--
-- 1. A model agent without a profile (the "Бенч z.ai" kind) gets a personal
--    profile of one row made from its fields. The row keeps the agent's custom
--    args, so what the server would choose for it does not change. The hidden
--    accept carrier is not a model agent and is left alone.
-- 2. Every agent on a profile gets row 1 as its fields.
-- 3. An account_group that holds a single account is cleared: a group links
--    several records of one real account, one record has nothing to link.
--
-- Pool order, grants, caps and the choice itself are not touched. The original
-- rows of everything changed go to model_truth_backup for the down migration.
-- Repeatable: an agent that already has a profile is not given another, a
-- backup row is written once, a second run changes nothing.
CREATE TABLE IF NOT EXISTS model_truth_backup (
    kind text NOT NULL,
    id uuid NOT NULL,
    data jsonb NOT NULL,
    saved_at timestamptz NOT NULL DEFAULT now()
);

DO $$
DECLARE
    a record;
    new_id uuid;
    final_name text;
BEGIN
    -- Originals of the agents that the steps below will change.
    INSERT INTO model_truth_backup (kind, id, data)
    SELECT 'agent', ag.id, jsonb_build_object(
        'runtime_id', ag.runtime_id, 'runtime_mode', ag.runtime_mode, 'model', ag.model,
        'thinking_level', ag.thinking_level, 'model_profile_id', ag.model_profile_id)
    FROM agent ag
    WHERE NOT (ag.kind = 'system' AND coalesce(ag.system_key, '') = 'accept')
      AND NOT EXISTS (SELECT 1 FROM model_truth_backup b WHERE b.kind = 'agent' AND b.id = ag.id)
      AND (
        (ag.model_profile_id IS NULL AND ag.runtime_id IS NOT NULL)
        OR EXISTS (
            SELECT 1 FROM model_profile_option o
            JOIN agent_runtime r ON r.id = o.runtime_id
            WHERE o.profile_id = ag.model_profile_id
              AND o.position = (SELECT min(m.position) FROM model_profile_option m WHERE m.profile_id = o.profile_id)
              AND (ag.runtime_id IS DISTINCT FROM o.runtime_id
                OR ag.runtime_mode IS DISTINCT FROM r.runtime_mode
                OR ag.model IS DISTINCT FROM NULLIF(o.model, '')
                OR ag.thinking_level IS DISTINCT FROM NULLIF(o.thinking_level, ''))
        )
      );

    FOR a IN
        SELECT ag.id, ag.workspace_id, ag.name, ag.runtime_id, ag.model, ag.thinking_level, ag.custom_args
        FROM agent ag
        WHERE ag.model_profile_id IS NULL
          AND ag.runtime_id IS NOT NULL
          AND NOT (ag.kind = 'system' AND coalesce(ag.system_key, '') = 'accept')
          AND NOT EXISTS (SELECT 1 FROM model_profile p WHERE p.owner_agent_id = ag.id)
        ORDER BY ag.workspace_id, ag.created_at, ag.id
    LOOP
        final_name := left(btrim(a.name), 70);
        IF EXISTS (SELECT 1 FROM model_profile p WHERE p.workspace_id = a.workspace_id AND lower(p.name) = lower(final_name)) THEN
            final_name := left(btrim(a.name), 60) || ' · ' || left(a.id::text, 8);
        END IF;

        new_id := gen_random_uuid();
        INSERT INTO model_profile (id, workspace_id, name, description, when_exhausted_profile_id, created_by, owner_agent_id, based_on_profile_id)
        VALUES (new_id, a.workspace_id, final_name, '', NULL, NULL, a.id, NULL);

        INSERT INTO model_profile_option (workspace_id, profile_id, position, runtime_id, model, thinking_level, custom_args)
        VALUES (a.workspace_id, new_id, 1, a.runtime_id, coalesce(a.model, ''), coalesce(a.thinking_level, ''), coalesce(a.custom_args, '[]'::jsonb));

        INSERT INTO model_truth_backup (kind, id, data) VALUES ('profile', new_id, jsonb_build_object('agent_id', a.id));

        UPDATE agent SET model_profile_id = new_id WHERE id = a.id;
    END LOOP;

    UPDATE agent ag SET
        runtime_id = o.runtime_id,
        runtime_mode = r.runtime_mode,
        model = NULLIF(o.model, ''),
        thinking_level = NULLIF(o.thinking_level, ''),
        updated_at = now()
    FROM model_profile_option o
    JOIN agent_runtime r ON r.id = o.runtime_id
    WHERE o.profile_id = ag.model_profile_id
      AND o.position = (SELECT min(m.position) FROM model_profile_option m WHERE m.profile_id = o.profile_id)
      AND NOT (ag.kind = 'system' AND coalesce(ag.system_key, '') = 'accept')
      AND (ag.runtime_id IS DISTINCT FROM o.runtime_id
        OR ag.runtime_mode IS DISTINCT FROM r.runtime_mode
        OR ag.model IS DISTINCT FROM NULLIF(o.model, '')
        OR ag.thinking_level IS DISTINCT FROM NULLIF(o.thinking_level, ''));

    INSERT INTO model_truth_backup (kind, id, data)
    SELECT 'account_group', s.id, jsonb_build_object('account_group', s.account_group)
    FROM subscription s
    WHERE s.account_group <> ''
      AND (SELECT count(*) FROM subscription x WHERE x.account_group = s.account_group) = 1
      AND NOT EXISTS (SELECT 1 FROM model_truth_backup b WHERE b.kind = 'account_group' AND b.id = s.id);

    UPDATE subscription s SET account_group = '', updated_at = now()
    WHERE s.account_group <> ''
      AND (SELECT count(*) FROM subscription x WHERE x.account_group = s.account_group) = 1;
END $$;
