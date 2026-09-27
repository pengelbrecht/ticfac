-- The phone page's LOCAL half: one row per local run that pushes snapshots
-- (ticfac tick i1r).
--
-- The factory stores and serves the 6dh status model (`ticfac.status.v1`) for
-- EVERY run. A cloud run is reported NATIVELY: the factory already holds the
-- run's index row, its progress verdict and its room's pending gates, so it
-- composes the model at read time and stores nothing new. A local run is NOT
-- on this machine's disk: nothing here can read its records, its tracker or
-- its feed — so the run itself pushes small snapshots while it works, opted in
-- by a ticfac config flag (`factory_status_push` in ~/.ticfacrc), and this
-- table is the last snapshot each run pushed.
--
-- One row per run, not an append log, deliberately: the page answers "where is
-- the run now", not "where has it been" — the run's own event feed is the
-- history, and this is the pocket-sized answer a phone reads. The pusher
-- sends on a short cadence (30s) and once more at each ending, so the row is
-- at most one cadence old while the laptop is awake.
--
-- THE PAUSED/STALE CASE is the reason `pushed_at` is a column the page reads
-- rather than a write the page assumes. A local run pauses when the laptop
-- sleeps, so its snapshots stop; a page that rendered the row as live would
-- look stuck when the truth is "paused, and this reading is old". The page
-- measures the snapshot's age and says PAUSED/STALE plainly once it exceeds
-- the cadence by several misses.
--
-- Snapshots carry no secrets and no work product, only the model (and the
-- label map the page names ticks by) — the same rule the pusher is built to.
-- Re-runnable, like every migration here.
CREATE TABLE IF NOT EXISTS status_snapshots (
  run_id  TEXT PRIMARY KEY,
  -- Always "local" today: cloud runs never need a pushed row.
  host     TEXT NOT NULL,
  epic_id  TEXT NOT NULL,
  pushed_at TEXT NOT NULL,
  -- The full `ticfac.status.v1` document as pushed, stored as it parsed so
  -- what the page serves is exactly what the run said.
  model    TEXT NOT NULL,
  -- Tick id -> label ("gloss else title cut to 40"), the naming the phone
  -- page and Telegram alerts read (tick q90). NULL when the run pushed none.
  tick_labels TEXT
);

-- The listing's order: newest push first, so attention is a sort the page
-- does on the classified rows and the query stays a plain read.
CREATE INDEX IF NOT EXISTS idx_status_snapshots_pushed
  ON status_snapshots (pushed_at);
