import * as hegel from "@hegeldev/hegel";
import * as gs from "@hegeldev/hegel/generators";
import { expect, it } from "vitest";

import {
  declaredMaxParallel,
  declaredSandboxImage,
  IMAGE_PATTERN,
  MAX_CONFIG_BYTES,
  MAX_IMAGE_LENGTH,
  RUNNERS_CONFIG_PATH,
} from "../../src/repo-config";
import { parseToml, TomlParseError } from "../../src/toml";

/**
 * Repo-config parsing (tick p0n): properties over `src/repo-config.ts`'s two
 * readers of `.tick/runners.toml` — `[sandbox].image` and
 * `[orchestration].max_parallel` — the two keys a control plane reads BEFORE
 * any container exists, mirrored from Go's own reader
 * (`internal/herd/config/load.go`) and pinned contract-first in
 * `contracts/runners-config-contract.json` (which `test/repo-config.test.ts`
 * already checks case by case, both sides of the split).
 *
 * The properties here are the FULL-DOMAIN complement to those pinned cases:
 * every image the schema's grammar can produce, built structurally, is
 * accepted; every one-field mutation of a well-formed document is refused in
 * the words the contract states; and the TOML reader itself never answers a
 * hostile document with anything but a `TomlParseError` — never a crash, never
 * a silently-wrong parse. The width rules mirror `internal/herd/config`'s
 * `must be >= 1`, and the byte bound rides the same MAX_CONFIG_BYTES the
 * reader enforces.
 */

// --------------------------------------------------------- the generators ---

/** The image-grammar alphabet, per the pinned pattern. */
const IMAGE_BODY = "ABCXYZabcxyz0123456789._/-";
const IMAGE_TAG = "ABCXYZabcxyz0123456789._-";
const HEX = "0123456789abcdef";

const imageGen = gs.composite((tc) => {
  const body = tc.draw(gs.text({ alphabet: IMAGE_BODY, minSize: 1, maxSize: 40 }));
  const head = tc.draw(gs.text({ alphabet: "ABCXYZabcxyz0123456789", minSize: 1, maxSize: 1 }));
  const tagged = tc.draw(gs.booleans())
    ? `:${tc.draw(gs.text({ alphabet: IMAGE_TAG, minSize: 1, maxSize: 20 }))}`
    : "";
  const digested = tc.draw(gs.booleans())
    ? `@sha256:${Array.from({ length: 64 }, () => HEX[tc.draw(gs.integers({ minValue: 0, maxValue: 15 }))]).join("")}`
    : "";
  return `${head}${body}${tagged}${digested}`;
});

/** A full tracked config declaring this image. */
const _imageDocGen = gs.composite((tc) => {
  const image = tc.draw(imageGen);
  const spacing = tc.draw(gs.sampledFrom(["\n", "\n\n", "\n\n"]));
  const declares = tc.draw(gs.booleans());
  if (!declares) {
    // A repository that tracks the file but declares no image: the common case.
    return `version = 2${spacing}`;
  }
  return `version = 2${spacing}[sandbox]\nimage = "${image}"\n`;
});

const _widthGen = gs.integers({ minValue: 1, maxValue: 2 ** 20 });
// ------------------------------------------------------------ the properties ---

it("every image the schema's grammar can produce, declared in a tracked config, is read back exactly", () => {
  hegel.test(
    (tc) => {
      const image = tc.draw(imageGen);
      if (image.length > MAX_IMAGE_LENGTH) return; // the bound is checked below
      const source = `version = 2\n\n[sandbox]\nimage = "${image}"\n`;
      const declared = declaredSandboxImage(source);
      if (declared !== image) {
        throw new Error(
          `the declared image ${JSON.stringify(image)} was read back as ${JSON.stringify(declared)}`,
        );
      }
    },
    { testCases: 2000 },
  );
});

it("a repository that declares nothing declares nothing conclusively, in every shape of the document", () => {
  hegel.test(
    (tc) => {
      // Documents with no `[sandbox].image` and no
      // `[orchestration].max_parallel` key, in the shapes a tracked config
      // actually arrives in: empty, all comment, other keys, other tables.
      const source = tc.draw(
        gs.oneOf(
          gs.just(""),
          gs.just("# a tracked config that is all comment\n"),
          gs.just('[sandbox]\nsetup = ["echo hi"]\n'),
          gs.just("version = 2\n"),
          gs.just("version = 2\n\n[orchestration]\n# no width declared\n"),
          gs.composite(
            (tc2) =>
              `[${tc2.draw(gs.text({ minSize: 1, maxSize: 12, alphabet: "abcdefghijklmnopqrstuvwxyz" }))}]\n`,
          ),
        ),
      );
      // No [sandbox].image key: nothing declared — a null answer, not a
      // refusal, and never a guessed base image.
      const declared = declaredSandboxImage(source);
      if (declared !== null) {
        throw new Error(`a document with no image declaration read ${JSON.stringify(declared)}`);
      }
      const width = declaredMaxParallel(source);
      if (width !== null) {
        throw new Error(`a document with no width declaration read ${JSON.stringify(width)}`);
      }
    },
    { testCases: 1000 },
  );
});

it("a hostile image is refused, in the words the pinned contract states", () => {
  hegel.test(
    (tc) => {
      const image = tc.draw(imageGen);
      if (image.length > 60) return; // keep the mutation inside the bound
      const uppercaseDigest = image.includes("@sha256:")
        ? `${image.split("@")[0]}@sha256:${"A".repeat(64)}`
        : `${image}@sha512:${"a".repeat(64)}`;
      const [mutation, message] = tc.draw(
        gs.oneOf(
          // The empty declaration has its own pinned message: omit the key,
          // never boot on an image nobody stated.
          gs.just(["", "must not be empty"] as [string, string]),
          gs.just([`-leading-punctuation${image}`, "is not a well-formed image reference"] as [
            string,
            string,
          ]),
          gs.just([`${image} `, "is not a well-formed image reference"] as [string, string]),
          gs.just([`${image}; rm -rf /`, "is not a well-formed image reference"] as [
            string,
            string,
          ]),
          gs.just([`${image}:`, "is not a well-formed image reference"] as [string, string]),
          gs.just([`${image}@sha256:0123abc`, "is not a well-formed image reference"] as [
            string,
            string,
          ]),
          gs.just([uppercaseDigest, "is not a well-formed image reference"] as [string, string]),
          gs.just(["a".repeat(MAX_IMAGE_LENGTH + 1), "past the limit"] as [string, string]),
        ),
      );
      const source = `version = 2\n\n[sandbox]\nimage = "${mutation}"\n`;
      try {
        const declared = declaredSandboxImage(source);
        throw new Error(
          `the hostile image ${JSON.stringify(mutation)} was accepted as ${declared}`,
        );
      } catch (error) {
        if (!(error instanceof Error)) throw error;
        if (error instanceof TomlParseError) throw error; // a TOML-level fault is its own finding
        // The refusal message is pinned by the contract, both sides.
        expect(error.message).toContain(message);
      }
    },
    { testCases: 2000 },
  );
});

it("a declared width is read back exactly, and the schema's bound is the one Go enforces", () => {
  hegel.test(
    (tc) => {
      const width = tc.draw(gs.integers({ minValue: 1, maxValue: 2 ** 20 }));
      const source = `[orchestration]\nmax_parallel = ${width}\n`;
      expect(declaredMaxParallel(source)).toBe(width);
      // One below the schema's `minimum` is refused, with the refusal the
      // pinned contract states verbatim — the same `must be >= 1` Go's
      // validator answers.
      expect(() => declaredMaxParallel("[orchestration]\nmax_parallel = 0\n")).toThrow(
        /must be >= 1/,
      );
      const below = tc.draw(gs.integers({ minValue: -(2 ** 31), maxValue: -1 }));
      expect(() => declaredMaxParallel(`[orchestration]\nmax_parallel = ${below}\n`)).toThrow(
        /must be >= 1/,
      );
    },
    { testCases: 1000 },
  );
});

it("a TOML value that is not an integer, in the width's place, is refused by name", () => {
  hegel.test(
    (tc) => {
      const hostile = tc.draw(
        gs.oneOf(
          gs.just("1.5"),
          gs.composite((tc2) => `"${tc2.draw(gs.integers({ minValue: 1, maxValue: 99 }))}"`),
          gs.just("true"),
          gs.composite((tc2) => `[${tc2.draw(gs.integers({ minValue: 1, maxValue: 3 }))}]`),
        ),
      );
      const source = `[orchestration]\nmax_parallel = ${hostile}\n`;
      try {
        declaredMaxParallel(source);
        throw new Error(`max_parallel = ${hostile} was accepted`);
      } catch (error) {
        if (!(error instanceof Error)) throw error;
        if (error instanceof TomlParseError) throw error;
        expect(error.message).toContain("not an integer");
      }
    },
    { testCases: 1000 },
  );
});

it("the TOML reader answers hostile text with a TomlParseError or a parse — never anything else", () => {
  hegel.test(
    (tc) => {
      const text = tc.draw(
        gs.oneOf(
          gs.text({ minSize: 0, maxSize: 120 }),
          gs.composite((tc2) => `[${tc2.draw(gs.text({ minSize: 0, maxSize: 12 }))}]`),
          gs.composite((tc2) => `key = ${tc2.draw(gs.text({ minSize: 0, maxSize: 12 }))}`),
          gs.composite(
            (tc2) => `${"=".repeat(tc2.draw(gs.integers({ minValue: 1, maxValue: 8 })))}`,
          ),
        ),
      );
      let parsed: unknown;
      try {
        parsed = parseToml(text);
      } catch (error) {
        // Every refusal is a TomlParseError — never a TypeError, never a
        // RangeError, never a crash on hostile shapes.
        if (!(error instanceof TomlParseError)) {
          throw new Error(
            `hostile text ${JSON.stringify(text)} was refused with ${String(error)} (not a TomlParseError)`,
          );
        }
        return;
      }
      // A parse is an object; whatever it holds, the readers stay calm.
      expect(typeof parsed).toBe("object");
      expect(parsed).not.toBeNull();
    },
    { testCases: 2000 },
  );
});

it("the image pattern is exactly the contract's: the pinned acceptance and refusal classes hold over the full domain", () => {
  hegel.test(
    (tc) => {
      const image = tc.draw(imageGen);
      if (image.length <= MAX_IMAGE_LENGTH) {
        expect(IMAGE_PATTERN.test(image)).toBe(true);
      }
      // The bound is the READER's, not the pattern's: the pattern accepts
      // any length (the schema's bound is a separate check), and the
      // declared reader refuses past it in the words the contract states.
      const atBound = "a".repeat(MAX_IMAGE_LENGTH);
      expect(IMAGE_PATTERN.test(atBound)).toBe(true);
      expect(IMAGE_PATTERN.test(`${atBound}a`)).toBe(true);
      try {
        declaredSandboxImage(`[sandbox]\nimage = "${atBound}a"\n`);
        throw new Error("a reference past the bound was accepted");
      } catch (error) {
        if (!(error instanceof Error)) throw error;
        expect(error.message).toContain("past the limit");
      }
      // The config file's own byte bound is the reader's, stated once.
      expect(MAX_CONFIG_BYTES).toBe(256_000);
      expect(RUNNERS_CONFIG_PATH).toBe(".tick/runners.toml");
    },
    { testCases: 500 },
  );
});
