import { env, SELF } from "cloudflare:test";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { deriveTokenHash, mintFactoryToken } from "../src/auth";

// GET /api/deployment: what this factory actually runs. Deploys mostly come
// from CI now, so the laptop's ~/.ticfacrc no longer knows; the factory does.
describe("deployment route", () => {
  const url = "https://factory.example.com/api/deployment";
  let token: string;

  beforeEach(async () => {
    token = mintFactoryToken();
    env.FACTORY_TOKEN_HASH = await deriveTokenHash(token);
    await env.DB.prepare("DELETE FROM factory_deployment").run();
    await env.DB.prepare("DELETE FROM factory_deployment_image").run();
  });

  afterEach(() => {
    delete env.FACTORY_TOKEN_HASH;
  });

  const get = (init: RequestInit = {}) =>
    SELF.fetch(url, { ...init, headers: { Authorization: `Bearer ${token}`, ...init.headers } });

  it("answers the recorded version, the confirmed image and the serving Worker version", async () => {
    await env.DB.prepare(
      `INSERT INTO factory_deployment (id, tk_version, bundle_sha256, deployed_at)
       VALUES (1, 'v1.2.3-4-g0123456789ab', 'bundlesha', '2026-09-29T12:00:00Z')`,
    ).run();
    await env.DB.prepare(
      `INSERT INTO factory_deployment_image (id, image_ref, image_digest, confirmed_at)
       VALUES (1, 'registry/ticks-orchestrator@sha256:abc', 'sha256:abc', '2026-09-29T12:05:00Z')`,
    ).run();

    const res = await get();
    expect(res.status).toBe(200);
    const body = (await res.json()) as Record<string, unknown>;
    expect(body).toMatchObject({
      version: "v1.2.3-4-g0123456789ab",
      bundle_sha256: "bundlesha",
      deployed_at: "2026-09-29T12:00:00Z",
      image_ref: "registry/ticks-orchestrator@sha256:abc",
      image_digest: "sha256:abc",
    });
    // The Worker version is whatever the runtime binds: a string when
    // [version_metadata] is bound, null when it is not — never absent.
    expect(body).toHaveProperty("worker_version_id");
    expect(body).toHaveProperty("worker_version_timestamp");
    // The harness kinds this deployment's image ships (tick kkt): what a
    // submission may route a container to, the same set image/common.sh's
    // require_common_inputs accepts — never empty, never a guess by the
    // caller about which kinds an older deployment predating the field
    // would have answered.
    expect(body.harness_kinds).toEqual(["omp", "claude", "pi-durable"]);
  });

  it("answers nulls, not an error, for a factory no deploy has recorded", async () => {
    const res = await get();
    expect(res.status).toBe(200);
    await expect(res.json()).resolves.toMatchObject({
      version: null,
      image_digest: null,
    });
  });

  it("is behind the factory token", async () => {
    const res = await SELF.fetch(url);
    expect(res.status).toBe(401);
  });

  it("is read-only", async () => {
    const res = await get({ method: "POST" });
    expect(res.status).toBe(405);
  });
});
