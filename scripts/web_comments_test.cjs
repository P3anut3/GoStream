// Simulated DOM/API checks, not browser rendering: node scripts/web_comments_test.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const root = path.join(__dirname, "..");
const source = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const html = fs.readFileSync(path.join(root, "web/index.html"), "utf8");
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((m) => m[1]);
assert.equal(ids.length, new Set(ids).size);
for (const match of source.matchAll(/\$\("([^"]+)"\)/g)) assert(ids.includes(match[1]), "missing element " + match[1]);
assert(!/\.innerHTML\s*=/.test(source), "API text must be rendered as text");
function deferred() { let release; const promise = new Promise((resolve) => { release = resolve; }); return { promise, release }; }
class Node {
  constructor(tag = "div") { Object.assign(this, { tag, children: [], attrs: {}, listeners: {}, value: "", textContent: "", open: false, hidden: false, disabled: false, elements: [], classList: { toggle() {} } }); }
  append(...items) { this.children.push(...items); }
  replaceChildren(...items) { this.children = items; }
  setAttribute(k, v) { this.attrs[k] = v; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(k, v) { this.listeners[k] = v; }
  showModal() { this.open = true; }
  close() { this.open = false; this.listeners.close?.(); }
  pause() {} load() {} reset() {}
}
function setup() {
  const elements = new Map(), calls = [];
  const fixture = Array.from({ length: 6 }, (_, i) => ({ id: i + 1, video_id: 1, user_id: i === 4 ? 2 : 1, content: i === 5 ? "<img src=x onerror=alert(1)>" : "comment " + (i + 1), created_at: "2026-10-18T12:00:00Z" }));
  const records = new Map([[1, fixture], [2, []]]);
  let nextID = 7;
  const options = { failPost: false, failList: false, failDelete: false, expirePost: false, postBarrier: null, listBarrier: null };
  function element(id) { assert(ids.includes(id), id); if (!elements.has(id)) elements.set(id, new Node()); return elements.get(id); }
  const context = vm.createContext({
    document: { getElementById: element, createElement: (tag) => new Node(tag), querySelectorAll: () => [] },
    location: { origin: "http://localhost" }, URL, FormData, TextEncoder, setTimeout: () => 0, clearTimeout() {},
    fetch: async (url, request) => {
      calls.push({ url, request });
      let status = 200, data = { videos: [], next_cursor: "", has_more: false };
      const u = new URL(url, "http://localhost");
      const detail = u.pathname.match(/^\/videos\/(\d+)$/);
      if (detail) data = { id: Number(detail[1]), author_id: 1, title: "test", play_url: "/media/videos/test.mp4", cover_url: "/cover.svg", created_at: "2026-10-18" };
      if (u.pathname.endsWith("/like")) data = { like_count: 0, liked: false };
      const list = u.pathname.match(/^\/videos\/(\d+)\/comments$/);
      if (list) {
        const id = Number(list[1]), items = records.get(id), uid = request.headers.Authorization === "Bearer b" ? 2 : 1;
        if (request.method === "GET") {
          const page = Number(u.searchParams.get("page") || 1), size = Number(u.searchParams.get("page_size") || 10);
          data = { video_id: id, page, page_size: size, comments: [...items].sort((a, b) => b.id - a.id).slice((page - 1) * size, page * size).map((x) => ({ ...x })) };
          if (options.failList) { status = 500; data = { error: "列表失败" }; }
          const gate = options.listBarrier; options.listBarrier = null;
          if (gate) await gate.promise;
        } else {
          const gate = options.postBarrier; options.postBarrier = null;
          if (gate) await gate.promise;
          if (options.expirePost) { status = 401; data = { error: "登录失效" }; }
          else if (options.failPost) { status = 500; data = { error: "发送失败" }; }
          else { data = { id: nextID++, video_id: id, user_id: uid, content: JSON.parse(request.body).content, created_at: "2026-10-18T13:00:00Z" }; items.push(data); status = 201; }
        }
      }
      const del = u.pathname.match(/^\/comments\/(\d+)$/);
      if (del) {
        if (options.failDelete) { status = 500; data = { error: "删除失败" }; }
        else {
          const uid = request.headers.Authorization === "Bearer b" ? 2 : 1;
          const items = [...records.values()].find((items) => items.some((x) => x.id === Number(del[1]) && x.user_id === uid));
          if (!items) { status = 404; data = { error: "不属于当前用户" }; }
          else { items.splice(items.findIndex((x) => x.id === Number(del[1])), 1); status = 204; }
        }
      }
      return { ok: status < 400, status, text: async () => status === 204 ? "" : JSON.stringify(data) };
    },
  });
  vm.runInContext(source, context);
  const run = (code) => vm.runInContext(code, context);
  const submit = () => element("comment-form").onsubmit({ preventDefault() {} });
  const deleteButtons = () => element("comments-list").children.flatMap((row) => row.children[0].children.filter((node) => node.tag === "button"));
  const posts = () => calls.filter((c) => c.request.method === "POST" && c.url.includes("/comments"));
  return { run, element, calls, records, options, submit, posts, deleteButtons };
}
const tick = () => new Promise((resolve) => setImmediate(resolve));
(async () => {
  let app = setup(); await app.run("showDetail(1)");
  assert.equal(app.element("comments-list").children.length, 5);
  assert.equal(app.deleteButtons().length, 0);
  assert.equal(app.element("comments-list").children[0].children[1].textContent, "<img src=x onerror=alert(1)>");
  await app.element("comments-next").onclick();
  assert.equal(app.element("comments-list").children.length, 1);
  assert.equal(app.element("comments-next").disabled, true);
  await app.element("comments-prev").onclick();
  await app.submit(); assert(app.element("auth-dialog").open); assert.equal(app.posts().length, 0);
  console.log("PASS public pagination, literal text rendering and guest login prompt");

  app.run('session("a", {id:1,username:"A"})'); await tick();
  assert.equal(app.deleteButtons().length, 4);
  app.element("comment-content").value = "  新评论！  "; await app.submit();
  assert.equal(JSON.parse(app.posts()[0].request.body).content, "新评论！");
  assert.equal(app.posts()[0].request.headers.Authorization, "Bearer a");
  assert.equal(app.element("comment-content").value, "");
  assert.equal(app.run("state.commentPage"), 1);
  assert.equal(app.run("state.comments[0].content"), "新评论！");
  console.log("PASS authenticated submission clears draft only on success and reloads page one");

  const before = app.posts().length;
  for (const content of [" \t\n　", "评".repeat(501)]) { app.element("comment-content").value = content; await app.submit(); }
  assert.equal(app.posts().length, before);
  app.element("comment-content").value = "😀".repeat(500); await app.submit();
  assert.equal(app.posts().length, before + 1);
  console.log("PASS empty/oversized comments are rejected and 500 Unicode code points are allowed");

  app.options.failPost = true; app.element("comment-content").value = "保留我的输入"; await app.submit();
  assert.equal(app.element("comment-content").value, "保留我的输入");
  assert(app.element("comment-message").textContent.includes("输入已保留"));
  assert.equal(app.element("comment-submit").disabled, false);
  app.options.failPost = false;
  const gate = deferred(); app.options.postBarrier = gate;
  const pending = app.submit(); const count = app.posts().length; await app.submit();
  assert.equal(app.posts().length, count); gate.release(); await pending;
  console.log("PASS failed posts preserve input and repeated clicks send only one request");

  app = setup(); app.run('session("a", {id:1,username:"A"})'); await app.run("showDetail(1)");
  await app.element("comments-next").onclick();
  app.deleteButtons()[0].onclick();
  assert(app.element("comment-delete-dialog").open);
  assert(!app.calls.some((c) => c.request.method === "DELETE"));
  app.element("comment-delete-dialog").close();
  assert.equal(app.run("state.commentDeleteTarget"), null);
  app.deleteButtons()[0].onclick();
  app.options.failDelete = true; await app.element("confirm-comment-delete").onclick();
  assert.equal(app.run("state.comments.length"), 1);
  assert(app.element("comment-delete-error").textContent.includes("删除失败"));
  app.options.failDelete = false; await app.element("confirm-comment-delete").onclick();
  assert.equal(app.run("state.commentPage"), 1);
  assert.equal(app.records.get(1).length, 5);
  console.log("PASS author-only delete, confirmation/cancel, failure retry and last-page fallback");

  app.run('session("b", {id:2,username:"B"})'); await tick();
  assert.equal(app.deleteButtons().length, 1);
  app.options.failList = true; await app.element("refresh-comments").onclick();
  assert(app.element("comments-list-message").textContent.includes("刷新评论"));
  app.options.failList = false; await app.element("refresh-comments").onclick();
  assert.equal(app.element("comments-list").children.length, 5);
  console.log("PASS account changes update delete controls and list failures are retryable");

  app = setup(); const stale = deferred(); app.options.listBarrier = stale;
  const first = app.run("showDetail(1)"); await tick(); await app.run("showDetail(2)");
  stale.release(); await first;
  assert.equal(app.run("state.detail.id"), 2); assert.equal(app.run("state.comments.length"), 0);
  app.run('session("a", {id:1,username:"A"})'); await tick(); await app.run("showDetail(1)");
  const oldPost = deferred(); app.options.postBarrier = oldPost; app.element("comment-content").value = "旧视频的评论";
  const writing = app.submit(); await app.run("showDetail(2)"); app.element("comment-content").value = "新视频的草稿";
  oldPost.release(); await writing;
  assert.equal(app.element("comment-content").value, "新视频的草稿"); assert.equal(app.run("state.comments.length"), 0);
  console.log("PASS late reads and writes from an old video cannot overwrite the new video or its draft");

  app = setup(); app.run('session("a", {id:1,username:"A"})'); await app.run("showDetail(1)");
  app.options.expirePost = true; app.element("comment-content").value = "登录失效时也保留"; await app.submit(); await tick();
  assert.equal(app.run("state.token"), ""); assert.equal(app.element("comment-content").value, "登录失效时也保留");
  assert.equal(app.deleteButtons().length, 0); assert.equal(app.element("comment-submit").textContent, "登录后发表评论");
  console.log("PASS expired login preserves the draft and removes private action controls");
})().catch((error) => { console.error(error); process.exitCode = 1; });
