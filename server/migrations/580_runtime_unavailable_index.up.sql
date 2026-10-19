CREATE INDEX CONCURRENTLY IF NOT EXISTS runtime_unavailable_runtime_idx ON runtime_unavailable(runtime_id, until);
