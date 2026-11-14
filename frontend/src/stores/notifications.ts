import { defineStore } from 'pinia'
import { ref } from 'vue'
import { errorMessage } from '../api/client'
import type { NotificationItem, NotificationPage, UnreadCount } from '../types'
import { useSessionStore } from './session'

// 登录后每 60 秒查一次未读数；第五步会改成服务端推送。
export const POLL_INTERVAL = 60_000
const PAGE_SIZE = 20

export const useNotificationStore = defineStore('notifications', () => {
  const session = useSessionStore()
  const items = ref<NotificationItem[]>([])
  const unread = ref(0)
  const nextCursor = ref('')
  const hasMore = ref(false)
  const loading = ref(false)
  const error = ref('')
  let sequence = 0
  let timer: ReturnType<typeof setInterval> | undefined

  // watchSession 在登录、退出、换账号时调用：清掉上一个账号的通知，登录时开始定时查未读数。
  function watchSession(signedIn: boolean) {
    clearInterval(timer)
    timer = undefined
    sequence++
    items.value = []; nextCursor.value = ''; hasMore.value = false
    loading.value = false; error.value = ''; unread.value = 0
    if (!signedIn) return
    void refreshCount()
    timer = setInterval(() => { void refreshCount() }, POLL_INTERVAL)
  }

  // refreshCount 失败时保留原来的数字：未读数只是提示，下次再查。
  async function refreshCount() {
    const user = session.user?.id
    if (!user) return
    try {
      const result = await session.call<UnreadCount>('/notifications/unread-count', {}, 'required')
      if (user === session.user?.id && typeof result?.count === 'number') unread.value = result.count
    } catch { /* 下次再查 */ }
  }

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

  return { items, unread, nextCursor, hasMore, loading, error, watchSession, refreshCount, load, markRead, markAllRead }
})
