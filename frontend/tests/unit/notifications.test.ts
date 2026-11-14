import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useSessionStore } from '../../src/stores/session'
import { useNotificationStore, POLL_INTERVAL } from '../../src/stores/notifications'
import type { NotificationItem } from '../../src/types'

const login = (name = 'alice') => ({ id: name === 'alice' ? 1 : 2, username: name, token: name + '-credential' })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const empty = () => new Response(null, { status: 204 })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
const note = (id: number, read = false): NotificationItem => ({
  id, type: 'like.created', actor_id: 7, actor_name: 'bob', video_id: 42, video_title: '视频',
  comment_id: null, comment_content: null, created_at: '2026-11-14T10:00:00+08:00', read,
})
const page = (ids: number[], next_cursor = '', read = false) => ({ notifications: ids.map(id => note(id, read)), next_cursor, has_more: next_cursor !== '' })
// route answers each request by its path and method, and records it.
function route(handlers: Record<string, (init: RequestInit) => Response>) {
  const calls: { path: string; method: string; auth: string | null }[] = []
  const fetch = vi.fn(async (path: string, init: RequestInit = {}) => {
    const method = init.method || 'GET'
    calls.push({ path, method, auth: new Headers(init.headers).get('Authorization') })
    const key = Object.keys(handlers).find(prefix => `${method} ${path}`.startsWith(prefix))
    if (!key) throw new Error(`unexpected ${method} ${path}`)
    return handlers[key](init)
  })
  vi.stubGlobal('fetch', fetch)
  return calls
}
beforeEach(() => { setActivePinia(createPinia()); vi.useFakeTimers() })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('unread count', () => {
  it('starts polling every minute after login and stops after logout', async () => {
    let count = 2
    const calls = route({ 'GET /notifications/unread-count': () => json({ count }) })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    notifications.watchSession(true)
    await vi.waitFor(() => expect(notifications.unread).toBe(2))
    expect(calls[0]).toEqual({ path: '/notifications/unread-count', method: 'GET', auth: 'Bearer alice-credential' })
    count = 5
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL)
    expect(notifications.unread).toBe(5)
    expect(calls).toHaveLength(2)
    notifications.watchSession(false)
    expect(notifications.unread).toBe(0)
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL * 3)
    expect(calls).toHaveLength(2)
  })
  it('keeps the last count when a check fails', async () => {
    let fail = false
    route({ 'GET /notifications/unread-count': () => fail ? json({ error: '暂时失败' }, 500) : json({ count: 3 }) })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    await notifications.refreshCount()
    fail = true
    await notifications.refreshCount()
    expect(notifications.unread).toBe(3)
  })
  it('drops a count that arrives after the account changed', async () => {
    const pending = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn().mockReturnValueOnce(pending.promise))
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    const check = notifications.refreshCount()
    session.setSession(login('bob')); notifications.watchSession(false)
    pending.resolve(json({ count: 9 })); await check
    expect(notifications.unread).toBe(0)
  })
})

describe('notification list', () => {
  it('reads the first page with the token, appends with the cursor and drops duplicates', async () => {
    const calls = route({
      'GET /notifications/unread-count': () => json({ count: 4 }),
      'GET /notifications?limit=20&cursor=8': () => json(page([8, 7, 6])),
      'GET /notifications?limit=20': () => json(page([10, 9, 8], '8')),
    })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    await notifications.load()
    expect(notifications.items.map(item => item.id)).toEqual([10, 9, 8])
    expect(notifications.hasMore).toBe(true)
    await notifications.load(true)
    expect(notifications.items.map(item => item.id)).toEqual([10, 9, 8, 7, 6])
    expect(notifications.hasMore).toBe(false)
    await notifications.load(true) // nothing more to read: no request
    const lists = calls.filter(call => call.path.startsWith('/notifications?'))
    expect(lists.map(call => call.path)).toEqual(['/notifications?limit=20', '/notifications?limit=20&cursor=8'])
    expect(lists.every(call => call.auth === 'Bearer alice-credential')).toBe(true)
    expect(notifications.unread).toBe(4) // the first page refreshes the count too
  })
  it('keeps the items and reports a malformed or failed page', async () => {
    let answer = () => json(page([3, 2], '2'))
    route({ 'GET /notifications/unread-count': () => json({ count: 0 }), 'GET /notifications?': () => answer() })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    await notifications.load()
    answer = () => json({ error: '暂时失败' }, 500)
    await notifications.load(true)
    expect(notifications.error).toBe('暂时失败')
    expect(notifications.items.map(item => item.id)).toEqual([3, 2])
    expect(notifications.nextCursor).toBe('2')
    answer = () => json({ notifications: null })
    await notifications.load()
    expect(notifications.error).toBe('通知列表响应格式异常')
  })
  it('ignores a page that arrives after the account changed', async () => {
    const pending = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn(async (path: string) => path.startsWith('/notifications?') ? pending.promise : json({ count: 0 })))
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    const loading = notifications.load()
    session.setSession(login('bob')); notifications.watchSession(true)
    pending.resolve(json(page([5]))); await loading
    expect(notifications.items).toEqual([])
    expect(notifications.loading).toBe(false)
  })
})

describe('marking read', () => {
  it('marks one notification, once, and lowers the count', async () => {
    let count = 2
    const calls = route({
      'GET /notifications/unread-count': () => json({ count }),
      'PUT /notifications/9/read': () => { count = 1; return empty() },
      'GET /notifications?': () => json(page([9, 8])),
    })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    await notifications.load()
    await vi.waitFor(() => expect(notifications.unread).toBe(2))
    const item = notifications.items[0]
    await notifications.markRead(item)
    expect(notifications.items[0].read).toBe(true)
    expect(notifications.unread).toBe(1)
    await notifications.markRead(notifications.items[0]) // already read: no second request
    expect(calls.filter(call => call.method === 'PUT')).toEqual([{ path: '/notifications/9/read', method: 'PUT', auth: 'Bearer alice-credential' }])
  })
  it('a failed mark leaves the notification unread', async () => {
    route({
      'GET /notifications/unread-count': () => json({ count: 1 }),
      'PUT /notifications/9/read': () => json({ error: '通知不存在' }, 404),
      'GET /notifications?': () => json(page([9])),
    })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    await notifications.load()
    await expect(notifications.markRead(notifications.items[0])).rejects.toMatchObject({ status: 404 })
    expect(notifications.items[0].read).toBe(false)
  })
  it('marks all up to the newest loaded notification', async () => {
    let count = 3
    const calls = route({
      'GET /notifications/unread-count': () => json({ count }),
      'PUT /notifications/read?up_to=12': () => { count = 1; return empty() }, // 13 arrived after the page loaded
      'GET /notifications?': () => json(page([12, 11, 7])),
    })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    await notifications.load()
    await notifications.markAllRead()
    expect(calls.filter(call => call.method === 'PUT').map(call => call.path)).toEqual(['/notifications/read?up_to=12'])
    expect(notifications.items.every(item => item.read)).toBe(true)
    expect(notifications.unread).toBe(1)
  })
  it('does nothing without notifications', async () => {
    const calls = route({})
    const session = useSessionStore(); session.setSession(login())
    await useNotificationStore().markAllRead()
    expect(calls).toEqual([])
  })
})
