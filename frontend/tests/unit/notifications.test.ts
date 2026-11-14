import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useSessionStore } from '../../src/stores/session'
import { useNotificationStore, POLL_INTERVAL, SILENCE_LIMIT } from '../../src/stores/notifications'
import type { NotificationItem } from '../../src/types'
import { eventStream, type EventStream } from './event-stream'

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
  it('polls every minute when the server has no stream, and stops after logout', async () => {
    let count = 2
    const calls = route({ 'GET /notifications/unread-count': () => json({ count }), 'GET /notifications/stream': () => json({ error: 'not found' }, 404) })
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    notifications.watchSession(true)
    await vi.waitFor(() => expect(notifications.unread).toBe(2))
    const counts = () => calls.filter(call => call.path === '/notifications/unread-count')
    expect(counts()[0]).toEqual({ path: '/notifications/unread-count', method: 'GET', auth: 'Bearer alice-credential' })
    count = 5
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL)
    expect(notifications.unread).toBe(5)
    expect(counts()).toHaveLength(2)
    // 404: this server has no push, so the stream is tried once and then only polling remains.
    expect(calls.filter(call => call.path === '/notifications/stream')).toHaveLength(1)
    expect(notifications.live).toBe(false)
    notifications.watchSession(false)
    expect(notifications.unread).toBe(0)
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL * 3)
    expect(calls).toHaveLength(3)
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
  it('checks once more after a check that was already running when asked', async () => {
    const first = deferred<Response>()
    let answers = 0
    vi.stubGlobal('fetch', vi.fn(async () => ++answers === 1 ? first.promise : json({ count: 2 })))
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    const running = notifications.refreshCount()
    const asked = notifications.refreshCount() // a hint arrives meanwhile: the running answer may be old
    first.resolve(json({ count: 1 }))
    await Promise.all([running, asked])
    expect(answers).toBe(2)
    expect(notifications.unread).toBe(2)
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

// pushServer answers stream requests with a new open event stream each, unless told to refuse.
function pushServer() {
  const streams: EventStream[] = []
  let refusal: ((init: RequestInit) => Response) | null = null
  return {
    streams,
    get latest() { return streams[streams.length - 1] },
    refuse(answer: ((init: RequestInit) => Response) | null) { refusal = answer },
    handler(init: RequestInit) {
      if (refusal) return refusal(init)
      const stream = eventStream(init.signal)
      streams.push(stream)
      return stream.response
    },
  }
}
const hint = 'event: notification\ndata: {}\n\n'
// settle lets promises run without moving the fake clock.
const settle = async () => { for (let i = 0; i < 5; i++) await vi.advanceTimersByTimeAsync(0) }

describe('push', () => {
  afterEach(() => { vi.restoreAllMocks() })
  function signIn(handlers: Record<string, (init: RequestInit) => Response>) {
    const calls = route(handlers)
    const session = useSessionStore(); session.setSession(login())
    const notifications = useNotificationStore()
    notifications.watchSession(true)
    const count = (path: string) => calls.filter(call => call.path === path).length
    return { calls, session, notifications, counts: () => count('/notifications/unread-count'), attempts: () => count('/notifications/stream') }
  }

  it('stops polling while the stream is open and checks the count on each hint', async () => {
    let count = 1
    const push = pushServer()
    const { calls, notifications, counts } = signIn({ 'GET /notifications/unread-count': () => json({ count }), 'GET /notifications/stream': init => push.handler(init) })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    expect(calls.find(call => call.path === '/notifications/stream')?.auth).toBe('Bearer alice-credential')
    await settle()
    expect(notifications.unread).toBe(1)
    const opened = counts()
    expect(opened).toBeLessThanOrEqual(2) // the login's check and the one on opening, merged when they overlap
    for (let i = 0; i < 4; i++) { push.latest.send(': ping\n\n'); await vi.advanceTimersByTimeAsync(25_000) }
    expect(counts()).toBe(opened) // 100 seconds open: no polling
    count = 3
    push.latest.send(hint)
    await vi.waitFor(() => expect(notifications.unread).toBe(3))
    await settle()
    const before = counts()
    push.latest.send(hint.repeat(10)) // ten likes at once
    await settle()
    expect(counts() - before).toBeGreaterThanOrEqual(1)
    expect(counts() - before).toBeLessThanOrEqual(2) // one check in flight, one more after it
    expect(push.streams).toHaveLength(1)
  })

  it('polls while the stream is down and reconnects after growing, jittered delays', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5) // each delay is 0.75 of the current step
    let count = 1
    const push = pushServer()
    const { notifications, counts, attempts } = signIn({ 'GET /notifications/unread-count': () => json({ count }), 'GET /notifications/stream': init => push.handler(init) })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    push.refuse(() => json({ error: '服务正在关闭' }, 503))
    push.latest.end() // the server restarts
    await settle()
    expect(notifications.live).toBe(false)
    let tried = attempts()
    for (const wait of [750, 1500, 3000, 6000, 12000, 22500, 22500]) { // 1, 2, 4, 8, 16, 30, 30 seconds
      await vi.advanceTimersByTimeAsync(wait - 1)
      expect(attempts()).toBe(tried)
      await vi.advanceTimersByTimeAsync(1)
      expect(attempts()).toBe(++tried)
    }
    // Meanwhile the count is polled: 68.25 seconds have passed.
    count = 4
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL)
    expect(notifications.unread).toBe(4)
    // The server is back: the stream opens, the count is checked at once, and polling stops.
    push.refuse(null)
    count = 6
    await vi.advanceTimersByTimeAsync(22500)
    await settle()
    expect(notifications.live).toBe(true)
    expect(notifications.unread).toBe(6)
    const polled = counts()
    push.latest.send(': ping\n\n')
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL - 1000)
    expect(counts()).toBe(polled)
  })

  it('starts the delays over once a stream has stayed open for 30 seconds', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const push = pushServer()
    const { notifications, attempts } = signIn({ 'GET /notifications/unread-count': () => json({ count: 0 }), 'GET /notifications/stream': init => push.handler(init) })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    push.latest.end() // a quick drop: the next wait grows to 2 seconds
    await settle()
    await vi.advanceTimersByTimeAsync(750)
    await settle()
    expect(notifications.live).toBe(true)
    push.latest.send(': ping\n\n')
    await vi.advanceTimersByTimeAsync(30_000)
    push.latest.end() // after 30 seconds: back to about 1 second
    await settle()
    const tried = attempts()
    await vi.advanceTimersByTimeAsync(749)
    expect(attempts()).toBe(tried)
    await vi.advanceTimersByTimeAsync(1)
    expect(attempts()).toBe(tried + 1)
  })

  it('renews the token once on 401 and reconnects with the new one', async () => {
    const push = pushServer()
    const { calls, session, notifications } = signIn({
      'GET /notifications/unread-count': () => json({ count: 0 }),
      'POST /auth/refresh': () => json({ id: 1, username: 'alice', token: 'alice-renewed' }),
      'GET /notifications/stream': init => new Headers(init.headers).get('Authorization') === 'Bearer alice-renewed'
        ? push.handler(init) : json({ error: '登录凭证无效或已过期' }, 401),
    })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    expect(session.token).toBe('alice-renewed')
    expect(calls.filter(call => call.path === '/notifications/stream').map(call => call.auth)).toEqual(['Bearer alice-credential', 'Bearer alice-renewed'])
    expect(calls.filter(call => call.path === '/auth/refresh')).toHaveLength(1)
  })

  it('does not keep renewing when the new token is refused too', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const { calls, notifications, attempts } = signIn({
      'GET /notifications/unread-count': () => json({ count: 0 }),
      'POST /auth/refresh': () => json({ id: 1, username: 'alice', token: 'alice-renewed' }),
      'GET /notifications/stream': () => json({ error: '登录凭证无效或已过期' }, 401),
    })
    await settle()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(calls.filter(call => call.path === '/auth/refresh')).toHaveLength(1)
    expect(attempts()).toBeGreaterThan(3) // still retrying, slowly
    expect(notifications.live).toBe(false)
  })

  it('ends the stream and everything else when the user signs out', async () => {
    const push = pushServer()
    const { calls, notifications } = signIn({ 'GET /notifications/unread-count': () => json({ count: 2 }), 'GET /notifications/stream': init => push.handler(init) })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    notifications.watchSession(false)
    expect(push.latest.open).toBe(false) // the request was aborted
    expect(notifications.live).toBe(false)
    const made = calls.length
    await vi.advanceTimersByTimeAsync(5 * POLL_INTERVAL)
    expect(calls).toHaveLength(made)
  })

  it('drops a stream that has been silent for 60 seconds and opens a new one', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    const push = pushServer()
    const { notifications } = signIn({ 'GET /notifications/unread-count': () => json({ count: 0 }), 'GET /notifications/stream': init => push.handler(init) })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    const first = push.latest
    first.send(': ping\n\n') // the last bytes for a while
    await settle()
    await vi.advanceTimersByTimeAsync(SILENCE_LIMIT - 1)
    expect(first.open).toBe(true)
    await vi.advanceTimersByTimeAsync(1)
    expect(first.open).toBe(false)
    await settle()
    expect(notifications.live).toBe(false)
    await vi.advanceTimersByTimeAsync(750)
    await settle()
    expect(push.streams).toHaveLength(2)
    expect(notifications.live).toBe(true)
  })

  it('does not open a stream for a page that is hidden at login', async () => {
    const page = Object.assign(new EventTarget(), { visibilityState: 'hidden' })
    vi.stubGlobal('document', page)
    const push = pushServer()
    const { notifications, attempts } = signIn({ 'GET /notifications/unread-count': () => json({ count: 0 }), 'GET /notifications/stream': init => push.handler(init) })
    await settle()
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL)
    expect(attempts()).toBe(0)
    page.visibilityState = 'visible'; page.dispatchEvent(new Event('visibilitychange'))
    await settle()
    expect(attempts()).toBe(1)
    expect(notifications.live).toBe(true)
  })

  it('leaves the stream while the page is hidden, polls, and reconnects when it is visible again', async () => {
    const page = Object.assign(new EventTarget(), { visibilityState: 'visible' })
    vi.stubGlobal('document', page)
    const show = (state: 'visible' | 'hidden') => { page.visibilityState = state; page.dispatchEvent(new Event('visibilitychange')) }
    let count = 1
    const push = pushServer()
    const { notifications, counts, attempts } = signIn({ 'GET /notifications/unread-count': () => json({ count }), 'GET /notifications/stream': init => push.handler(init) })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    show('hidden')
    expect(push.latest.open).toBe(false)
    expect(notifications.live).toBe(false)
    count = 2
    await vi.advanceTimersByTimeAsync(5 * POLL_INTERVAL)
    expect(notifications.unread).toBe(2)
    expect(attempts()).toBe(1) // no stream while hidden
    const polled = counts()
    show('visible')
    await settle()
    expect(attempts()).toBe(2)
    expect(notifications.live).toBe(true)
    expect(counts()).toBe(polled + 1) // checked once on reconnecting
  })

  it('marks a loaded list stale when a hint brings a new notification', async () => {
    let count = 1
    const push = pushServer()
    const { notifications } = signIn({
      'GET /notifications/unread-count': () => json({ count }),
      'GET /notifications/stream': init => push.handler(init),
      'GET /notifications?': () => json(page([5])),
    })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    count = 2
    push.latest.send(hint) // new, but no list has been loaded that could be out of date
    await vi.waitFor(() => expect(notifications.unread).toBe(2))
    await settle()
    expect(notifications.stale).toBe(false)
    count = 1
    await notifications.load()
    await settle()
    push.latest.send(hint) // a hint without a new notification, such as after Redis reconnects
    await settle()
    expect(notifications.stale).toBe(false)
    count = 2
    push.latest.send(hint)
    await vi.waitFor(() => expect(notifications.stale).toBe(true))
    await notifications.load()
    expect(notifications.stale).toBe(false)
  })

  it('marks a loaded list stale when reconnecting finds a notification missed meanwhile', async () => {
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    let count = 1
    const push = pushServer()
    const { notifications } = signIn({
      'GET /notifications/unread-count': () => json({ count }),
      'GET /notifications/stream': init => push.handler(init),
      'GET /notifications?': () => json(page([5])),
    })
    await vi.waitFor(() => expect(notifications.live).toBe(true))
    await notifications.load()
    await settle()
    push.latest.end() // the API restarts, and a like arrives before the page is back
    count = 2
    await settle()
    expect(notifications.stale).toBe(false)
    await vi.advanceTimersByTimeAsync(750)
    await settle()
    expect(notifications.live).toBe(true)
    expect(notifications.unread).toBe(2)
    expect(notifications.stale).toBe(true)
  })
})
