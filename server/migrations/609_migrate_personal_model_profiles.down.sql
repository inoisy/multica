-- Back to own lists. The rows of a personal profile become the agent's
-- agent_model_option rows (the old ones are replaced), so an edit made after
-- the move is not lost, and the agent points at the shared profile it was based
-- on. Agents returned to a shared profile get no own rows. Personal profiles,
-- their rows and their when_exhausted profile go.
DO $$
BEGIN
    DELETE FROM agent_model_option;

    INSERT INTO agent_model_option (workspace_id, agent_id, position, runtime_id, model, thinking_level, custom_args)
    SELECT po.workspace_id, p.owner_agent_id, po.position, po.runtime_id, po.model, po.thinking_level, po.custom_args
    FROM model_profile p
    JOIN agent a ON a.id = p.owner_agent_id AND a.model_profile_id = p.id
    JOIN model_profile_option po ON po.profile_id = p.id
    WHERE p.owner_agent_id IS NOT NULL;

    UPDATE agent a
    SET model_profile_id = p.based_on_profile_id
    FROM model_profile p
    WHERE p.owner_agent_id = a.id AND a.model_profile_id = p.id;

    UPDATE issue SET model_profile_id = NULL
    WHERE model_profile_id IN (SELECT id FROM model_profile WHERE owner_agent_id IS NOT NULL);

    DELETE FROM model_profile_option
    WHERE profile_id IN (SELECT id FROM model_profile WHERE owner_agent_id IS NOT NULL);
    DELETE FROM model_profile WHERE owner_agent_id IS NOT NULL;
END $$;
