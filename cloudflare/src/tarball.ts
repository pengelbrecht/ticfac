/**
 * A streaming tar reader, and the repository snapshot it serves (tick 8xd).
 *
 * WHY THIS EXISTS. The tracker is ~1100 small JSON records. Read one at a time
 * through GitHub's contents API that is 1104 requests per plan pass, and
 * GitHub's secondary limit is 900 POINTS PER MINUTE for REST where most GETs
 * cost a point. So 1104 reads is 1104 points however they are paced: serial it
 * is a fourteen-minute pass that outlives its own three-minute publish slot,
 * concurrent it is 403s. Pacing was never the fix.
 *
 * `GET /repos/{owner}/{repo}/tarball/{ref}` redirects to codeload and only the
 * api.github.com hop is rate limited, so the whole tracker costs ONE point.
 *
 * Measured on pengelbrecht/ticks: 18 MB gzipped, 49 MB and 4332 entries
 * streamed, 1104 records and 2.24 MB retained, ~137 ms of parse CPU against a
 * 30 s limit, ~10 MB of heap against 128 MB.
 *
 * Nothing is materialised. Entries that are not wanted are skipped without
 * being retained, so the isolate's memory ceiling applies to what the caller
 * keeps, never to the archive.
 */

import type { Env } from "./index";
import { GITHUB_API_BASE_URL } from "./progress";

/** Tar's fixed block size; headers and payload padding are both multiples. */
const BLOCK = 512;

/** Where a ustar header keeps the fields this reader needs. */
const NAME_OFFSET = 0;
const NAME_LENGTH = 100;
const SIZE_OFFSET = 124;
const SIZE_LENGTH = 12;
const TYPE_OFFSET = 156;
/** '0' and '\0' are both "regular file"; everything else is a thing we skip. */
const TYPE_FILE = 0x30;
const TYPE_FILE_ALT = 0x00;

const decoder = new TextDecoder();

/** One file the archive held, decoded as UTF-8 text. */
export type TarEntry = {
  /** The path as the archive spells it, INCLUDING the wrapper directory. */
  name: string;
  content: string;
};

function cString(bytes: Uint8Array): string {
  const end = bytes.indexOf(0);
  return decoder.decode(end === -1 ? bytes : bytes.subarray(0, end));
}

/**
 * Tar writes sizes as octal in a fixed field, NUL- or space-terminated.
 *
 * A field this reader cannot make sense of is a zero, which makes the entry
 * empty rather than making the walk guess a length and lose its place in the
 * stream.
 */
function octal(bytes: Uint8Array): number {
  const text = cString(bytes).trim();
  if (text === "") return 0;
  const value = Number.parseInt(text, 8);
  return Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

/**
 * Walks a tar stream, yielding only the entries `wanted` keeps.
 *
 * The buffer holds at most one entry's payload: an unwanted entry is consumed
 * and dropped as it arrives, which is what lets a 49 MB archive pass through
 * an isolate that keeps 2 MB.
 */
export async function* tarEntries(
  stream: ReadableStream<Uint8Array>,
  wanted: (name: string) => boolean,
): AsyncGenerator<TarEntry> {
  const reader = stream.getReader();
  let buffer = new Uint8Array(0);
  let drained = false;

  /** Pulls until the buffer holds `n` bytes, or the stream ends. */
  const fill = async (n: number): Promise<boolean> => {
    while (buffer.length < n && !drained) {
      const { value, done } = await reader.read();
      if (done) {
        drained = true;
        break;
      }
      if (value === undefined || value.length === 0) continue;
      const grown = new Uint8Array(buffer.length + value.length);
      grown.set(buffer, 0);
      grown.set(value, buffer.length);
      buffer = grown;
    }
    return buffer.length >= n;
  };

  /** Consumes `n` bytes without keeping them. */
  const skip = async (n: number): Promise<boolean> => {
    let left = n;
    while (left > 0) {
      if (buffer.length === 0 && !(await fill(1))) return false;
      const take = Math.min(left, buffer.length);
      buffer = buffer.subarray(take);
      left -= take;
    }
    return true;
  };

  try {
    for (;;) {
      if (!(await fill(BLOCK))) return;
      const header = buffer.subarray(0, BLOCK);
      buffer = buffer.subarray(BLOCK);

      // Two zero blocks end an archive; one is enough to know we are done.
      if (header.every((byte) => byte === 0)) return;

      const name = cString(header.subarray(NAME_OFFSET, NAME_OFFSET + NAME_LENGTH));
      const size = octal(header.subarray(SIZE_OFFSET, SIZE_OFFSET + SIZE_LENGTH));
      const type = header[TYPE_OFFSET];
      const padded = Math.ceil(size / BLOCK) * BLOCK;
      const isFile = type === TYPE_FILE || type === TYPE_FILE_ALT;

      if (!isFile || size === 0 || !wanted(name)) {
        if (!(await skip(padded))) return;
        continue;
      }

      if (!(await fill(padded))) return;
      yield { name, content: decoder.decode(buffer.subarray(0, size)) };
      buffer = buffer.subarray(padded);
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}

/**
 * GitHub's archive of one ref, gunzipped and walked.
 *
 * `fetch` follows the 302 to codeload itself, and gzip decoding is native to
 * the runtime, so this is one request and no dependency.
 */
export async function repositoryFiles(
  env: Env,
  project: string,
  ref: string,
  wanted: (path: string) => boolean,
): Promise<Map<string, string>> {
  const base = (env.GITHUB_API_BASE_URL ?? GITHUB_API_BASE_URL).replace(/\/+$/, "");
  const url = `${base}/repos/${project}/tarball/${encodeURIComponent(ref)}`;

  const headers: Record<string, string> = {
    accept: "application/vnd.github+json",
    "user-agent": "ticks-factory",
  };
  const token = env.GITHUB_TOKEN;
  if (typeof token === "string" && token.trim() !== "") {
    headers.authorization = `Bearer ${token.trim()}`;
  }

  const response = await fetch(url, { headers });
  if (!response.ok || response.body === null) {
    // The reason, not just the code: a 403 here is a rate limit and a 404 is a
    // ref that is not there, and telling them apart from the status alone cost
    // a whole diagnosis once (tick 8xd).
    throw new Error(
      `GitHub answered HTTP ${response.status} fetching the ${ref} archive of ${project}` +
        describeLimit(response),
    );
  }

  const files = new Map<string, string>();
  const stream = response.body.pipeThrough(new DecompressionStream("gzip"));
  // Every path in a GitHub archive sits under one generated wrapper directory
  // (`owner-repo-<sha>/`), which callers must not have to know about.
  for await (const entry of tarEntries(stream, (name) => wanted(stripWrapper(name)))) {
    files.set(stripWrapper(entry.name), entry.content);
  }
  return files;
}

/** Drops the archive's generated top-level directory from a path. */
export function stripWrapper(name: string): string {
  const cut = name.indexOf("/");
  return cut === -1 ? name : name.slice(cut + 1);
}

/** What GitHub said about a refusal, when it said anything worth repeating. */
export function describeLimit(response: Response): string {
  const parts: string[] = [];
  const retry = response.headers.get("retry-after");
  if (retry !== null) parts.push(`retry-after ${retry}s`);
  const remaining = response.headers.get("x-ratelimit-remaining");
  if (remaining !== null) parts.push(`${remaining} of the hourly budget left`);
  const reset = response.headers.get("x-ratelimit-reset");
  if (reset !== null) parts.push(`resets at ${reset}`);
  return parts.length === 0 ? "" : ` (${parts.join("; ")})`;
}
