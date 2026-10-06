CREATE TABLE IF NOT EXISTS registry_pull_credentials (
 id text PRIMARY KEY,
 key_hash text UNIQUE NOT NULL,
 namespace text NOT NULL,
 scope jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL,
 revoked boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_registry_pull_credentials_namespace ON registry_pull_credentials(namespace);
