/**
 * The staging Worker's entry (epic umq, tick nmd): FactorySandbox alone,
 * behind a token, so a deploy can be rehearsed under a LIVE container without
 * the production factory (staging/wrangler.toml).
 *
 * The question it exists to answer is umq [A2]: does a `wrangler deploy` that
 * changes the image leave a container that is already running on the image
 * it started on, with its processes alive? Every route is one method of the
 * class, addressed by a sandbox name, so the rehearsal drives the same code a
 * run does.
 */

import { FactorySandbox, type FactorySandboxNamespace } from "./factory-sandbox";
import type { Env } from "./index";

type StagingEnv = Env & { STAGING_TOKEN?: string; SANDBOXES_V1?: FactorySandboxNamespace };

/** Constant-time enough for a staging token: lengths first, then every byte. */
function sameToken(a: string, b: string): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  return diff === 0;
}

export async function stagingFetch(request: Request, env: StagingEnv): Promise<Response> {
  const token = env.STAGING_TOKEN ?? "";
  const given = (request.headers.get("authorization") ?? "").replace(/^Bearer\s+/i, "");
  // No token configured is a closed door, never an open one.
  if (token === "" || !sameToken(given, token))
    return new Response("unauthorized", { status: 401 });
  const namespace = env.SANDBOXES_V1;
  if (namespace === undefined) return new Response("no SANDBOXES_V1", { status: 503 });

  const url = new URL(request.url);
  // /s/<name>/<verb>[/<id>]
  const match = /^\/s\/([a-z0-9-]{1,63})\/([a-z]+)(?:\/([A-Za-z0-9-]{1,64}))?$/.exec(url.pathname);
  if (match === null) return new Response("not found", { status: 404 });
  const [, name, verb, id] = match as unknown as [string, string, string, string | undefined];
  const stub = namespace.get(namespace.idFromName(name));

  switch (verb) {
    case "start": {
      const command = url.searchParams.get("cmd") ?? "";
      if (command === "" || request.method !== "POST")
        return new Response("POST ?cmd=", { status: 400 });
      const keepAlive = url.searchParams.get("keepalive") === "1";
      return Response.json(await stub.startProcess(command, {}, { keepAlive }));
    }
    case "proc":
      return id === undefined
        ? new Response("id", { status: 400 })
        : Response.json(await stub.getProcess(id));
    case "list":
      return Response.json(await stub.listProcesses());
    case "read":
      return id === undefined
        ? new Response("id", { status: 400 })
        : Response.json(await stub.readOutput(id, Number(url.searchParams.get("offset") ?? "0")));
    case "kill":
      if (id === undefined) return new Response("id", { status: 400 });
      await stub.killProcess(id);
      return Response.json({ killed: id });
    case "destroy":
      await stub.destroy();
      return Response.json({ destroyed: name });
    case "image":
      // The proof: the image the running container started on, beside the
      // image this deployment would start a new one on.
      return Response.json({
        running: await stub.isRunning(),
        running_image: await stub.runningImage(),
        deployed_image: await stub.imageRef(),
      });
    default:
      return new Response("not found", { status: 404 });
  }
}

export default {
  fetch: (request: Request, env: StagingEnv) => stagingFetch(request, env),
};

export { FactorySandbox };
