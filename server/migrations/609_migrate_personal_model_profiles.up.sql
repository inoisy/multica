-- Moves every agent's own list (agent_model_option) into a personal model
-- profile. The agent then points at it through agent.model_profile_id; its old
-- shared profile survives as based_on_profile_id. Nobody is moved to a shared
-- profile: a list that equals its profile stays a personal profile.
--
-- Repeatable: an agent that already owns a personal profile is skipped.
-- agent_model_option is kept as it is until the old schema is removed.
--
-- Names: "<agent> · личный"; when the lower-cased name is taken in the
-- workspace (by a profile, or by the personal profile of an agent named
-- "Claude" next to one named "claude") the first 8 characters of the agent id
-- are added.
DO $$
DECLARE
    a record;
    new_id uuid;
    final_name text;
    based uuid;
BEGIN
    FOR a IN
        SELECT ag.id, ag.workspace_id, ag.name, ag.model_profile_id
        FROM agent ag
        WHERE EXISTS (SELECT 1 FROM agent_model_option o WHERE o.agent_id = ag.id)
          AND NOT EXISTS (SELECT 1 FROM model_profile p WHERE p.owner_agent_id = ag.id)
        ORDER BY ag.workspace_id, ag.created_at, ag.id
    LOOP
        final_name := left(btrim(a.name), 70) || ' · личный';
        IF EXISTS (SELECT 1 FROM model_profile p WHERE p.workspace_id = a.workspace_id AND lower(p.name) = lower(final_name)) THEN
            final_name := left(btrim(a.name), 60) || ' · личный · ' || left(a.id::text, 8);
        END IF;

        based := NULL;
        IF a.model_profile_id IS NOT NULL AND EXISTS (
            SELECT 1 FROM model_profile p
            WHERE p.id = a.model_profile_id AND p.workspace_id = a.workspace_id AND p.owner_agent_id IS NULL
        ) THEN
            based := a.model_profile_id;
        END IF;

        new_id := gen_random_uuid();
        INSERT INTO model_profile (id, workspace_id, name, description, when_exhausted_profile_id, created_by, owner_agent_id, based_on_profile_id)
        VALUES (new_id, a.workspace_id, final_name, '', NULL, NULL, a.id, based);

        INSERT INTO model_profile_option (workspace_id, profile_id, position, runtime_id, model, thinking_level, custom_args)
        SELECT o.workspace_id, new_id, o.position, o.runtime_id, o.model, o.thinking_level, o.custom_args
        FROM agent_model_option o
        WHERE o.agent_id = a.id;

        UPDATE agent SET model_profile_id = new_id WHERE id = a.id;
    END LOOP;
END $$;
