// Simulate DOM and API responses to test publishing without a browser or database.
// Run: node scripts/web_publish_test.cjs
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { File } = require("node:buffer");
const root = path.join(__dirname, "..");
const html = fs.readFileSync(path.join(root, "web/index.html"), "utf8");
const source = fs.readFileSync(path.join(root, "web/app.js"), "utf8");
const ids = [...html.matchAll(/id="([^"]+)"/g)].map((match) => match[1]);
assert.equal(ids.length, new Set(ids).size, "HTML IDs must be unique");
for (const match of source.matchAll(/\$\("([^"]+)"\)/g)) assert(ids.includes(match[1]), "missing DOM element: " + match[1]);

function setup() {
  const elements = new Map(), calls = [], revoked = [];
  let objectID = 0;
  function element(id) {
    assert(ids.includes(id), "unknown element " + id);
    if (!elements.has(id)) elements.set(id, {
      value: "", hidden: false, open: false, textContent: "", elements: [],
      classList: { toggle() {} }, setAttribute() {}, removeAttribute(name) { delete this[name]; },
      replaceChildren() {}, addEventListener() {}, showModal() { this.open = true; },
      close() { this.open = false; }, pause() {}, load() {}, reset() {},
    });
    return elements.get(id);
  }
  const options = { failPublish: false, failCover: false, expireCover: false };
  class TestURL extends URL {
    static createObjectURL() { return "blob:test-" + ++objectID; }
    static revokeObjectURL(url) { revoked.push(url); }
  }
  const context = vm.createContext({
    document: { getElementById: element, querySelectorAll: () => [] },
    location: { origin: "http://localhost" }, URL: TestURL, File, Blob, FormData, TextEncoder,
    setTimeout: () => 0, clearTimeout() {},
    fetch: async (url, request) => {
      calls.push({ url, request });
      let status = 200, data = { videos: [] };
      if (url === "/uploads/videos") data = { play_url: "/media/videos/test.mp4" };
      if (url === "/uploads/covers") {
        data = { cover_url: "/media/covers/test.png" };
        if (options.failCover) { status = 500; data = { error: "封面上传失败" }; }
        if (options.expireCover) { status = 401; data = { error: "登录失效" }; }
      }
      if (url === "/videos" && request.method === "POST") {
        status = options.failPublish ? 500 : 201;
        data = options.failPublish ? { error: "发布失败" } : { id: 1 };
      }
      return { ok: status < 400, status, text: async () => JSON.stringify(data) };
    },
  });
  vm.runInContext(source, context);
  const run = (code) => vm.runInContext(code, context);
  run('session("test-token", {id:1, username:"tester"}); chooseFile(new File(["video"], "demo.mp4", {type:"video/mp4"}))');
  element("video-title").value = "test video";
  const submit = () => element("publish-form").onsubmit({ preventDefault() {} });
  const posts = (url) => calls.filter((call) => call.url === url && call.request.method === "POST");
  return { run, element, submit, posts, calls, options, revoked };
}

(async () => {
  let app = setup();
  await app.submit();
  assert.equal(app.posts("/uploads/covers").length, 0);
  assert.equal(JSON.parse(app.posts("/videos")[0].request.body).cover_url, "/cover.svg");
  console.log("PASS default cover without an image upload");

  app = setup();
  app.run('chooseCover(new File(["image"], "封面.PNG", {type:"image/png"}))');
  assert.equal(app.element("cover-preview").hidden, false);
  await app.submit();
  assert.deepEqual(app.calls.filter((call) => call.request.method === "POST").map((call) => call.url), ["/uploads/videos", "/uploads/covers", "/videos"]);
  const coverRequest = app.posts("/uploads/covers")[0].request;
  assert.equal(coverRequest.headers.Authorization, "Bearer test-token");
  assert.equal(coverRequest.body.get("file").name, "封面.PNG");
  assert.equal(coverRequest.headers["Content-Type"], undefined, "browser must generate multipart boundary");
  assert.equal(JSON.parse(app.posts("/videos")[0].request.body).cover_url, "/media/covers/test.png");
  assert.equal(app.run("state.coverFile"), null);
  assert.equal(app.element("cover-preview").hidden, true);
  assert.equal(app.revoked.length, 2);
  console.log("PASS video, cover, publication sequence and successful cleanup");

  app = setup(); app.options.failPublish = true;
  app.run('chooseCover(new File(["image"], "cover.png", {type:"image/png"}))');
  await app.submit(); app.options.failPublish = false; await app.submit();
  assert.equal(app.posts("/uploads/videos").length, 1);
  assert.equal(app.posts("/uploads/covers").length, 1);
  assert.equal(app.posts("/videos").length, 2);
  console.log("PASS failed publication reuses both uploaded files");

  app = setup(); app.options.failCover = true;
  app.run('chooseCover(new File(["image"], "cover.png", {type:"image/png"}))');
  await app.submit(); assert.equal(app.posts("/videos").length, 0);
  app.options.failCover = false; await app.submit();
  assert.equal(app.posts("/uploads/videos").length, 1);
  assert.equal(app.posts("/uploads/covers").length, 2);
  console.log("PASS failed cover upload retries without reuploading video");

  app = setup(); app.options.failPublish = true;
  app.run('chooseCover(new File(["image"], "cover.png", {type:"image/png"}))');
  await app.submit();
  app.run('chooseCover(new File(["new image"], "new.jpg", {type:"image/jpeg"}))');
  assert.equal(app.run("state.uploadedCover"), null);
  await app.submit();
  assert.equal(app.posts("/uploads/videos").length, 1);
  assert.equal(app.posts("/uploads/covers").length, 2);
  app.element("remove-cover").onclick();
  assert.equal(app.run("state.coverFile"), null);
  assert.equal(app.run("state.uploadedCover"), null);
  assert.equal(app.element("cover-preview").hidden, true);
  console.log("PASS replacing/removing cover clears its cached result and preview");

  for (const expression of [
    'new File(["image"], "cover.gif", {type:"image/gif"})',
    'new File([], "cover.png", {type:"image/png"})',
    'new File([new Uint8Array(5*1024*1024+1)], "cover.png", {type:"image/png"})',
    'new File(["image"], "cover.png", {type:"text/plain"})',
  ]) {
    app = setup(); app.run("chooseCover(" + expression + ")");
    assert.equal(app.run("state.coverFile"), null);
    assert(app.element("publish-message").textContent.includes("5 MiB"));
  }
  console.log("PASS invalid extension, empty file, oversize and MIME validation");

  app = setup(); app.options.failPublish = true;
  app.run('chooseCover(new File(["image"], "cover.png", {type:"image/png"}))');
  await app.submit();
  app.run('session("other-token", {id:2,username:"other"})');
  assert.equal(app.run("state.uploaded"), null);
  assert.equal(app.run("state.uploadedCover"), null);
  app.options.expireCover = true; await app.submit();
  assert.equal(app.run("state.token"), "");
  assert.equal(app.run("state.uploaded"), null);
  assert.equal(app.posts("/videos").length, 1, "expired session must not publish again");
  console.log("PASS changed or expired login never reuses another session's uploads");
})().catch((error) => { console.error(error); process.exitCode = 1; });
