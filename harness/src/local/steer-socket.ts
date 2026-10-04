/**
 * The steer socket (epic 43y step 7, tick hpk): the one door the Go
 * supervisor has into a RUNNING local worker's conversation.
 *
 * pi-durable's storage is single-owner — "one process owns a storage at a
 * time; there is no cross-process locking" — so a steer from outside must go
 * THROUGH the owning process, and that is what this socket is: a Unix domain
 * stream socket, one per attempt beside its storage, that turns each request
 * line into a durable `submit({ whenBusy: "steer" })` on the conversation.
 * The spike's experiment 5 is the shape: input placed after the current tool
 * round, in the same conversation, with no relaunch.
 *
 * THE PROTOCOL (one line each way, UTF-8 JSON, and it is pinned by all three
 * halves: this server, the node suite's client, and the Go supervisor's
 * client in internal/exec/subprocess/steer.go):
 *
 *     request:  {"requestId":"stuck-nudge-1","text":"You appear stuck: …"}\n
 *     reply:    {"ok":true,"requestId":"stuck-nudge-1"}\n
 *     failure:  {"ok":false,"requestId":"stuck-nudge-1","error":"…"}\n
 *
 * `requestId` is optional and travels to pi-durable's own idempotent
 * submit: a client that retries an unacknowledged steer with the same id
 * cannot double-submit it. `text` is required. The reply is written only
 * after `submit()` has resolved — after the steer is durably admitted, not
 * merely received — so an acknowledged steer survives the harness dying.
 * One request per connection keeps the halves simple; a client that wants
 * two steers opens two connections.
 */

import { existsSync, rmSync } from "node:fs";
import { createConnection, createServer, type Socket } from "node:net";

/** What one request line must carry. */
export type SteerRequest = {
  readonly requestId?: string;
  readonly text: string;
};

/** What one reply line says. */
export type SteerReply = {
  readonly ok: boolean;
  readonly requestId?: string;
  readonly error?: string;
};

/** One request line, more than which a socket never buffers. */
const MAX_LINE = 1024 * 1024;

/** The malformed-request reply, so a client that sends garbage learns why. */
function refused(request: Partial<SteerRequest>, error: string): SteerReply {
  return { ok: false, requestId: request.requestId, error };
}

export type SteerServer = {
  /** The path the socket listens on (the config's `steerSock`). */
  readonly path: string;
  /** Stop listening and remove the socket file. Idempotent. */
  readonly close: () => Promise<void>;
};

/**
 * The harness's half: listen on `path`, and turn every request line into a
 * durable steer through `submit`. `submit` resolves once the input is
 * durably admitted (pi-durable's `submit`); its rejections become the
 * `ok:false` reply's error text.
 */
export async function openSteerServer(
  path: string,
  submit: (request: SteerRequest) => Promise<void>,
): Promise<SteerServer> {
  if (existsSync(path)) {
    // A previous harness that died between listen and unlink leaves the
    // file behind, and listen would fail on it forever.
    rmSync(path);
  }
  let sockets = new Set<Socket>();
  const server = createServer((socket) => {
    sockets.add(socket);
    socket.on("close", () => {
      sockets.delete(socket);
    });
    let buffered = "";
    socket.on("data", (chunk: Buffer) => {
      buffered += chunk.toString("utf8");
      const at = buffered.indexOf("\n");
      if (at === -1) {
        if (buffered.length > MAX_LINE) {
          socket.end(`${JSON.stringify(refused({}, "the steer line is too long"))}\n`);
          socket.destroy();
        }
        return;
      }
      const line = buffered.slice(0, at);
      buffered = buffered.slice(at + 1);
      let request: Partial<SteerRequest> = {};
      try {
        request = JSON.parse(line) as Partial<SteerRequest>;
      } catch (error) {
        socket.end(
          `${JSON.stringify(refused({}, `the request line is not JSON: ${String(error)}`))}\n`,
        );
        return;
      }
      if (typeof request.text !== "string" || request.text.trim() === "") {
        socket.end(`${JSON.stringify(refused(request, "the steer carries no text"))}\n`);
        return;
      }
      const admitted: SteerRequest = {
        text: request.text,
        ...(request.requestId === undefined ? {} : { requestId: request.requestId }),
      };
      void submit(admitted)
        .then(() => {
          socket.end(
            `${JSON.stringify({ ok: true, ...(admitted.requestId === undefined ? {} : { requestId: admitted.requestId }) })}\n`,
          );
        })
        .catch((error: unknown) => {
          socket.end(
            `${JSON.stringify(refused(admitted, error instanceof Error ? error.message : String(error)))}\n`,
          );
        });
    });
  });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(path, () => resolve());
  });
  return {
    path,
    close: () =>
      new Promise<void>((resolve) => {
        // Connections this server accepted but a client never closed would
        // keep the close callback waiting; both halves end their side after
        // one exchange, and a client that walked away is not a reason to
        // hang the harness's exit.
        for (const socket of sockets) socket.destroy();
        sockets = new Set();
        server.close(() => {
          rmSync(path, { force: true });
          resolve();
        });
      }),
  };
}

/**
 * The client's half — the reference implementation of the protocol above,
 * the one the node suite's steer tests drive and the shape the Go
 * supervisor's client (internal/exec/subprocess/steer.go) mirrors. Returns
 * the server's reply, or throws when the socket cannot be reached in time:
 * the caller (a supervisor about to steer a stuck worker) needs a refusal it
 * can fall back from, not a hang.
 */
export async function steerOnce(
  path: string,
  text: string,
  requestId?: string,
  timeoutMs = 5000,
): Promise<SteerReply> {
  return new Promise<SteerReply>((resolve, reject) => {
    const socket = createConnection(path);
    const giveUp = () => {
      socket.destroy();
      reject(new Error(`the steer socket at ${path} did not answer in ${timeoutMs}ms`));
    };
    const timer = setTimeout(giveUp, timeoutMs);
    let buffered = "";
    socket.on("connect", () => {
      socket.write(
        `${JSON.stringify({ text, ...(requestId === undefined ? {} : { requestId }) })}\n`,
      );
    });
    socket.on("data", (chunk: Buffer) => {
      buffered += chunk.toString("utf8");
      const at = buffered.indexOf("\n");
      if (at === -1) return;
      clearTimeout(timer);
      socket.end();
      try {
        resolve(JSON.parse(buffered.slice(0, at)) as SteerReply);
      } catch (error) {
        reject(new Error(`the steer reply is not JSON: ${String(error)}`));
      }
    });
    socket.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });
}
