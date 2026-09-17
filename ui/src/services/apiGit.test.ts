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

await run("getGitTourStatus requests the durable worker state", async () => {
  let requestedUrl = "";
  await withFetch(
    (async (input) => {
      requestedUrl = String(input);
      return Response.json({ status: "absent", hash: "abc1234" });
    }) as typeof globalThis.fetch,
    async () => {
      const status = await api.getGitTourStatus("/tmp/a repo", "abc1234");
      assert(status.status === "absent", `status = ${status.status}`);
    },
  );
  assert(
    requestedUrl === "/api/git/tour/status?cwd=%2Ftmp%2Fa+repo&hash=abc1234",
    `url = ${requestedUrl}`,
  );
});

await run("requestGitTour sends the built-in command with its worktree", async () => {
  let requestedUrl = "";
  let requestedBody = "";
  await withFetch(
    (async (input, init) => {
      requestedUrl = String(input);
      requestedBody = String(init?.body);
      return Response.json({
        status: "accepted",
        tour: { status: "building", hash: "abc1234", worker_slug: "tour-abc1234" },
      });
    }) as typeof globalThis.fetch,
    async () => {
      const status = await api.requestGitTour("conversation", "/tmp/a repo", "abc1234");
      assert(status.status === "building", `status = ${status.status}`);
    },
  );
  assert(requestedUrl === "/api/conversation/conversation/chat", `url = ${requestedUrl}`);
  const body = JSON.parse(requestedBody) as { message: string };
  assert(body.message === "/tour abc1234\n/tmp/a repo", `message = ${body.message}`);
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
