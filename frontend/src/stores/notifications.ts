import { defineStore } from 'pinia'
import { ref } from 'vue'
import { errorMessage } from '../api/client'
import { readStream } from '../api/stream'
import type { NotificationItem, NotificationPage, UnreadCount } from '../types'
import { useSessionStore } from './session'

// 推送连着时不轮询，收到提示才查未读数；推送没连上（正在重连、页面在后台、服务端没开推送）时每 60 秒查一次。
export const POLL_INTERVAL = 60_000
// 重连前等待：第一次约 1 秒，每失败一次翻倍，最多 30 秒；连上并持续 30 秒以后从头算。
export const RETRY_MIN = 1_000
export const RETRY_MAX = 30_000
const STABLE_AFTER = 30_000
// 服务端每 25 秒写一次心跳：60 秒什么都没收到，说明连接已经悄悄断了，主动断开重连。
export const SILENCE_LIMIT = 60_000
const PAGE_SIZE = 20

export const useNotificationStore = defineStore('notifications', () => {
  const session = useSessionStore()
  const items = ref<NotificationItem[]>([])
  const unread = ref(0)
  const nextCursor = ref('')
  const hasMore = ref(false)
  const loading = ref(false)
  const error = ref('')
  const live = ref(false) // 推送连着
  const stale = ref(false) // 列表加载以后又来了新通知：通知页提示刷新
  let sequence = 0
  let listed = false // 这次登录加载过通知列表
  let poller: ReturnType<typeof setInterval> | undefined
  let counting: Promise<void> | null = null
  let countAgain = false
  // 推送连接
  let stream: AbortController | null = null
  let retryTimer: ReturnType<typeof setTimeout> | undefined
  let silenceTimer: ReturnType<typeof setTimeout> | undefined
  let retryDelay = RETRY_MIN
  let unsupported = false // 服务端没开推送（404）：这次登录不再尝试，只轮询
  let refreshedForStream = false // 推送收到 401 后已经换过一次 token

  // watchSession 在登录、退出、换账号时调用：清掉上一个账号的通知和连接；登录时查一次未读数、开始轮询并连接推送。
  function watchSession(signedIn: boolean) {
    stopStream(); stopPolling()
    sequence++
    items.value = []; nextCursor.value = ''; hasMore.value = false
    loading.value = false; error.value = ''; unread.value = 0; stale.value = false; listed = false
    retryDelay = RETRY_MIN; unsupported = false; refreshedForStream = false
    if (!signedIn) return
    void refreshCount()
    startPolling()
    void connect()
  }

  function startPolling() { poller ??= setInterval(() => { void checkForNew() }, POLL_INTERVAL) }
  function stopPolling() { clearInterval(poller); poller = undefined }

  // refreshCount 查一次未读数。正在查时又被调用（比如短时间来了很多提示），只在这次查完后再补查一次。
  // 失败时保留原来的数字：未读数只是提示，下次再查。
  function refreshCount(): Promise<void> {
    if (counting) { countAgain = true; return counting }
    counting = (async () => {
      do { countAgain = false; await fetchCount() } while (countAgain)
    })().finally(() => { counting = null })
    return counting
  }
  async function fetchCount() {
    const user = session.user?.id
    if (!user) return
    try {
      const result = await session.call<UnreadCount>('/notifications/unread-count', {}, 'required')
      if (user === session.user?.id && typeof result?.count === 'number') unread.value = result.count
    } catch { /* 下次再查 */ }
  }

  // checkForNew 在轮询和收到推送提示时调用：未读数变多，说明来了新通知；
  // 加载过列表的话提示用户刷新，不自动刷新，免得正在看的内容跳走。
  async function checkForNew() {
    const before = unread.value
    await refreshCount()
    if (listed && unread.value > before) stale.value = true
  }

  const hidden = () => typeof document !== 'undefined' && document.visibilityState === 'hidden'

  // connect 打开推送连接；已经连着、服务端没开推送、页面在后台时不连。连着的时候停止轮询；
  // 断开后退回轮询，再按退避时间重连。
  async function connect() {
    clearTimeout(retryTimer); retryTimer = undefined
    const user = session.user?.id, token = session.token
    if (stream || unsupported || !user || !token || hidden()) return
    const controller = new AbortController()
    stream = controller
    let openedAt = 0
    const watchSilence = () => {
      clearTimeout(silenceTimer)
      silenceTimer = setTimeout(() => controller.abort(), SILENCE_LIMIT)
    }
    const end = await readStream('/notifications/stream', token, controller.signal, {
      onOpen() {
        openedAt = Date.now(); live.value = true; refreshedForStream = false
        stopPolling(); watchSilence()
        void checkForNew() // 没连上的这段时间可能错过了提示
      },
      onEvent(event) { if (event.type === 'notification') void checkForNew() },
      onData: watchSilence,
    })
    clearTimeout(silenceTimer)
    if (stream !== controller) return // 已经停掉了（退出登录、换账号、页面切到后台）
    stream = null; live.value = false
    if (user !== session.user?.id) return
    startPolling()
    if (end === 'unsupported') { unsupported = true; return }
    if (openedAt && Date.now() - openedAt >= STABLE_AFTER) retryDelay = RETRY_MIN
    if (end === 'unauthorized') {
      // token 过期或会话已结束：token 已经换过就直接重连；否则先换一次，换到了就重连。
      // 换来的 token 还是 401 时不再换，按退避时间重试，免得一直请求 /auth/refresh。
      if (token !== session.token) { void connect(); return }
      if (!refreshedForStream) {
        refreshedForStream = true
        if (await session.refresh() && user === session.user?.id) { void connect(); return }
      }
    }
    scheduleRetry()
  }

  function scheduleRetry() {
    clearTimeout(retryTimer)
    // 乘一个 0.5～1 的随机数：服务重启时，所有页面错开重连，不会同时涌过去。
    const delay = retryDelay * (0.5 + Math.random() / 2)
    retryDelay = Math.min(retryDelay * 2, RETRY_MAX)
    retryTimer = setTimeout(() => { retryTimer = undefined; void connect() }, delay)
  }

  function stopStream() {
    clearTimeout(retryTimer); retryTimer = undefined
    clearTimeout(silenceTimer)
    const current = stream
    stream = null
    current?.abort()
    live.value = false
  }

  // 页面切到后台时断开推送、改为轮询：HTTP/1.1 下浏览器对一个站点最多开 6 个连接，
  // 每个标签页都占一条推送的话，其他请求会排队卡住。切回来马上重连。
  function onVisibilityChange() {
    if (!session.user) return
    if (hidden()) { stopStream(); startPolling() } else { retryDelay = RETRY_MIN; void connect() }
  }
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibilityChange)

  // load 读第一页（append 为 false，同时刷新未读数）或者接着读下一页；按 id 去重。
  async function load(append = false) {
    if (append && (loading.value || !hasMore.value)) return
    const current = ++sequence, user = session.user?.id
    const fresh = () => current === sequence && user === session.user?.id
    const cursor = append ? nextCursor.value : ''
    loading.value = true; error.value = ''
    if (!append) void refreshCount()
    try {
      const query = new URLSearchParams({ limit: String(PAGE_SIZE), ...(cursor ? { cursor } : {}) })
      const page = await session.call<NotificationPage>(`/notifications?${query}`, {}, 'required')
      if (!fresh()) return
      if (!Array.isArray(page?.notifications) || typeof page.next_cursor !== 'string' || typeof page.has_more !== 'boolean') {
        throw new Error('通知列表响应格式异常')
      }
      const unique = new Map<number, NotificationItem>()
      for (const item of append ? [...items.value, ...page.notifications] : page.notifications) unique.set(item.id, item)
      items.value = [...unique.values()]
      nextCursor.value = page.next_cursor; hasMore.value = page.has_more
      if (!append) { listed = true; stale.value = false }
    } catch (cause) {
      if (fresh()) error.value = errorMessage(cause)
    } finally { if (fresh()) loading.value = false }
  }

  // markRead 标记一条已读；已经读过的不再发请求。
  async function markRead(item: NotificationItem) {
    if (item.read) return
    await session.call<void>(`/notifications/${item.id}/read`, { method: 'PUT' }, 'required')
    item.read = true
    unread.value = Math.max(0, unread.value - 1)
    void refreshCount()
  }

  // markAllRead 只标到页面上最新的那一条（up_to）：之后才到的通知用户还没看到，仍是未读。
  async function markAllRead() {
    const newest = items.value.reduce((max, item) => Math.max(max, item.id), 0)
    if (!newest) return
    await session.call<void>(`/notifications/read?up_to=${newest}`, { method: 'PUT' }, 'required')
    for (const item of items.value) if (item.id <= newest) item.read = true
    await refreshCount()
  }

  return { items, unread, nextCursor, hasMore, loading, error, live, stale, watchSession, refreshCount, load, markRead, markAllRead }
})
