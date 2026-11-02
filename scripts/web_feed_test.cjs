// DOM/API regression checks. No real browser, database, or uploaded file is used.
const assert = require("node:assert/strict");
const fs = require("node:fs"), path = require("node:path"), vm = require("node:vm");
const root = path.join(__dirname, "..");
const source = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const html = fs.readFileSync(path.join(root, "web/index.html"), "utf8");
const ids = [...html.matchAll(/id="([^"]+)"/g)].map(m => m[1]);
assert.equal(new Set(ids).size, ids.length);
for (const match of source.matchAll(/\$\("([^"]+)"\)/g)) assert(ids.includes(match[1]), match[1]);
class Node {
  constructor() { Object.assign(this, { children: [], attrs: {}, listeners: {}, value: "6", textContent: "", disabled: false, hidden: false, open: false, elements: [], classList: { toggle() {} } }); }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  setAttribute(k, v) { this.attrs[k] = v; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(k, fn) { this.listeners[k] = fn; }
  showModal() { this.open = true; }
  close() { this.open = false; this.listeners.close?.(); }
  pause() {} load() {} reset() {}
}
const tick = () => new Promise(resolve => setImmediate(resolve));
const video = id => ({ id, author_id: 1, title: "video " + id, created_at: "2026-10-20T10:00:00Z", cover_url: "/cover.svg" });
const page = (ids, cursor = "") => ({ videos: ids.map(video), next_cursor: cursor, has_more: Boolean(cursor) });
// Keep the process alive long enough to fail if a deferred test never settles.
const watchdog = setTimeout(() => { throw new Error("Feed tests did not finish"); }, 10000);
function setup() {
  const elements = new Map(), calls = [], replies = [];
  const element = id => { assert(ids.includes(id), id); if (!elements.has(id)) elements.set(id, new Node()); return elements.get(id); };
  const context = vm.createContext({
    document: { getElementById: element, createElement: () => new Node(), querySelectorAll: () => [] },
    location: { origin: "http://localhost" }, URL, FormData, TextEncoder, setTimeout: () => 0, clearTimeout() {},
    fetch: async (url, request) => {
      calls.push({ url, request });
      const result = await (replies.length ? replies.shift()(url, request) : { data: page([]) });
      const status = result.status || 200;
      return { ok: status < 400, status, text: async () => JSON.stringify(result.data) };
    },
  });
  vm.runInContext(source, context);
  const run = code => vm.runInContext(code, context);
  const enqueue = data => replies.push(async () => ({ data }));
  const shown = () => Array.from(run("state.videos"), item => item.id);
  return { element, calls, replies, run, enqueue, shown };
}
(async () => {
  let app = setup(); await tick();
  assert.equal(app.calls[0].url, "/feed?limit=6");
  assert.equal(app.calls[0].request.headers.Authorization, undefined);
  app.enqueue(page([30, 29], "cursor/A+=")); await app.run("loadVideos()");
  app.enqueue(page([28])); await app.element("load-more").onclick();
  assert.equal(app.calls.at(-1).url, "/feed?limit=6&cursor=cursor%2FA%2B%3D");
  assert.deepEqual(app.shown(), [30, 29, 28]);
  assert.equal(app.element("load-more").disabled, true);
  const count = app.calls.length; await app.element("load-more").onclick(); assert.equal(app.calls.length, count);
  assert.equal(app.element("prev-page").hidden, true);
  console.log("PASS public Feed, encoded opaque cursor, append order and end-of-list guard");

  app.enqueue(page([31], "fresh")); await app.element("refresh-button").onclick();
  assert.equal(app.calls.at(-1).url, "/feed?limit=6"); assert.deepEqual(app.shown(), [31]);
  app.element("page-size").value = "12"; app.enqueue(page([32])); await app.element("page-size").onchange();
  assert.equal(app.calls.at(-1).url, "/feed?limit=12"); assert.deepEqual(app.shown(), [32]);
  console.log("PASS refresh and batch-size changes replace data and clear cursor");

  app.enqueue(page([30], "retry")); await app.run("loadVideos()");
  app.replies.push(async () => { throw new Error("offline"); }); await app.element("load-more").onclick();
  assert.deepEqual(app.shown(), [30]); assert.equal(app.run("state.nextCursor"), "retry");
  assert.equal(app.element("load-more").disabled, false);
  app.enqueue(page([30, 29])); await app.element("load-more").onclick();
  assert(app.calls.at(-1).url.endsWith("cursor=retry")); assert.deepEqual(app.shown(), [30, 29]);
  console.log("PASS failed append retains list/cursor and retries without duplicate cards");

  app.enqueue(page([30], "expired")); await app.run("loadVideos()");
  app.replies.push(async () => ({ status: 400, data: { code: "INVALID_CURSOR", error: "游标失效" } }));
  await app.element("load-more").onclick();
  assert.deepEqual(app.shown(), [30]); assert.equal(app.element("load-more").disabled, true);
  assert(app.element("list-message").textContent.includes("刷新"));
  app.enqueue(page([33])); await app.element("refresh-button").onclick(); assert.deepEqual(app.shown(), [33]);
  console.log("PASS invalid cursor requires refresh without clearing displayed videos");

  app.enqueue(page([30], "old")); await app.run("loadVideos()");
  let release;
  app.replies.push(() => new Promise(resolve => { release = resolve; }));
  const pending = app.element("load-more").onclick();
  const once = app.calls.length; await app.element("load-more").onclick(); assert.equal(app.calls.length, once);
  app.enqueue(page([40], "new")); await app.element("refresh-button").onclick();
  release({ data: page([29]) }); await pending;
  assert.deepEqual(app.shown(), [40]); assert.equal(app.run("state.nextCursor"), "new");
  console.log("PASS duplicate click guarded and late append cannot overwrite refresh");

  await app.run('session("a", {id:1,username:"A"})');
  app.enqueue(page([40], "new")); await app.run("loadVideos()");
  app.replies.push(() => new Promise(resolve => { release = resolve; }));
  const oldPublic = app.element("load-more").onclick();
  app.enqueue({ videos: Array.from({ length: 12 }, (_, i) => video(100 + i)) });
  await app.run('selectScope("mine")');
  assert.equal(app.calls.at(-1).url, "/me/videos?page=1&page_size=12");
  assert.equal(app.calls.at(-1).request.headers.Authorization, "Bearer a");
  release({ data: page([39]) }); await oldPublic;
  assert.equal(app.shown()[0], 100); assert.equal(app.element("load-more").hidden, true);
  app.enqueue({ videos: [video(200)] }); await app.element("next-page").onclick();
  assert.equal(app.calls.at(-1).url, "/me/videos?page=2&page_size=12");
  app.enqueue(page([50])); await app.run('selectScope("all")');
  assert.equal(app.calls.at(-1).url, "/feed?limit=12"); assert.deepEqual(app.shown(), [50]);
  console.log("PASS public/personal mode isolation, private page navigation and stale response rejection");

  app.enqueue(page([60], "keep")); await app.run("loadVideos()");
  app.enqueue({ videos: [video(59)], next_cursor: "", has_more: true });
  await app.element("load-more").onclick();
  assert.deepEqual(app.shown(), [60]); assert.equal(app.run("state.nextCursor"), "keep");
  app.enqueue(page([59], "keep")); await app.element("load-more").onclick();
  assert.deepEqual(app.shown(), [60]);
  console.log("PASS inconsistent or non-advancing pagination responses never commit partial state");

  await app.run('session("a", {id:1,username:"A"})');
  app.enqueue({ videos: [video(700)] }); await app.run('selectScope("mine")');
  app.replies.push(() => new Promise(resolve => { release = resolve; }));
  const privateRead = app.run("loadVideos()");
  app.enqueue(page([80])); app.run("session()"); await tick();
  release({ data: { videos: [video(701)] } }); await privateRead;
  assert.equal(app.run("state.scope"), "all"); assert.deepEqual(app.shown(), [80]);
  assert.equal(app.element("load-more").hidden, false);
  console.log("PASS logout resets private state and rejects late private responses");

  app = setup(); await tick();
  const enriched = page([90]);
  Object.assign(enriched.videos[0], { author_name: "<作者>", like_count: 3, liked: true });
  app.enqueue(enriched); await app.run('session("a", {id:1,username:"A"})');
  assert.equal(app.calls.at(-1).request.headers.Authorization, "Bearer a");
  const textOf = node => [node.textContent, ...node.children.map(textOf)].join(" ");
  assert(textOf(app.element("video-grid")).includes("<作者>"));
  assert(textOf(app.element("video-grid")).includes("♥ 3 · 已点赞"));
  app.enqueue(page([90])); await app.run("session()");
  assert.equal(app.calls.at(-1).request.headers.Authorization, undefined);
  assert(!textOf(app.element("video-grid")).includes("已点赞"));
  console.log("PASS author text, counts, optional credentials and logout personal-state reset");

  app.enqueue(page([91], "next")); await app.run('session("a", {id:1,username:"A"})');
  app.replies.push(async () => ({ status: 401, data: { error: "登录已过期" } }));
  app.enqueue(page([92])); await app.element("load-more").onclick(); await tick();
  assert.equal(app.run("state.token"), ""); assert.deepEqual(app.shown(), [92]);
  console.log("PASS expired optional token clears session and refreshes guest Feed");

  app = setup(); await tick();
  assert.deepEqual(app.shown(), []); assert.equal(app.element("load-more").disabled, true);
  console.log("PASS empty Feed displays an empty state without requesting another batch");
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(() => clearTimeout(watchdog));
