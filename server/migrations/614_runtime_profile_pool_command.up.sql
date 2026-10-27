-- An environment of an account pool runs the standard CLI of its type: a
-- wrapper command signs in on its own and spends an account the pool does not
-- meter. The server refuses such a write by itself; this is the same rule in
-- the schema.
--
-- runtime_type is empty on profiles written before it existed, and the type
-- is then read from protocol_family (agent.ProfileRuntimeType). Pool
-- environments get that value written out so the check compares what the
-- server compares.
--
-- NOT VALID: rows that already break the rule stay as they are and are listed
-- by the query in the down migration's comment; every new or changed row is
-- checked. VALIDATE is a separate step once the report is clean.
UPDATE runtime_profile SET runtime_type = protocol_family
WHERE pool_id IS NOT NULL AND runtime_type = '';

ALTER TABLE runtime_profile
    ADD CONSTRAINT runtime_profile_pool_command_check
    CHECK (pool_id IS NULL OR command_name = runtime_type) NOT VALID;
