-- Default route of a squad: "dev:Agent,qa:Agent,review:Agent,close:Agent".
-- An issue assigned to a squad with a template runs along a server route
-- instead of waking the leader. Empty means no default route.
ALTER TABLE squad ADD COLUMN IF NOT EXISTS route_template TEXT NOT NULL DEFAULT '';
