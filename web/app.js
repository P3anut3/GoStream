"use strict";

// 统一请求入口，token 仅保存在当前页面内存，不写入 localStorage。
const $ = (id) => document.getElementById(id);
const state = { token: "", user: null, scope: "all", page: 1, size: 6, videos: [], mode: "login", source: "file", file: null, uploaded: null, coverFile: null, uploadedCover: null, coverPreview: "", detail: null, preview: "", busy: false, listSequence: 0, detailSequence: 0, likeStatus: null, likeLoading: false, likeBusy: false, likeSequence: 0, commentPage: 1, commentSize: 5, comments: [], commentLoading: false, commentBusy: false, commentHasNext: false, commentError: "", commentSequence: 0, commentMutation: 0, commentDeleteTarget: null };
let toastTimer;
let pendingMine = false;
function notify(message) { $("toast").textContent = message; $("toast").hidden = false; clearTimeout(toastTimer); toastTimer = setTimeout(() => { $("toast").hidden = true; }, 4500); }
function mediaURL(value) { if (!value) return ""; try { const u = new URL(value, location.origin); return ["http:", "https:"].includes(u.protocol) ? u.href : ""; } catch { return ""; } }
function formatDate(value) { const d = new Date(value); return Number.isNaN(d.valueOf()) ? "时间未知" : d.toLocaleDateString("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit" }); }
function session(token = "", user = null) { const changed = state.token !== token; if (changed) { state.uploaded = null; state.uploadedCover = null; } state.token = token; state.user = user; $("account-name").textContent = user ? user.username : "访客模式"; $("auth-button").textContent = user ? "账户信息" : "登录 / 注册"; $("logout-button").hidden = !user; $("delete-button").hidden = !user || !state.detail || String(state.detail.author_id) !== String(user.id); if (changed) { syncCommentSession(); resetLikeState(); if (state.detail && $("detail-dialog").open) loadLikeStatus(); } if (changed && state.scope === "mine") { if (!token) state.scope = "all"; state.page = 1; updateScope(); loadVideos(); } }
async function api(path, { method = "GET", body, auth = false, form = false } = {}) {
  if (auth && !state.token) throw new Error("请先登录后再操作");
  const requestToken = state.token;
  const headers = {};
  if (auth) headers.Authorization = "Bearer " + requestToken;
  if (body && !form) headers["Content-Type"] = "application/json";
  let response;
  try { response = await fetch(path, { method, headers, body: body ? (form ? body : JSON.stringify(body)) : undefined }); }
  catch { throw new Error("无法连接服务，请检查网络并确认 GoStream 正在运行"); }
  const text = await response.text(); let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { if (response.ok) throw new Error("服务器响应格式异常"); }
  if (!response.ok) { if (response.status === 401 && auth && state.token === requestToken) { session(); notify("登录状态已失效，请重新登录"); } throw new Error(data?.error || (response.status === 413 ? (path === "/uploads/covers" ? "封面或请求过大，请选择不超过 5 MiB 的图片" : "文件或请求过大，请选择不超过 100 MiB 的视频") : "请求失败（" + response.status + "）")); }
  return data;
}
function openAuth(mode = "login") { state.mode = mode; $("auth-title").textContent = mode === "login" ? "欢迎回来" : "创建你的账号"; $("auth-description").textContent = mode === "login" ? "登录后，发布属于你的视频。" : "从你的第一个视频开始。"; $("tab-login").classList.toggle("selected", mode === "login"); $("tab-register").classList.toggle("selected", mode === "register"); $("password").autocomplete = mode === "login" ? "current-password" : "new-password"; $("auth-submit").textContent = mode === "login" ? "登录" : "创建账号"; $("auth-error").textContent = ""; if (!$("auth-dialog").open) $("auth-dialog").showModal(); }
$("tab-login").onclick = () => openAuth("login"); $("tab-register").onclick = () => openAuth("register");
$("auth-form").onsubmit = async (event) => {
  event.preventDefault(); const username = $("username").value; const password = $("password").value;
  const length = new TextEncoder().encode(password).length;
  if (state.mode === "register" && (length < 8 || length > 72)) { $("auth-error").textContent = "密码需为 8～72 字节"; return; }
  const registering = state.mode === "register"; let registered = false; $("auth-submit").disabled = true; $("tab-login").disabled = true; $("tab-register").disabled = true; $("auth-error").textContent = "";
  try { const credentials = { username, password }; if (registering) { await api("/users", { method: "POST", body: credentials }); registered = true; } const data = await api("/login", { method: "POST", body: credentials }); if (!data?.token) throw new Error("登录响应缺少凭证"); session(data.token, { id: data.id, username: data.username }); $("password").value = ""; const goToMine = pendingMine; $("auth-dialog").close(); if (goToMine) { pendingMine = false; selectScope("mine"); } notify(registering ? "账号已创建，欢迎加入 GoStream" : "欢迎回来，" + data.username); }
  catch (error) { $("auth-error").textContent = registered ? "账号已创建，但自动登录失败。请切换到登录后重试：" + error.message : error.message; }
  finally { $("auth-submit").disabled = false; $("tab-login").disabled = false; $("tab-register").disabled = false; }
};
async function showProfile() { if (!state.token) { openAuth(); return; } const token = state.token; try { const data = await api("/me", { auth: true }); if (state.token !== token) return; session(token, data); notify("当前用户：" + data.username + " · ID " + data.id); } catch (error) { notify(error.message); } }
$("auth-button").onclick = () => state.token ? showProfile() : openAuth(); $("profile-button").onclick = showProfile;
$("logout-button").onclick = () => { if (state.busy) { notify("请等待当前发布完成"); return; } session(); notify("已退出当前页面的登录状态"); };

function updateScope() {
  const mine = state.scope === "mine";
  $("library-title").textContent = mine ? "我的视频" : "视频库";
  $("breadcrumb-title").textContent = mine ? "我的视频" : "视频库";
  for (const [id, active] of [["nav-library", !mine], ["nav-mine", mine]]) {
    $(id).classList.toggle("active", active);
    if (active) $(id).setAttribute("aria-current", "page"); else $(id).removeAttribute("aria-current");
  }
}
function selectScope(scope) {
  if (scope === "mine" && !state.token) { pendingMine = true; openAuth(); notify("登录后即可查看我的视频"); return; }
  pendingMine = false; state.scope = scope; state.page = 1; updateScope(); loadVideos();
}
$("nav-library").onclick = () => selectScope("all");
$("nav-mine").onclick = () => selectScope("mine");
$("auth-dialog").addEventListener("close", () => { pendingMine = false; });

// 接口文本始终通过 textContent 写入 DOM，不使用 innerHTML。
function card(video) {
  const article = document.createElement("article"); article.className = "video-card";
  const preview = document.createElement("button"); preview.className = "card-preview"; preview.type = "button"; preview.setAttribute("aria-label", "播放：" + video.title);
  const img = document.createElement("img"); img.src = mediaURL(video.cover_url) || "/cover.svg"; img.alt = ""; img.loading = "lazy"; img.referrerPolicy = "no-referrer"; img.onerror = () => { img.onerror = null; img.src = "/cover.svg"; };
  const play = document.createElement("span"); play.className = "play-circle"; play.textContent = "▶";
  const id = document.createElement("span"); id.className = "card-id"; id.textContent = "片段 #" + video.id;
  preview.append(img, play, id); preview.onclick = () => showDetail(video.id);
  const content = document.createElement("div"); content.className = "card-content";
  const title = document.createElement("button"); title.className = "card-title"; title.textContent = video.title; title.title = video.title; title.onclick = () => showDetail(video.id);
  const meta = document.createElement("div"); meta.className = "card-meta";
  const author = document.createElement("span"); author.className = "author-tag";
  const avatar = document.createElement("span"); avatar.className = "mini-avatar"; avatar.textContent = String(video.author_id).slice(-2);
  const authorName = document.createElement("span"); authorName.textContent = "创作者 " + video.author_id;
  const date = document.createElement("time"); date.textContent = formatDate(video.created_at); author.append(avatar, authorName); meta.append(author, date); content.append(title, meta); article.append(preview, content); return article;
}
async function loadVideos() {
  const sequence = ++state.listSequence; $("list-message").textContent = "正在整理你的片段……"; $("video-grid").replaceChildren(); $("refresh-button").disabled = true; $("prev-page").disabled = true; $("next-page").disabled = true;
  try { const mine = state.scope === "mine"; const data = await api((mine ? "/me/videos" : "/videos") + "?page=" + state.page + "&page_size=" + state.size, { auth: mine }); if (sequence !== state.listSequence) return; state.videos = Array.isArray(data.videos) ? data.videos : []; $("video-grid").replaceChildren(...state.videos.map(card)); $("list-message").textContent = state.videos.length ? "" : (state.page === 1 ? (state.scope === "mine" ? "你还没有发布视频。发布你的第一个片段吧。" : "这里还没有视频。发布你的第一个片段吧。") : "这一页还没有视频，试试返回上一页。"); $("page-count").textContent = "本页 " + state.videos.length + " 条"; $("page-number").textContent = String(state.page).padStart(2, "0"); $("page-description").textContent = "第 " + state.page + " 页 · " + (state.scope === "mine" ? "我的作品" : "发现每一个片段"); $("prev-page").disabled = state.page <= 1; $("next-page").disabled = state.videos.length < state.size || state.page >= 10000; }
  catch (error) { if (sequence === state.listSequence) { $("list-message").textContent = error.message + "，可点击刷新重试。"; $("page-count").textContent = "暂不可用"; $("prev-page").disabled = state.page <= 1; } }
  finally { if (sequence === state.listSequence) $("refresh-button").disabled = false; }
}
$("refresh-button").onclick = loadVideos; $("prev-page").onclick = () => { if (state.page > 1) { state.page--; loadVideos(); } }; $("next-page").onclick = () => { state.page++; loadVideos(); }; $("page-size").onchange = () => { state.size = Number($("page-size").value); state.page = 1; loadVideos(); }; $("lookup-form").onsubmit = (e) => { e.preventDefault(); showDetail($("lookup-id").value); };
async function showDetail(id) {
  const sequence = ++state.detailSequence;
  state.detail = null; resetLikeState(); resetComments();
  try { const video = await api("/videos/" + encodeURIComponent(id)); if (sequence !== state.detailSequence) return; state.detail = video; $("detail-title").textContent = video.title; $("detail-label").textContent = "VIDEO / " + video.id; $("detail-meta").textContent = "创作者 " + video.author_id + " · " + formatDate(video.created_at); const url = mediaURL(video.play_url); $("playback-message").textContent = url ? "" : "视频地址格式不受支持"; $("detail-player").removeAttribute("src"); if (url) $("detail-player").src = url; $("detail-player").poster = mediaURL(video.cover_url) || "/cover.svg"; $("open-media").hidden = !url; if (url) $("open-media").href = url; $("delete-button").hidden = !state.user || String(video.author_id) !== String(state.user.id); if (!$("detail-dialog").open) $("detail-dialog").showModal(); await Promise.all([loadLikeStatus(), loadComments()]); }
  catch (error) { notify(error.message); }
}
// 点赞数量来自后端查询；只在请求成功后刷新，不在前端猜测数量。
function renderLikes() {
  const button = $("like-button");
  const status = state.likeStatus;
  button.classList.toggle("liked", Boolean(status?.liked));
  button.setAttribute("aria-pressed", String(Boolean(status?.liked)));
  button.disabled = !state.detail || state.likeBusy || state.likeLoading;
  $("refresh-likes").hidden = !state.token;
  $("refresh-likes").disabled = button.disabled;
  if (!state.token) {
    button.textContent = "♡ 登录后点赞"; $("like-count").textContent = "登录后查看点赞数量";
  } else if (state.likeBusy) {
    button.textContent = "正在处理……";
  } else if (state.likeLoading) {
    button.textContent = "正在查询……"; $("like-count").textContent = "正在读取点赞数量";
  } else if (!status) {
    button.textContent = "重试查询"; $("like-count").textContent = "点赞状态暂不可用";
  } else {
    button.textContent = status.liked ? "♥ 已点赞 · 点击取消" : "♡ 点赞";
    $("like-count").textContent = status.like_count + " 人点赞";
  }
}
function resetLikeState() {
  ++state.likeSequence; state.likeStatus = null; state.likeBusy = false; state.likeLoading = false;
  $("like-message").textContent = ""; renderLikes();
}
function sameLikeTarget(id, token, sequence) {
  return state.detail && String(state.detail.id) === String(id) && state.token === token && state.likeSequence === sequence && $("detail-dialog").open;
}
async function loadLikeStatus() {
  if (!state.detail || !state.token) { renderLikes(); return; }
  const id = state.detail.id, token = state.token, sequence = ++state.likeSequence;
  state.likeLoading = true; $("like-message").textContent = ""; renderLikes();
  try {
    const status = await api("/videos/" + encodeURIComponent(id) + "/like", { auth: true });
    if (!sameLikeTarget(id, token, sequence)) return;
    if (!Number.isSafeInteger(status?.like_count) || status.like_count < 0 || typeof status.liked !== "boolean") throw new Error("点赞状态响应格式异常");
    state.likeStatus = status;
  } catch (error) {
    if (!sameLikeTarget(id, token, sequence)) return;
    state.likeStatus = null; $("like-message").textContent = error.message + "，可点击重试查询。";
  } finally {
    if (sameLikeTarget(id, token, sequence)) { state.likeLoading = false; renderLikes(); }
  }
}
$("refresh-likes").onclick = () => { if (!state.likeBusy && !state.likeLoading) return loadLikeStatus(); };
$("like-button").onclick = async () => {
  if (!state.detail) return;
  if (!state.token) { openAuth(); notify("登录后即可点赞"); return; }
  if (state.likeBusy || state.likeLoading) return;
  if (!state.likeStatus) { await loadLikeStatus(); return; }
  const id = state.detail.id, token = state.token, liked = state.likeStatus.liked, sequence = ++state.likeSequence;
  state.likeBusy = true; $("like-message").textContent = ""; renderLikes();
  try {
    await api("/videos/" + encodeURIComponent(id) + "/like", { method: liked ? "DELETE" : "PUT", auth: true });
    if (!sameLikeTarget(id, token, sequence)) return;
    state.likeBusy = false; await loadLikeStatus();
  } catch (error) {
    if (!sameLikeTarget(id, token, sequence)) return;
    state.likeStatus = null; $("like-message").textContent = error.message + "，请重新查询点赞状态后再操作。";
  } finally {
    if (sameLikeTarget(id, token, sequence)) { state.likeBusy = false; renderLikes(); }
  }
};
$("detail-player").onerror = () => { $("playback-message").textContent = "视频暂时无法播放。地址可能不可用，或浏览器不支持该视频编码。"; };
$("detail-dialog").addEventListener("close", () => { ++state.detailSequence; state.detail = null; resetLikeState(); resetComments(); $("detail-player").pause(); $("detail-player").removeAttribute("src"); $("detail-player").load(); });
$("delete-button").onclick = () => { $("delete-error").textContent = ""; $("confirm-dialog").showModal(); };
$("confirm-delete").onclick = async () => { if (!state.detail) return; $("confirm-delete").disabled = true; try { await api("/videos/" + state.detail.id, { method: "DELETE", auth: true }); $("confirm-dialog").close(); $("detail-dialog").close(); state.detail = null; if (state.videos.length === 1 && state.page > 1) state.page--; await loadVideos(); notify("视频记录已删除，已上传文件仍保留"); } catch (error) { $("delete-error").textContent = error.message; } finally { $("confirm-delete").disabled = false; } };

// 评论列表公开读取，写入和删除仍由后端 JWT 与作者条件控制。
function commentRow(comment) {
  const row = document.createElement("article"); row.className = "comment-item";
  const meta = document.createElement("div"); meta.className = "comment-meta";
  const author = document.createElement("strong");
  author.textContent = state.user && String(state.user.id) === String(comment.user_id) ? state.user.username + "（我）" : "用户 " + comment.user_id;
  const date = document.createElement("time"); date.textContent = formatDate(comment.created_at);
  meta.append(author, date);
  if (state.token && state.user && String(state.user.id) === String(comment.user_id)) {
    const button = document.createElement("button"); button.type = "button"; button.className = "text-button comment-delete"; button.textContent = "删除";
    button.disabled = state.commentBusy || state.commentLoading;
    button.onclick = () => {
      if (!state.detail || state.commentBusy || state.commentLoading) return;
      state.commentDeleteTarget = { id: comment.id, videoID: state.detail.id, token: state.token };
      $("comment-delete-error").textContent = ""; $("comment-delete-dialog").showModal();
    };
    meta.append(button);
  }
  const content = document.createElement("p"); content.className = "comment-text"; content.textContent = comment.content;
  row.append(meta, content); return row;
}
function renderComments() {
  const locked = !state.detail || state.commentBusy || state.commentLoading;
  $("comments-list").replaceChildren(...state.comments.map(commentRow));
  $("comments-count").textContent = state.commentError ? "暂不可用" : "本页 " + state.comments.length + " 条";
  $("comments-page").textContent = "第 " + state.commentPage + " 页";
  $("comments-prev").disabled = locked || state.commentPage <= 1;
  $("comments-next").disabled = locked || !state.commentHasNext;
  $("refresh-comments").disabled = locked;
  $("comment-content").disabled = !state.detail || state.commentBusy || !state.token;
  $("comment-submit").disabled = locked;
  $("comment-submit").textContent = state.commentBusy ? "正在处理……" : (state.token ? "发表评论" : "登录后发表评论");
  $("confirm-comment-delete").disabled = state.commentBusy;
  $("comments-list-message").textContent = state.commentLoading ? "正在加载评论……" : state.commentError || (state.comments.length ? "" : (state.commentPage === 1 ? "还没有评论，来发表第一条吧。" : "这一页暂无评论，可返回上一页。"));
  $("comment-counter").textContent = [...$("comment-content").value].length + " / 500 字符";
}
function closeCommentConfirmation() {
  state.commentDeleteTarget = null;
  if ($("comment-delete-dialog").open) $("comment-delete-dialog").close();
}
function resetComments() {
  ++state.commentSequence; ++state.commentMutation;
  state.commentPage = 1; state.comments = []; state.commentLoading = false; state.commentBusy = false; state.commentHasNext = false; state.commentError = "";
  $("comment-content").value = ""; $("comment-message").textContent = "";
  closeCommentConfirmation(); renderComments();
}
function syncCommentSession() {
  ++state.commentSequence; ++state.commentMutation;
  state.commentLoading = false; state.commentBusy = false;
  closeCommentConfirmation(); renderComments();
  if (state.detail && $("detail-dialog").open) loadComments();
}
function sameCommentTarget(id, sequence) {
  return state.detail && String(state.detail.id) === String(id) && state.commentSequence === sequence && $("detail-dialog").open;
}
function sameCommentMutation(id, token, mutation) {
  return state.detail && String(state.detail.id) === String(id) && state.token === token && state.commentMutation === mutation && $("detail-dialog").open;
}
async function loadComments() {
  if (!state.detail) return;
  const id = state.detail.id, page = state.commentPage, sequence = ++state.commentSequence;
  state.commentLoading = true; state.commentError = ""; state.comments = []; renderComments();
  try {
    const data = await api("/videos/" + encodeURIComponent(id) + "/comments?page=" + page + "&page_size=" + state.commentSize);
    if (!sameCommentTarget(id, sequence)) return;
    if (!Array.isArray(data?.comments) || String(data.video_id) !== String(id) || data.page !== page || data.page_size !== state.commentSize) throw new Error("评论列表响应格式异常");
    state.comments = data.comments;
    state.commentHasNext = data.comments.length === state.commentSize && page < 10000;
  } catch (error) {
    if (!sameCommentTarget(id, sequence)) return;
    state.commentError = error.message + "，可点击刷新评论重试。"; state.commentHasNext = false;
  } finally {
    if (sameCommentTarget(id, sequence)) { state.commentLoading = false; renderComments(); }
  }
}
$("comment-content").oninput = () => { $("comment-counter").textContent = [...$("comment-content").value].length + " / 500 字符"; };
$("refresh-comments").onclick = () => { if (!state.commentBusy && !state.commentLoading) return loadComments(); };
$("comments-prev").onclick = () => { if (!state.commentBusy && !state.commentLoading && state.commentPage > 1) { --state.commentPage; return loadComments(); } };
$("comments-next").onclick = () => { if (!state.commentBusy && !state.commentLoading && state.commentHasNext) { ++state.commentPage; return loadComments(); } };
$("comment-form").onsubmit = async (event) => {
  event.preventDefault(); if (!state.detail || state.commentBusy || state.commentLoading) return;
  if (!state.token) { openAuth(); return; }
  const draft = $("comment-content").value, content = draft.trim();
  if (!content || [...content].length > 500) { $("comment-message").textContent = "评论内容必须为 1～500 个字符"; return; }
  const id = state.detail.id, token = state.token, mutation = ++state.commentMutation;
  state.commentBusy = true; $("comment-message").textContent = ""; renderComments();
  try {
    await api("/videos/" + encodeURIComponent(id) + "/comments", { method: "POST", body: { content }, auth: true });
    if (!sameCommentMutation(id, token, mutation)) return;
    if ($("comment-content").value === draft) $("comment-content").value = "";
    state.commentPage = 1; await loadComments(); notify("评论已发表");
  } catch (error) {
    if (sameCommentMutation(id, token, mutation)) $("comment-message").textContent = error.message + "。输入已保留，可先刷新列表确认是否已发送，避免重复提交。";
  } finally {
    if (sameCommentMutation(id, token, mutation)) { state.commentBusy = false; renderComments(); }
  }
};
$("confirm-comment-delete").onclick = async () => {
  const target = state.commentDeleteTarget;
  if (!target || state.commentBusy || !state.detail || String(target.videoID) !== String(state.detail.id) || target.token !== state.token) return;
  const id = target.videoID, token = state.token, mutation = ++state.commentMutation;
  state.commentBusy = true; $("comment-delete-error").textContent = ""; renderComments();
  try {
    await api("/comments/" + encodeURIComponent(target.id), { method: "DELETE", auth: true });
    if (!sameCommentMutation(id, token, mutation)) return;
    closeCommentConfirmation();
    if (state.comments.length === 1 && state.commentPage > 1) --state.commentPage;
    await loadComments(); notify("评论已删除");
  } catch (error) {
    if (sameCommentMutation(id, token, mutation)) $("comment-delete-error").textContent = error.message;
  } finally {
    if (sameCommentMutation(id, token, mutation)) { state.commentBusy = false; renderComments(); }
  }
};
$("comment-delete-dialog").addEventListener("close", () => { state.commentDeleteTarget = null; });

// 上传和发布分两步。元数据发布失败时，保留上传结果供重试。
function openPublish() { if (!state.token) { openAuth(); notify("登录后即可发布视频"); return; } $("publish-message").textContent = ""; if (!$("publish-dialog").open) $("publish-dialog").showModal(); }
for (const id of ["publish-button", "nav-publish", "strip-publish"]) $(id).onclick = openPublish;
function source(mode) { if (state.busy) return; state.source = mode; $("source-file").classList.toggle("selected", mode === "file"); $("source-url").classList.toggle("selected", mode === "url"); $("file-section").hidden = mode !== "file"; $("url-section").hidden = mode !== "url"; $("play-url").required = mode === "url"; $("publish-submit").textContent = mode === "file" ? "上传并发布" : "发布视频"; }
$("source-file").onclick = () => source("file"); $("source-url").onclick = () => source("url");
function chooseFile(file) {
  if (state.busy || !file) return; state.file = null; state.uploaded = null; if (state.preview) URL.revokeObjectURL(state.preview); state.preview = ""; $("upload-preview").hidden = true; $("upload-preview").removeAttribute("src");
  if (!/\.mp4$/i.test(file.name) || !file.size || file.size > 100 * 1024 * 1024) { $("publish-message").textContent = "请选择非空的 MP4 文件，且不超过 100 MiB"; $("file-label").textContent = "重新选择视频文件"; $("video-file").value = ""; return; }
  state.file = file; $("file-label").textContent = file.name + " · " + (file.size / 1024 / 1024).toFixed(2) + " MiB"; $("publish-message").textContent = ""; state.preview = URL.createObjectURL(file); $("upload-preview").src = state.preview; $("upload-preview").hidden = false; if (!$("video-title").value) $("video-title").value = file.name.replace(/\.mp4$/i, "").slice(0, 100);
}
$("video-file").onchange = () => chooseFile($("video-file").files[0]);
for (const event of ["dragover", "dragleave", "drop"]) $("drop-zone").addEventListener(event, (e) => { e.preventDefault(); $("drop-zone").classList.toggle("dragover", event === "dragover"); if (event === "drop") chooseFile(e.dataTransfer.files[0]); });
function resetCover() {
  state.coverFile = null; state.uploadedCover = null;
  if (state.coverPreview) URL.revokeObjectURL(state.coverPreview);
  state.coverPreview = "";
  $("cover-file").value = "";
  $("cover-preview").hidden = true; $("cover-preview").removeAttribute("src");
  $("remove-cover").hidden = true;
  $("cover-file-label").textContent = "未选择，使用默认封面。";
}
function chooseCover(file) {
  if (state.busy || !file) return;
  resetCover();
  if (!/\.(jpe?g|png)$/i.test(file.name) || !file.size || file.size > 5 * 1024 * 1024 || (file.type && !["image/jpeg", "image/png"].includes(file.type))) {
    $("publish-message").textContent = "请选择非空的 JPEG 或 PNG 图片，且不超过 5 MiB"; return;
  }
  state.coverFile = file;
  const preview = URL.createObjectURL(file); state.coverPreview = preview;
  $("cover-preview").onerror = () => {
    if (state.coverPreview !== preview || state.busy) return;
    resetCover(); $("publish-message").textContent = "图片无法预览，请重新选择有效的 JPEG 或 PNG 图片";
  };
  $("cover-preview").src = preview; $("cover-preview").hidden = false; $("remove-cover").hidden = false;
  $("cover-file-label").textContent = file.name + " · " + (file.size / 1024 / 1024).toFixed(2) + " MiB";
  $("publish-message").textContent = "";
}
$("cover-file").onchange = () => chooseCover($("cover-file").files[0]);
$("remove-cover").onclick = () => { if (!state.busy) { resetCover(); $("publish-message").textContent = ""; } };

$("publish-form").onsubmit = async (event) => {
  event.preventDefault(); if (state.busy) return;
  if (!state.token) { $("publish-message").textContent = "请先登录后再发布"; return; }
  const title = $("video-title").value.trim();
  if (!title || [...title].length > 100) { $("publish-message").textContent = "请输入 1～100 个字符的标题"; return; }
  if (state.source === "url" && !/^\/media\/videos\/[^/\\?#%]+$/.test($("play-url").value.trim())) {
    $("publish-message").textContent = "请填写本人上传接口返回的 /media/videos/ 视频地址"; return;
  }
  if (state.source === "file" && !state.file) { $("publish-message").textContent = "请先选择视频文件"; return; }
  const token = state.token;
  const checkSession = () => { if (state.token !== token) throw new Error("登录状态已变化，请重新登录后发布"); };
  state.busy = true; for (const el of $("publish-form").elements) el.disabled = true;
  try {
    let playURL = $("play-url").value.trim();
    if (state.source === "file") {
      if (!state.uploaded) {
        $("publish-message").textContent = "正在上传视频，请稍候……";
        const form = new FormData(); form.append("file", state.file);
        const uploaded = await api("/uploads/videos", { method: "POST", body: form, form: true, auth: true });
        checkSession();
        if (!uploaded?.play_url) throw new Error("上传响应缺少视频地址，请重试");
        state.uploaded = uploaded;
      }
      playURL = state.uploaded.play_url;
    }
    let coverURL = "/cover.svg";
    if (state.coverFile) {
      if (!state.uploadedCover) {
        $("publish-message").textContent = "正在上传封面，请稍候……";
        const form = new FormData(); form.append("file", state.coverFile);
        const uploaded = await api("/uploads/covers", { method: "POST", body: form, form: true, auth: true });
        checkSession();
        if (!uploaded?.cover_url) throw new Error("上传响应缺少封面地址，请重试");
        state.uploadedCover = uploaded;
      }
      coverURL = state.uploadedCover.cover_url;
    }
    checkSession();
    $("publish-message").textContent = "正在发布视频信息……";
    await api("/videos", { method: "POST", body: { title, play_url: playURL, cover_url: coverURL }, auth: true });
    checkSession();
    $("publish-dialog").close(); $("publish-form").reset();
    state.file = null; state.uploaded = null; resetCover();
    if (state.preview) URL.revokeObjectURL(state.preview);
    state.preview = ""; $("upload-preview").hidden = true; $("upload-preview").removeAttribute("src");
    $("file-label").textContent = "点击选择，或拖入视频文件";
    state.page = 1; await loadVideos(); notify("视频已发布，去看看你的新片段吧");
  } catch (error) {
    const retained = [state.uploaded ? "视频" : "", state.uploadedCover ? "封面" : ""].filter(Boolean).join("和");
    $("publish-message").textContent = error.message + (retained ? "。已上传的" + retained + "会在本次登录中复用，可重试。" : "");
  } finally {
    state.busy = false; for (const el of $("publish-form").elements) el.disabled = false;
  }
};
$("publish-dialog").addEventListener("cancel", (e) => { if (state.busy) e.preventDefault(); }); $("publish-dialog").addEventListener("close", () => $("upload-preview").pause());
document.querySelectorAll("[data-close]").forEach((button) => { button.onclick = () => { if (button.dataset.close === "publish-dialog" && state.busy) { notify("正在处理，请稍候"); return; } $(button.dataset.close).close(); }; });
loadVideos();
