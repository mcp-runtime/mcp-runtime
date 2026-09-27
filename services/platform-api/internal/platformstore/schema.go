package platformstore

const platformSchemaSQL = `
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE SEQUENCE IF NOT EXISTS platform_namespace_seq;
CREATE TABLE IF NOT EXISTS users (
  id uuid primary key,
  email text unique not null,
  role text not null check (role in ('user','admin')),
  created_at timestamptz not null default now(),
  deleted_at timestamptz
);
CREATE TABLE IF NOT EXISTS auth_identities (
  user_id uuid references users(id) on delete cascade,
  provider text not null,
  subject text not null,
  password_hash text,
  created_at timestamptz not null default now(),
  primary key (provider, subject)
);
CREATE TABLE IF NOT EXISTS api_keys (
  id text primary key,
  key_hash text unique not null,
  user_id uuid not null references users(id) on delete cascade,
  name text not null,
  prefix text not null,
  created_at timestamptz not null default now(),
  last_used_at timestamptz,
  revoked boolean not null default false,
  revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_api_keys_user_id ON api_keys(user_id);
CREATE TABLE IF NOT EXISTS registry_credentials (
  id text primary key,
  key_hash text unique not null,
  user_id uuid not null references users(id) on delete cascade,
  name text not null,
  prefix text not null,
  created_at timestamptz not null default now(),
  last_used_at timestamptz,
  revoked boolean not null default false,
  revoked_at timestamptz
);
CREATE INDEX IF NOT EXISTS idx_registry_credentials_user_id ON registry_credentials(user_id);
CREATE TABLE IF NOT EXISTS namespaces (
  id uuid primary key,
  user_id uuid references users(id) on delete cascade,
  team_id uuid,
  namespace text not null,
  display_name text,
  scope text not null default 'user',
  created_at timestamptz not null default now(),
  deleted_at timestamptz
);
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS team_id uuid;
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS display_name text;
ALTER TABLE namespaces ADD COLUMN IF NOT EXISTS scope text NOT NULL DEFAULT 'user';
ALTER TABLE namespaces ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE namespaces
  DROP CONSTRAINT IF EXISTS namespaces_scope_check;
ALTER TABLE namespaces
  ADD CONSTRAINT namespaces_scope_check CHECK (scope IN ('user', 'team'));
CREATE INDEX IF NOT EXISTS idx_namespaces_user_id ON namespaces(user_id);
CREATE INDEX IF NOT EXISTS idx_namespaces_team_id ON namespaces(team_id);
ALTER TABLE IF EXISTS namespaces
  DROP CONSTRAINT IF EXISTS namespaces_namespace_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_namespaces_active ON namespaces(namespace) WHERE deleted_at IS NULL;
CREATE TABLE IF NOT EXISTS teams (
  id uuid primary key,
  slug text not null,
  display_name text not null,
  created_by uuid references users(id),
  created_at timestamptz not null default now(),
  deleted_at timestamptz
);
ALTER TABLE IF EXISTS teams
  DROP CONSTRAINT IF EXISTS teams_slug_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_teams_slug_active ON teams(slug) WHERE deleted_at IS NULL;
ALTER TABLE namespaces
  DROP CONSTRAINT IF EXISTS namespaces_team_id_fkey;
ALTER TABLE namespaces
  ADD CONSTRAINT namespaces_team_id_fkey FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE;
CREATE TABLE IF NOT EXISTS team_memberships (
  id uuid primary key,
  team_id uuid not null references teams(id) on delete cascade,
  user_id uuid not null references users(id) on delete cascade,
  role text not null check (role in ('owner', 'member')),
  created_at timestamptz not null default now(),
  deleted_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_team_memberships_active ON team_memberships(team_id, user_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_team_memberships_user_id ON team_memberships(user_id);
CREATE TABLE IF NOT EXISTS agents (
  id text primary key,
  team_id uuid not null references teams(id),
  name text not null,
  name_normalized text not null,
  status text not null check (status in ('active', 'inactive')),
  created_by uuid references users(id),
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  deactivated_at timestamptz,
  deactivated_by uuid references users(id)
);
ALTER TABLE agents DROP COLUMN IF EXISTS legacy;
CREATE UNIQUE INDEX IF NOT EXISTS uq_agents_team_name ON agents(team_id, name_normalized);
CREATE INDEX IF NOT EXISTS idx_agents_team_status_created ON agents(team_id, status, created_at DESC, id);
CREATE TABLE IF NOT EXISTS refresh_tokens (
  id uuid primary key,
  user_id uuid not null references users(id) on delete cascade,
  token_hash text unique not null,
  expires_at timestamptz not null,
  revoked boolean not null default false,
  user_agent text,
  client_ip inet
);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user_id ON refresh_tokens(user_id);
CREATE TABLE IF NOT EXISTS audit_logs (
  id bigserial primary key,
  user_id uuid references users(id),
  action text not null,
  resource text not null,
  namespace text,
  status text not null,
  message text,
  actor_ip text,
  request_id text,
  source text,
  auth_identity text,
  image_ref text,
  server_name text,
  deployment_target text,
  agent_id text,
  team_id uuid,
  created_at timestamptz not null default now()
);
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'audit_logs'
      AND column_name = 'timestamp'
  ) AND NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema()
      AND table_name = 'audit_logs'
      AND column_name = 'created_at'
  ) THEN
    EXECUTE 'ALTER TABLE audit_logs RENAME COLUMN "timestamp" TO created_at';
  END IF;
END
$$;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS source text;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS auth_identity text;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS image_ref text;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS server_name text;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS deployment_target text;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS agent_id text;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS team_id uuid;
CREATE INDEX IF NOT EXISTS idx_audit_logs_user_id ON audit_logs(user_id);
CREATE INDEX IF NOT EXISTS idx_audit_logs_created_at ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs(action);
CREATE INDEX IF NOT EXISTS idx_audit_logs_image_ref ON audit_logs(image_ref);
CREATE INDEX IF NOT EXISTS idx_audit_logs_agent_id ON audit_logs(agent_id);
`
