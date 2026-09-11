# testdata: a file written by the single migrator

`old-migrator.runners.toml` was produced by the REAL migrator — the `tk
config migrate --write` of the tk build at the av8 base, run in a scratch
repository whose `.tick/config.md` carried the legacy structured sections
(Testing, Environment, Closeout Evidence Commands, Acceptance Evidence) and
whose `.tick/runners.toml` already carried routing AND the tracker tables
(`[signals]`, `[sweeps]`) under `version = 1`. The migrator merged the command
surface in, preserved every pre-existing table byte-for-byte, and bumped the
file to `version = 2` with the tk-0.32.0 warning.

It is the obligation both halves of the split owe the other, pinned as bytes:

- the execution half (this package) must load it — split_test.go does, and
  the tracker tables in it must be tolerated as foreign, not refused;
- ticks' reader loads its own migrator's output by its own suite (its
  TestMigrate* and load tests in internal/herd/config).

Regenerating it needs no ticks checkout beyond the installed tk: create the
scratch repo as above and run `tk config migrate --write`.
