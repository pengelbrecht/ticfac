import type { StorageConformanceAssertions } from "@earendil-works/pi-durable/testing";

/**
 * The conformance suite's assertions, implemented without a test runner.
 *
 * pi-durable's `createStorageConformance` is runner-independent: it takes an
 * assertions object, and the suite normally gets one from `createExpectAssertions`
 * over a Vitest/Jest `expect`. The cases for the DO SQLite adapter run INSIDE a
 * Durable Object — exactly where the storage they test lives — where there is no
 * `expect`, so this module is that object: every method throws on failure, and
 * the DO returns the message to the test that drove it.
 */

class ConformanceAssertionError extends Error {}

function fail(message: string): never {
  throw new ConformanceAssertionError(message);
}

function format(value: unknown): string {
  if (typeof value === "string") {
    return JSON.stringify(value);
  }
  if (value === undefined) {
    return "undefined";
  }
  try {
    return JSON.stringify(value) ?? String(value);
  } catch {
    return String(value);
  }
}

function equal(actual: unknown, expected: unknown): boolean {
  if (Object.is(actual, expected)) {
    return true;
  }
  if (typeof actual !== typeof expected) {
    return false;
  }
  if (actual === null || expected === null || typeof actual !== "object") {
    return false;
  }
  const actualArray = Array.isArray(actual);
  if (actualArray !== Array.isArray(expected)) {
    return false;
  }
  if (actualArray) {
    const a = actual as unknown[];
    const e = expected as unknown[];
    return a.length === e.length && a.every((value, index) => equal(value, e[index]));
  }
  const a = actual as Record<string, unknown>;
  const e = expected as Record<string, unknown>;
  const aKeys = Object.keys(a);
  const eKeys = Object.keys(e);
  return (
    aKeys.length === eKeys.length &&
    aKeys.every((key) => key in e && equal(a[key], e[key])) &&
    eKeys.every((key) => key in a)
  );
}

/** `expected` as a subtree: every present key must match, arrays element-wise. */
function matches(actual: unknown, expected: unknown): boolean {
  if (typeof expected === "object" && expected !== null && !Array.isArray(expected)) {
    if (typeof actual !== "object" || actual === null || Array.isArray(actual)) {
      return false;
    }
    const a = actual as Record<string, unknown>;
    return Object.entries(expected as Record<string, unknown>).every(
      ([key, value]) => key in a && matches(a[key], value),
    );
  }
  if (Array.isArray(expected)) {
    return (
      Array.isArray(actual) &&
      actual.length === expected.length &&
      expected.every((value, index) => matches(actual[index], value))
    );
  }
  return equal(actual, expected);
}

function diff(actual: unknown, expected: unknown, what: string): string {
  return `${what}: expected ${format(expected)}, got ${format(actual)}`;
}

export const storageConformanceAssertions: StorageConformanceAssertions = {
  ok(value, message) {
    if (!value) {
      fail(message ?? `expected a truthy value, got ${format(value)}`);
    }
  },
  strictEqual(actual, expected) {
    if (!Object.is(actual, expected)) {
      fail(diff(actual, expected, "expected strict equality"));
    }
  },
  deepEqual(actual, expected) {
    if (!equal(actual, expected)) {
      fail(diff(actual, expected, "expected deep equality"));
    }
  },
  partialDeepEqual(actual, expected) {
    if (!matches(actual, expected)) {
      fail(diff(actual, expected, "expected a partial deep match"));
    }
  },
  greaterThan(actual, expected) {
    if (!(actual > expected)) {
      fail(`expected ${format(actual)} to be greater than ${format(expected)}`);
    }
  },
  async rejects(operation, messageIncludes) {
    try {
      await operation;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (!message.includes(messageIncludes)) {
        fail(`expected the rejection to mention ${format(messageIncludes)}, got: ${message}`);
      }
      return;
    }
    fail(`expected the operation to reject with ${format(messageIncludes)}, but it resolved`);
  },
};
