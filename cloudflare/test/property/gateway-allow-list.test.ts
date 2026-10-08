import * as hegel from "@hegeldev/hegel";
import * as gs from "@hegeldev/hegel/generators";
import { expect, it } from "vitest";

import {
  allowedProviders,
  CREDIT_BILLED_PROVIDERS,
  JEV_ROUTE_SLUG,
  PROVIDER_OPT_IN_VAR,
  PROVIDER_SLUGS,
  type ProviderSlug,
} from "../../src/gateway";

/**
 * The gateway's Workers-AI-only allow-list (tick p0n): properties over
 * `allowedProviders`, the one place a factory decides which inference
 * routes it will spend against.
 *
 * The security argument is billing (src/gateway.ts's own why): Workers AI
 * (and Jev, which Workers AI serves) bills to the operator's Cloudflare
 * account, while the anthropic/openai/openrouter rungs bill a vendor in real
 * cash — and from the hop down, the two are indistinguishable: same request
 * shape, same telemetry, only the invoice differs, and it differs AFTER the
 * money is spent. So the route list fails closed in both directions, and the
 * properties state each direction:
 *
 * - **No var means the credit-billed routes alone**, whatever keys the
 *   factory happens to hold: a credential is not a budget decision.
 * - **The var only ever ADDS**: the credit-billed two are present under every
 *   value of the var, and unknown or malformed entries are dropped, never
 *   guessed into routes (an allow-list that widens on a typo is not an
 *   allow-list).
 * - **Monotone**: adding an entry to a var never removes one that was there.
 * - **Ordered and closed**: the answer is always a subsequence of the
 *   provider table in its own order, never anything outside it — including
 *   inherited names like `__proto__`, which are not routes this gateway
 *   offers.
 */

// --------------------------------------------------------- the generators ---

/** Text an operator or a typo could leave in the var. */
const varGen = gs.oneOf(
  gs.text({ minSize: 0, maxSize: 64 }),
  gs.composite((tc) =>
    tc
      .draw(
        gs.arrays(
          gs.oneOf(
            gs.sampledFrom(PROVIDER_SLUGS),
            // The near-misses a typo produces: casing, punctuation, padding.
            gs.composite((tc2) => tc2.draw(gs.sampledFrom(PROVIDER_SLUGS)).toUpperCase()),
            gs.just("anthropi"),
            gs.just("__proto__"),
            gs.just("constructor"),
            gs.just(""),
          ),
          { minSize: 0, maxSize: 6 },
        ),
      )
      .join(tc.draw(gs.sampledFrom([",", ", ", " ", "  ", ",", ",, ", ",\t"]))),
  ),
);

// ------------------------------------------------------------ the properties ---

it("no opt-in var means the credit-billed routes alone, and nothing else is routable", () => {
  hegel.test(
    (tc) => {
      const env: Record<string, unknown> = {};
      // Whatever keys the factory happens to hold — including every vendor
      // key — none of them buys a route: a credential is not a decision.
      for (const secret of ["ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY"]) {
        env[secret] = `sk-drawn-${tc.draw(gs.text({ maxSize: 12 }))}`;
      }
      const allowed = allowedProviders(env as never);
      expect(allowed).toEqual(CREDIT_BILLED_PROVIDERS);
      for (const slug of allowed) {
        expect(PROVIDER_SLUGS.includes(slug as ProviderSlug)).toBe(true);
      }
    },
    { testCases: 500 },
  );
});

it("every value of the opt-in var keeps the credit-billed routes, and drops what it cannot read", () => {
  hegel.test(
    (tc) => {
      const raw = tc.draw(varGen);
      const env = { [PROVIDER_OPT_IN_VAR]: raw } as never;
      const allowed = allowedProviders(env);

      // The two credit-billed routes are always there, in every shape of the
      // var — an operator who opts into cash cannot accidentally opt out of
      // credit.
      for (const credit of CREDIT_BILLED_PROVIDERS) {
        if (!allowed.includes(credit)) {
          throw new Error(`var ${JSON.stringify(raw)} dropped the credit-billed route ${credit}`);
        }
      }
      // Nothing outside the provider table is ever routable — no inherited
      // names, no typos guessed into routes, no casing games.
      for (const slug of allowed) {
        if (!PROVIDER_SLUGS.includes(slug as ProviderSlug)) {
          throw new Error(
            `var ${JSON.stringify(raw)} routed the non-provider ${JSON.stringify(slug)}`,
          );
        }
      }
      // The answer is ordered like the provider table itself, so no two
      // spellings of the same opt-in produce differently-ordered routes.
      const indices = allowed.map((slug) => PROVIDER_SLUGS.indexOf(slug as ProviderSlug));
      for (let i = 1; i < indices.length; i++) {
        if (indices[i - 1] >= indices[i]) {
          throw new Error(
            `var ${JSON.stringify(raw)} answered out of order: ${JSON.stringify(allowed)}`,
          );
        }
      }
    },
    { testCases: 3000 },
  );
});

it("the allow-list is monotone: an entry added to the var never removes one that was there", () => {
  hegel.test(
    (tc) => {
      const base = tc.draw(varGen);
      const extra = tc.draw(gs.sampledFrom(PROVIDER_SLUGS));
      const fewer = allowedProviders({ [PROVIDER_OPT_IN_VAR]: base } as never);
      const more = allowedProviders({
        [PROVIDER_OPT_IN_VAR]: `${base} ${extra}`,
      } as never);
      for (const slug of fewer) {
        if (!more.includes(slug)) {
          throw new Error(`adding ${extra} to ${JSON.stringify(base)} removed ${slug}`);
        }
      }
    },
    { testCases: 1500 },
  );
});

it("an opt-in naming a vendor widens exactly that vendor, and the answer says so", () => {
  hegel.test(
    (tc) => {
      const vendors = ["anthropic", "openai", "openrouter"] as const;
      const chosen = tc.draw(gs.sampledFrom(vendors));
      const allowed = allowedProviders({
        [PROVIDER_OPT_IN_VAR]: ` ${chosen} `,
      } as never);
      expect(allowed).toContain(chosen);
      for (const other of vendors) {
        if (other === chosen) continue;
        expect(allowed).not.toContain(other);
      }
      // And the answer stays in the provider table's own order — the
      // credit-billed floor is membership, not position (an opt-in is a
      // widening, never a reordering).
      expect(allowed).toEqual([
        ...PROVIDER_SLUGS.filter(
          (slug) =>
            slug === chosen || (CREDIT_BILLED_PROVIDERS as readonly string[]).includes(slug),
        ),
      ]);
    },
    { testCases: 500 },
  );
});

it("the vocabulary is closed: the credit-billed floor is Workers AI and Jev, in that order", () => {
  expect(CREDIT_BILLED_PROVIDERS).toContain("workers-ai");
  expect(CREDIT_BILLED_PROVIDERS).toContain(JEV_ROUTE_SLUG);
  expect(PROVIDER_SLUGS).toEqual(["anthropic", "openai", "openrouter", "workers-ai", "jev"]);
});
