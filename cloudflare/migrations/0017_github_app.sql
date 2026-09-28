-- The factory's own GitHub App (epic dm6), registered through GitHub's App
-- manifest flow by the Worker itself (src/github-app.ts).
--
-- `github_app` is one row: the App this deployment mints installation tokens
-- with. Its private key and webhook secret are SEALED — AES-GCM under the
-- GITHUB_APP_SEALING_KEY Worker secret — so this table alone, in an export or
-- a dashboard query, holds no usable credential.
CREATE TABLE IF NOT EXISTS github_app (
  singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
  app_id TEXT NOT NULL,
  slug TEXT NOT NULL,
  owner TEXT,
  html_url TEXT,
  client_id TEXT,
  private_key_sealed TEXT NOT NULL,
  webhook_secret_sealed TEXT,
  created_at TEXT NOT NULL
);

-- One row per manifest flow `ticfac factory setup` started. Only the state's
-- SHA-256 is kept: the state itself is the one-time capability in the link
-- the operator opens, and the callback consumes it (pending -> consumed ->
-- created) so a replayed or forged callback finds nothing to complete.
CREATE TABLE IF NOT EXISTS github_app_flows (
  state_hash TEXT PRIMARY KEY,
  org TEXT,
  created_at TEXT NOT NULL,
  expires_at_ms INTEGER NOT NULL,
  status TEXT NOT NULL,
  app_id TEXT
);
