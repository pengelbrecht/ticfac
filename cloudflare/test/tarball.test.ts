import { env } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";

import { githubContentsStore } from "../src/git-contents";
import { repositoryFiles, stripWrapper, tarEntries } from "../src/tarball";
import { TrackerClient } from "../src/tracker-client";

/**
 * The tracker is read from ONE repository archive (tick 8xd), because reading
 * ~1100 records file by file costs ~1100 points against GitHub's
 * 900-points-per-minute secondary limit and cannot be paced out of it.
 *
 * These build real tar bytes rather than stubbing the reader, so the format
 * handling — octal sizes, 512-byte padding, the wrapper directory, entries
 * skipped without being retained — is what is under test.
 */

const BLOCK = 512;
const encoder = new TextEncoder();

/** A ustar header for one regular file, with the checksum GitHub's tar writes. */
function header(name: string, size: number, type = "0"): Uint8Array {
  const block = new Uint8Array(BLOCK);
  block.set(encoder.encode(name).subarray(0, 100), 0);
  block.set(encoder.encode("0000644\0"), 100); // mode
  block.set(encoder.encode("0000000\0"), 108); // uid
  block.set(encoder.encode("0000000\0"), 116); // gid
  block.set(encoder.encode(`${size.toString(8).padStart(11, "0")}\0`), 124);
  block.set(encoder.encode("00000000000\0"), 136); // mtime
  block.set(encoder.encode(type), 156);
  block.set(encoder.encode("ustar\x0000"), 257);
  // Checksum is computed with the field itself read as spaces.
  block.set(encoder.encode("        "), 148);
  let sum = 0;
  for (const byte of block) sum += byte;
  block.set(encoder.encode(`${sum.toString(8).padStart(6, "0")}\0 `), 148);
  return block;
}

function tar(files: Array<{ name: string; body: string; type?: string }>): Uint8Array {
  const parts: Uint8Array[] = [];
  for (const file of files) {
    const bytes = encoder.encode(file.body);
    parts.push(header(file.name, bytes.length, file.type));
    const padded = Math.ceil(bytes.length / BLOCK) * BLOCK;
    const payload = new Uint8Array(padded);
    payload.set(bytes, 0);
    parts.push(payload);
  }
  parts.push(new Uint8Array(BLOCK * 2)); // the two zero blocks that end it
  const total = parts.reduce((n, p) => n + p.length, 0);
  const out = new Uint8Array(total);
  let at = 0;
  for (const part of parts) {
    out.set(part, at);
    at += part.length;
  }
  return out;
}

function streamOf(bytes: Uint8Array, chunk = 64): ReadableStream<Uint8Array> {
  let at = 0;
  return new ReadableStream({
    pull(controller) {
      if (at >= bytes.length) {
        controller.close();
        return;
      }
      controller.enqueue(bytes.subarray(at, Math.min(at + chunk, bytes.length)));
      at += chunk;
    },
  });
}

async function gzip(bytes: Uint8Array): Promise<Uint8Array> {
  const compressed = streamOf(bytes).pipeThrough(new CompressionStream("gzip"));
  const chunks: Uint8Array[] = [];
  const reader = compressed.getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    if (value !== undefined) chunks.push(value);
  }
  const total = chunks.reduce((n, c) => n + c.length, 0);
  const out = new Uint8Array(total);
  let at = 0;
  for (const c of chunks) {
    out.set(c, at);
    at += c.length;
  }
  return out;
}

function tick(id: string): string {
  return JSON.stringify({
    id,
    title: `tick ${id}`,
    status: "open",
    priority: 2,
    type: "task",
    owner: "ticfac",
    created_by: "test",
    created_at: "2026-09-20T00:00:00Z",
    updated_at: "2026-09-20T00:00:00Z",
  });
}

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
});

/** Stubs fetch with one archive, counting how many requests were made. */
function archiveHost(files: Array<{ name: string; body: string }>): { calls: () => number } {
  const original = globalThis.fetch;
  let calls = 0;
  globalThis.fetch = (async () => {
    calls += 1;
    const bytes = await gzip(tar(files));
    return new Response(bytes, { status: 200 });
  }) as typeof globalThis.fetch;
  restore = () => void (globalThis.fetch = original);
  return { calls: () => calls };
}

describe("the tar reader", () => {
  it("keeps the entries asked for and skips the rest", async () => {
    const bytes = tar([
      { name: "wrap/README.md", body: "x".repeat(900) },
      { name: "wrap/.tick/issues/a1.json", body: tick("a1") },
      { name: "wrap/internal/huge.bin", body: "y".repeat(5000) },
      { name: "wrap/.tick/issues/b2.json", body: tick("b2") },
    ]);

    const kept: string[] = [];
    for await (const entry of tarEntries(streamOf(bytes), (n) => n.includes("/.tick/issues/"))) {
      kept.push(stripWrapper(entry.name));
      expect(JSON.parse(entry.content).id).toBeTruthy();
    }

    expect(kept).toEqual([".tick/issues/a1.json", ".tick/issues/b2.json"]);
  });

  it("reads a payload split across many stream chunks", async () => {
    const body = tick("c3").repeat(20);
    const bytes = tar([{ name: `wrap/.tick/issues/c3.json`, body }]);

    // One byte at a time: the reader must reassemble, not assume a chunk
    // boundary lands on a block boundary.
    const seen: string[] = [];
    for await (const entry of tarEntries(streamOf(bytes, 1), () => true)) {
      seen.push(entry.content);
    }

    expect(seen).toEqual([body]);
  });

  it("skips an unwanted payload rather than reading into it", async () => {
    // A tar walk that forgets to skip a payload usually gets away with it:
    // headers are block-aligned and payloads are padded, so it consumes 512
    // bytes at a time and RESYNCHRONISES on the next real header. This is the
    // fixture where it cannot — the unwanted file's own bytes are a valid
    // header for a record that is not in the archive.
    const smuggled = tar([{ name: "wrap/.tick/issues/ghost.json", body: tick("ghost") }]);
    const payload = new TextDecoder("latin1").decode(smuggled);
    const bytes = tar([
      { name: "wrap/vendor/fixture.tar", body: payload },
      { name: "wrap/.tick/issues/real.json", body: tick("real") },
    ]);

    const ids: string[] = [];
    for await (const entry of tarEntries(streamOf(bytes), (n) => n.includes("/.tick/issues/"))) {
      ids.push(JSON.parse(entry.content).id);
    }

    expect(ids).toEqual(["real"]);
    expect(ids).not.toContain("ghost");
  });

  it("does not mistake a directory entry for a file", async () => {
    const bytes = tar([
      { name: "wrap/.tick/issues/", body: "", type: "5" },
      { name: "wrap/.tick/issues/d4.json", body: tick("d4") },
    ]);

    const names: string[] = [];
    for await (const entry of tarEntries(streamOf(bytes), () => true)) names.push(entry.name);

    expect(names).toEqual(["wrap/.tick/issues/d4.json"]);
  });

  it("stops at the archive's end rather than reading past it", async () => {
    const bytes = tar([{ name: "wrap/.tick/issues/e5.json", body: tick("e5") }]);
    const trailing = new Uint8Array(bytes.length + 4096);
    trailing.set(bytes, 0); // zeros after the terminator

    let count = 0;
    for await (const _ of tarEntries(streamOf(trailing), () => true)) count += 1;

    expect(count).toBe(1);
  });
});

describe("the tracker read", () => {
  it("reads every record in ONE request", async () => {
    const files = Array.from({ length: 200 }, (_, i) => {
      const id = `t${String(i).padStart(3, "0")}`;
      return { name: `wrap/.tick/issues/${id}.json`, body: tick(id) };
    });
    const host = archiveHost(files);

    const store = githubContentsStore(env as never, "owner/repo", "epic/x");
    const bulk = await store.readAll?.(".tick/issues");

    expect(bulk?.size).toBe(200);
    expect(bulk?.get(".tick/issues/t000.json")).toContain('"id":"t000"');
    expect(host.calls()).toBe(1);
  });

  it("serves the tracker client's whole listing from that one request", async () => {
    const files = Array.from({ length: 50 }, (_, i) => {
      const id = `u${String(i).padStart(3, "0")}`;
      return { name: `wrap/.tick/issues/${id}.json`, body: tick(id) };
    });
    const host = archiveHost(files);

    const store = githubContentsStore(env as never, "owner/repo", "epic/x");
    // list() would be a second request; the client must not need it to be
    // cheap, but the READS must all come from the archive.
    store.list = async () => files.map((f) => stripWrapper(f.name));
    const client = new TrackerClient(store, "owner/repo", "epic/x");

    const listed = await client.list();

    expect(listed.ticks).toHaveLength(50);
    expect(host.calls()).toBe(1);
  });

  it("names the ref and the repository when GitHub refuses the archive", async () => {
    const original = globalThis.fetch;
    globalThis.fetch = (async () =>
      new Response("", {
        status: 403,
        headers: { "retry-after": "60", "x-ratelimit-remaining": "0" },
      })) as typeof globalThis.fetch;
    restore = () => void (globalThis.fetch = original);

    await expect(repositoryFiles(env as never, "owner/repo", "epic/x", () => true)).rejects.toThrow(
      /HTTP 403 fetching the epic\/x archive of owner\/repo.*retry-after 60s/s,
    );
  });
});
