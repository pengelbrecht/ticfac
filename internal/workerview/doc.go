// Package workerview is the live worker view (epic 43y step 8, tick y03):
// one pi-durable worker's conversation — its thinking, its tool calls and
// their live output, its answers, the steers it was sent — folded from the
// commit stream into a model, and that model drawn as a frame a terminal
// redraws in place or as plain lines a pipe reads.
//
// THE STREAM is pi-durable's own agent events (`watchEvents`, spec §9.4):
// a snapshot at attachment, then one batch per commit. Both hosts serve it in
// one frame shape, so this package reads both:
//
//   - a LOCAL worker's steer socket (harness/src/local/steer-socket.ts), one
//     JSON frame per line after a {"type":"watch"} request;
//
//   - a CLOUD worker's WorkerAgent watch socket (cloudflare/src/
//     worker-agent.ts), one JSON frame per WebSocket message, plus the
//     attempt's state and log frames only the cloud host has.
//
//     {"type":"events","events":[…]}   a snapshot first, then one per commit
//     {"type":"end","reason":"…"}       the local watch ended, and why
//     {"type":"state","state":{…}}      the cloud attempt's phase
//     {"type":"log","text":"…"}         the cloud attempt's host log
//
// THE HEARTBEAT (f6o's, from the commit sequence): commits are the heartbeat.
// Every events frame after the snapshot is one commit, so the model counts
// them and stamps the last; with the model-call and tool-call counts and the
// last tool call's age it says what a stuck watcher could only guess — quiet
// only when the worker is truly quiet.
//
// This package decides nothing: it reads, folds and draws. Which worker to
// watch, how to reach it and how to steer it are the CLI's (internal/cli
// worker_watch.go); the events' shape is pi-durable's, pinned by the
// harness package's exact version and by testdata/local-watch.jsonl — a
// stream the harness's own node suite captured from a real local worker
// (TICFAC_CAPTURE_WATCH, harness/test/node/local-host.test.ts).
package workerview
