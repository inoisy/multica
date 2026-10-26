-- Gives the agents their original fields and profile pointers, removes the
-- personal profiles the up migration made and restores the cleared account
-- groups, all from model_truth_backup. Edits made after the move to rows or
-- fields of these agents are overwritten.
DO $$
BEGIN
    IF to_regclass('model_truth_backup') IS NULL THEN
        RETURN;
    END IF;

    UPDATE agent ag SET
        runtime_id = nullif(b.data ->> 'runtime_id', '')::uuid,
        runtime_mode = b.data ->> 'runtime_mode',
        model = b.data ->> 'model',
        thinking_level = b.data ->> 'thinking_level',
        model_profile_id = nullif(b.data ->> 'model_profile_id', '')::uuid,
        updated_at = now()
    FROM model_truth_backup b
    WHERE b.kind = 'agent' AND b.id = ag.id;

    DELETE FROM model_profile_option
    WHERE profile_id IN (SELECT b.id FROM model_truth_backup b WHERE b.kind = 'profile');
    DELETE FROM model_profile
    WHERE id IN (SELECT b.id FROM model_truth_backup b WHERE b.kind = 'profile');

    UPDATE subscription s SET account_group = b.data ->> 'account_group', updated_at = now()
    FROM model_truth_backup b
    WHERE b.kind = 'account_group' AND b.id = s.id AND s.account_group = '';
END $$;

DROP TABLE IF EXISTS model_truth_backup;
