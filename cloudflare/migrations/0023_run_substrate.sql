-- Which container substrate a run's containers run on (epic umq, tick 1hq).
--
-- A run with no row here is on the Sandbox SDK 0.x `Sandbox` class (the
-- SANDBOXES binding, the `default`-policy application) — which is every run
-- submitted before this table existed, and every run submitted without
-- asking. A row with substrate `do_v1` routes the run's containers through
-- FactorySandbox (SANDBOXES_V1, the `durable_object`-policy application).
--
-- Written once, at submit, before the Run Workflow exists, and never changed:
-- a run never straddles the two namespaces, because the same container name
-- addresses a DIFFERENT Durable Object in each, and a run whose later boots
-- looked in the other one would find none of its live containers.
--
-- image: the image reference the run's containers start on (tick v1d), read
--   from the deployment's own `ctx.container.images` at submit. NULL means
--   the deployment's current image at each start.
--
-- Keep the migration re-runnable: deploys may be retried after the D1
-- migration has already been applied.
CREATE TABLE IF NOT EXISTS run_substrate (
  run_id     TEXT PRIMARY KEY,
  substrate  TEXT NOT NULL CHECK (substrate IN ('do_v1')),
  image      TEXT
);
