// DOM/API simulation only: node scripts/web_likes_test.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const root = path.join(__dirname, "..");
const html = fs.readFileSync(path.join(root, "web/index.html"), "utf8");
const source = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
assert.equal(ids.length, new Set(ids).size);
for (const match of source.matchAll(/\$\("([^"]+)"\)/g)) assert(ids.includes(match[1]), "missing element " + match[1]);
function deferred() { let release; const promise = new Promise((resolve) => { release = resolve; }); return { promise, release }; }
function setup() {
  const elements = new Map(), calls = [], likes = new Map();
  const options = { failQuery: false, failWrite: false, expire: false, getBarrier: null, writeBarrier: null };
  function element(id) {
    assert(ids.includes(id));
    if (!elements.has(id)) elements.set(id, {
      value: "", textContent: "", open: false, hidden: false, disabled: false, elements: [], attrs: {}, listeners: {},
      classList: { toggle() {} },
      setAttribute(k, v) { this.attrs[k] = v; }, removeAttribute(k) { delete this.attrs[k]; },
      addEventListener(k, v) { this.listeners[k] = v; }, replaceChildren() {},
      showModal() { this.open = true; }, close() { this.open = false; this.listeners.close?.(); },
      pause() {}, load() {}, reset() {},
    });
    return elements.get(id);
  }
  const context = vm.createContext({
    document: { getElementById: element, querySelectorAll: () => [] },
    location: { origin: "http://localhost" }, URL, FormData, TextEncoder,
    setTimeout: () => 0, clearTimeout() {},
    fetch: async (url, request) => {
      calls.push({ url, method: request.method, headers: request.headers });
      let data = { videos: [] }, status = 200;
      const detail = url.match(/^\/videos\/(\d+)$/);
      if (detail) data = { id: Number(detail[1]), title: "test", author_id: 1, play_url: "/media/videos/test.mp4", cover_url: "/cover.svg", created_at: "2026-10-18" };
      const match = url.match(/^\/videos\/(\d+)\/like$/);
      if (match) {
        const id = match[1], user = request.headers.Authorization;
        if (!likes.has(id)) likes.set(id, new Set());
        const users = likes.get(id);
        if (request.method === "GET") {
          data = { like_count: users.size, liked: users.has(user) };
          if (options.failQuery) { status = 500; data = { error: "查询失败" }; }
          if (options.expire) { status = 401; data = { error: "登录失效" }; }
          const barrier = options.getBarrier; options.getBarrier = null;
          if (barrier) await barrier.promise;
        } else {
          const barrier = options.writeBarrier; options.writeBarrier = null;
          if (barrier) await barrier.promise;
          if (options.failWrite) { status = 500; data = { error: "操作失败" }; }
          else { if (request.method === "PUT") users.add(user); else users.delete(user); status = 204; }
        }
      }
      return { ok: status < 400, status, text: async () => status === 204 ? "" : JSON.stringify(data) };
    },
  });
  vm.runInContext(source, context);
  const run = (code) => vm.runInContext(code, context);
  return { run, element, calls, likes, options, click: () => element("like-button").onclick() };
}
const tick = () => new Promise((resolve) => setImmediate(resolve));

(async () => {
  let app = setup();
  await app.run("showDetail(1)");
  assert(!app.calls.some((c) => c.url.endsWith("/like")));
  assert(app.element("like-count").textContent.includes("登录后"));
  await app.click(); assert(app.element("auth-dialog").open);
  console.log("PASS guests are prompted to log in without querying or writing likes");

  app = setup(); app.run('session("a", {id:1,username:"A"})');
  await app.run("showDetail(1)");
  assert.equal(app.element("like-count").textContent, "0 人点赞");
  await app.click();
  assert.equal(app.element("like-count").textContent, "1 人点赞");
  assert.equal(app.element("like-button").attrs["aria-pressed"], "true");
  await app.click();
  assert.equal(app.element("like-count").textContent, "0 人点赞");
  assert.deepEqual(app.calls.filter((c) => c.url.endsWith("/like")).map((c) => c.method), ["GET", "PUT", "GET", "DELETE", "GET"]);
  assert(app.calls.filter((c) => c.url.endsWith("/like")).every((c) => c.headers.Authorization === "Bearer a"));
  console.log("PASS querying, liking, cancelling and server-authoritative counts");

  const gate = deferred(); app.options.writeBarrier = gate;
  const writing = app.click(); await app.click();
  assert(app.element("like-button").disabled);
  const before = app.calls.filter((c) => c.method === "PUT").length;
  gate.release(); await writing;
  assert.equal(app.calls.filter((c) => c.method === "PUT").length, before);
  assert.equal(app.element("like-button").disabled, false);
  console.log("PASS repeated clicks do not send concurrent mutations");

  app.options.failWrite = true; await app.click();
  assert.equal(app.run("state.likeStatus"), null);
  assert(app.element("like-message").textContent.includes("重新查询"));
  app.options.failWrite = false; await app.click();
  assert.equal(app.element("like-button").attrs["aria-pressed"], "true");
  app.options.failQuery = true; await app.run("loadLikeStatus()");
  assert.equal(app.element("like-button").textContent, "重试查询");
  app.options.failQuery = false; await app.click();
  assert.equal(app.element("like-count").textContent, "1 人点赞");
  console.log("PASS failed writes/queries require a fresh query and allow retry");

  app = setup(); app.run('session("a", {id:1,username:"A"})');
  const oldVideo = deferred(); app.options.getBarrier = oldVideo;
  const first = app.run("showDetail(1)"); await tick();
  await app.run("showDetail(2)"); oldVideo.release(); await first;
  assert.equal(app.run("state.detail.id"), 2);
  assert.equal(app.element("like-count").textContent, "0 人点赞");
  console.log("PASS stale responses from a previous video do not overwrite current state");

  app.likes.set("2", new Set(["Bearer a"]));
  const oldAccount = deferred(); app.options.getBarrier = oldAccount;
  const stale = app.run("loadLikeStatus()");
  app.run('session("b", {id:2,username:"B"})'); await tick();
  oldAccount.release(); await stale;
  assert.equal(app.element("like-count").textContent, "1 人点赞");
  assert.equal(app.element("like-button").attrs["aria-pressed"], "false");
  console.log("PASS account changes refresh personal state and ignore the previous account's response");

  const closing = deferred(); app.options.getBarrier = closing;
  const pending = app.run("loadLikeStatus()"); app.element("detail-dialog").close();
  closing.release(); await pending;
  assert.equal(app.run("state.detail"), null);
  assert.equal(app.run("state.likeStatus"), null);
  console.log("PASS closing detail invalidates pending state queries");

  app = setup(); app.run('session("a", {id:1,username:"A"})');
  app.options.expire = true; await app.run("showDetail(1)");
  assert.equal(app.run("state.token"), "");
  assert(app.element("like-button").textContent.includes("登录"));
  assert.equal(app.element("like-button").disabled, false);
  console.log("PASS expired login returns to guest controls without a write");
})().catch((error) => { console.error(error); process.exitCode = 1; });
