/**
 * The pi CLI's own credential store, read by the local worker host (tick hpk)
 * — because the operator's local rung DOES NOT authenticate the way the cloud
 * rung does: there is no factory gateway and no run token locally, and the
 * Workers AI credential the pi CLI runs GLM on lives in its own auth store
 * (`~/.pi/agent/auth.json`), not in the ambient environment. A local worker
 * that resolved auth from the environment alone would refuse its first model
 * call on a host whose credentials are stored, so the host reads THE SAME
 * FILE the pi CLI does, in pi-ai's own canonical shape ("one type-tagged
 * credential per provider — the shape of today's auth.json", pi-ai's
 * auth/types).
 *
 * Semantics, kept pi's own:
 *
 * - **Stored wins, ambient env falls back.** pi-ai's per-field merge does
 *   exactly this (`resolveCloudflareEnv`: prefer the credential value, fall
 *   back to ambient env for the account id), so the local host inherits the
 *   behaviour simply by handing the store to `createModels`.
 * - **Reads re-read the file.** A rotated credential (the store's write path
 *   is pi's own `pi login`; a refreshed OAuth token lands in the same file)
 *   is picked up by the next request, not by the next process.
 * - **Writes go back to the same file**, atomically, so an OAuth refresh pi-ai
 *   runs through `modify` cannot corrupt the store the CLI also reads. The
 *   local rung's Workers AI credential is an `api_key` credential, which
 *   never refreshes — but the store is provider-neutral, so a later rung
 *   that routes an OAuth-backed provider through the same host does not
 *   silently log itself out.
 * - **A missing file is an empty store**, not an error: the host then
 *   resolves auth from the ambient environment, which is also pi's own
 *   answer, and the refusal that follows names the provider.
 */

import { readFileSync, renameSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { Credential, CredentialInfo, CredentialStore } from "@earendil-works/pi-ai";

/** pi's own config-dir convention (pi-coding-agent's config.js: `.pi`). */
const PI_AUTH_FILE = ["agent", "auth.json"] as const;

/** The auth.json path: the operator's override first, then pi's own place. */
export function piAuthFile(override?: string): string {
  if (override !== undefined && override !== "") return override;
  const fromEnv = process.env.TICFAC_PI_AUTH_FILE;
  if (fromEnv !== undefined && fromEnv !== "") return fromEnv;
  return join(homedir(), ".pi", ...PI_AUTH_FILE);
}

/** One provider's entry, as auth.json stores it. */
type AuthFile = Record<string, Credential>;

function readStore(file: string): AuthFile {
  try {
    const raw = JSON.parse(readFileSync(file, "utf8")) as AuthFile;
    return typeof raw === "object" && raw !== null ? raw : {};
  } catch {
    // A file that cannot be read — missing, or not the shape auth.json is —
    // is an empty store: pi-ai's answer is then the ambient environment.
    return {};
  }
}

function writeStore(file: string, store: AuthFile): void {
  const tmp = `${file}.ticfac-tmp`;
  writeFileSync(tmp, JSON.stringify(store, undefined, 2));
  renameSync(tmp, file);
}

/**
 * The file-backed credential store over the pi CLI's auth.json. One store
 * per process is fine; reads always re-read the file, writes serialize
 * through a promise chain per provider (a single-process worker makes at
 * most one refresh race worth preventing, and preventing it costs one Map).
 */
export function piAuthStore(file?: string): CredentialStore {
  const path = piAuthFile(file);
  const writes = new Map<string, Promise<unknown>>();
  const serialize = <T>(providerId: string, run: () => Promise<T>): Promise<T> => {
    const prior = writes.get(providerId) ?? Promise.resolve();
    const next = prior.then(run, run);
    writes.set(providerId, next);
    void next.catch(() => {});
    return next;
  };
  return {
    read: async (providerId) => readStore(path)[providerId],
    list: async () =>
      Object.entries(readStore(path)).map(([providerId, credential]) => ({
        providerId,
        type: credential.type,
      })) as readonly CredentialInfo[],
    modify: (providerId, fn) =>
      serialize(providerId, async () => {
        const store = readStore(path);
        const updated = await fn(store[providerId]);
        if (updated !== undefined) {
          store[providerId] = updated;
          writeStore(path, store);
        }
        return updated;
      }),
    delete: (providerId) =>
      serialize(providerId, async () => {
        const store = readStore(path);
        delete store[providerId];
        writeStore(path, store);
      }),
  };
}
