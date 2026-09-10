import { ApiError, api } from "./api";

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(`Assertion failed: ${message}`);
}

async function withFetch(fake: typeof globalThis.fetch, fn: () => Promise<void>): Promise<void> {
  const original = globalThis.fetch;
  globalThis.fetch = fake;
  try {
    await fn();
  } finally {
    globalThis.fetch = original;
  }
}

async function run(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`✓ ${name}`);
  } catch (error) {
    console.error(`✗ ${name}`);
    throw error;
  }
}

await run("getGitDiffs requests an explicit commit", async () => {
  let requestedUrl = "";
  await withFetch(
    (async (input) => {
      requestedUrl = String(input);
      return Response.json({ diffs: [], gitRoot: "/tmp/repo" });
    }) as typeof globalThis.fetch,
    async () => {
      await api.getGitDiffs("/tmp/a repo", "abc1234");
    },
  );
  assert(
    requestedUrl === "/api/git/diffs?cwd=%2Ftmp%2Fa+repo&commit=abc1234",
    `url = ${requestedUrl}`,
  );
});

await run("hasGitTour uses a HEAD existence probe", async () => {
  let requestedUrl = "";
  let requestedMethod = "";
  await withFetch(
    (async (input, init) => {
      requestedUrl = String(input);
      requestedMethod = init?.method ?? "GET";
      return new Response(null, { status: 200 });
    }) as typeof globalThis.fetch,
    async () => {
      assert(await api.hasGitTour("/tmp/a repo", "abc1234"), "expected a tour");
    },
  );
  assert(requestedMethod === "HEAD", `method = ${requestedMethod}`);
  assert(
    requestedUrl === "/api/git/tour?cwd=%2Ftmp%2Fa+repo&hash=abc1234",
    `url = ${requestedUrl}`,
  );
});

await run("hasGitTour returns false for a missing tour", async () => {
  await withFetch(
    (async () => new Response(null, { status: 404 })) as typeof globalThis.fetch,
    async () => {
      assert(!(await api.hasGitTour("/tmp/repo", "abc1234")), "expected no tour");
    },
  );
});

await run("hasGitTour propagates probe failures", async () => {
  await withFetch(
    (async () => new Response("boom", { status: 500 })) as typeof globalThis.fetch,
    async () => {
      try {
        await api.hasGitTour("/tmp/repo", "abc1234");
        throw new Error("expected hasGitTour to reject");
      } catch (error) {
        assert(error instanceof ApiError, `error = ${String(error)}`);
        assert(error.status === 500, `status = ${error.status}`);
      }
    },
  );
});
