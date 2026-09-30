-- Where a run's orchestrator runs, for the runs whose orchestrator is NOT the
-- factory's own container: `ticfac run <epic> --cloud-workers` drives the Go
-- orchestrator on the operator's machine while the factory boots only its
-- workers (the "local orchestrator, cloud workers" mode).
--
-- A run with no row here is a container-orchestrated run, which is every run
-- before this table existed. A row with kind `local`:
--
--   - is not counted as holding a container slot (container-capacity.ts):
--     its orchestrator occupies none of the account's instances;
--   - is supervised by the Run Workflow without booting anything: the
--     Workflow renews the lease, enforces the budgets and the stop, and ends
--     the run when the orchestrator reports done or stops heartbeating;
--   - is the only kind the operator-authenticated orchestrator-credential
--     route mints a run token for.
--
-- heartbeat_at: the last time the local orchestrator said it was alive
--   (POST /api/heartbeat on its run token), or when its credential was
--   minted. NULL until either happens.
--
-- Keep the migration re-runnable: deploys may be retried after the D1
-- migration has already been applied.
CREATE TABLE IF NOT EXISTS run_orchestrator (
  run_id        TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN ('local')),
  heartbeat_at  TEXT
);
