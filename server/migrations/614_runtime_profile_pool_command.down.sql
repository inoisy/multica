-- Violations that stay behind NOT VALID, to read before VALIDATE:
--   SELECT id, display_name, command_name, runtime_type FROM runtime_profile
--   WHERE pool_id IS NOT NULL AND command_name <> runtime_type;
ALTER TABLE runtime_profile DROP CONSTRAINT IF EXISTS runtime_profile_pool_command_check;
