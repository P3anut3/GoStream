import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useSessionStore } from '../../src/stores/session'

const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const empty = (status = 204) => new Response(null, { status })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
// jwt builds an unsigned token with the given expiry; the store only reads exp to plan refreshes.
const jwt = (expSeconds: number) => `x.${btoa(JSON.stringify({ exp: expSeconds })).replace(/=+$/, '')}.y`
const signedIn = (token: string) => ({ id: 1, username: 'alice', token })
const pathOf = (call: unknown[]) => String(call[0])
const authOf = (call: unknown[]) => ((call[1] as RequestInit).headers as Headers).get('Authorization')
beforeEach(() => { setActivePinia(createPinia()); vi.useFakeTimers() })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('restoring a login on page load', () => {
  it('uses the refresh cookie once and keeps the session in memory only', async () => {
    const fetch = vi.fn().mockResolvedValue(json(signedIn('restored-token')))
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore()
    await session.restore(); await session.restore()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(pathOf(fetch.mock.calls[0])).toBe('/auth/refresh'); expect((fetch.mock.calls[0][1] as RequestInit).method).toBe('POST')
    expect(session.user?.username).toBe('alice'); expect(session.token).toBe('restored-token')
  })
  it('stays a quiet guest when there is no valid cookie', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ error: '登录已过期，请重新登录' }, 401)))
    const session = useSessionStore()
    await session.restore()
    expect(session.user).toBeNull(); expect(session.notice).toBe('')
  })
})

describe('refreshing an expired access token', () => {
  it('refreshes once and retries the request with the new token', async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(json({ error: '登录凭证无效或已过期' }, 401))
      .mockResolvedValueOnce(json(signedIn('fresh-token')))
      .mockResolvedValueOnce(json({ id: 1, username: 'alice' }))
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(signedIn('stale-token'))
    await expect(session.call('/me', {}, 'required')).resolves.toEqual({ id: 1, username: 'alice' })
    expect(fetch.mock.calls.map(pathOf)).toEqual(['/me', '/auth/refresh', '/me'])
    expect(authOf(fetch.mock.calls[0])).toBe('Bearer stale-token'); expect(authOf(fetch.mock.calls[2])).toBe('Bearer fresh-token')
  })
  it('shares one refresh between concurrent 401s, because the server treats a reused token as stolen', async () => {
    const refreshed = deferred<Response>()
    const fetch = vi.fn((path: string, init?: RequestInit) => {
      if (path === '/auth/refresh') return refreshed.promise
      const auth = (init?.headers as Headers).get('Authorization')
      return Promise.resolve(auth === 'Bearer fresh-token' ? json({ ok: true }) : json({ error: '过期' }, 401))
    })
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(signedIn('stale-token'))
    const both = Promise.all([session.call('/me', {}, 'required'), session.call('/me/videos', {}, 'required')])
    await vi.waitFor(() => expect(fetch.mock.calls.filter(call => pathOf(call) === '/auth/refresh')).toHaveLength(1))
    refreshed.resolve(json(signedIn('fresh-token')))
    await expect(both).resolves.toEqual([{ ok: true }, { ok: true }])
    expect(fetch.mock.calls.filter(call => pathOf(call) === '/auth/refresh')).toHaveLength(1)
  })
  it('signs out when the refresh token is no longer valid', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ error: '登录已过期，请重新登录' }, 401)))
    const session = useSessionStore(); session.setSession(signedIn('stale-token'))
    await expect(session.call('/me', {}, 'required')).rejects.toMatchObject({ status: 401 })
    expect(session.user).toBeNull(); expect(session.notice).toBe('登录状态已失效，请重新登录')
  })
  it('signs out when even the refreshed token is rejected', async () => {
    const fetch = vi.fn((path: string) => Promise.resolve(path === '/auth/refresh' ? json(signedIn('fresh-token')) : json({ error: '登录已失效，请重新登录' }, 401)))
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(signedIn('stale-token'))
    await expect(session.call('/me', {}, 'required')).rejects.toMatchObject({ status: 401 })
    expect(fetch.mock.calls.map(pathOf)).toEqual(['/me', '/auth/refresh', '/me'])
    expect(session.user).toBeNull(); expect(session.notice).toBe('登录状态已失效，请重新登录')
  })
  it('refreshes a minute before the access token expires', async () => {
    vi.setSystemTime(new Date('2026-11-10T08:00:00Z'))
    const fetch = vi.fn().mockResolvedValue(json(signedIn(jwt(Date.now() / 1000 + 30 * 60))))
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(signedIn(jwt(Date.now() / 1000 + 15 * 60)))
    await vi.advanceTimersByTimeAsync(13 * 60_000)
    expect(fetch).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(60_000 + 1)
    expect(fetch.mock.calls.map(pathOf)).toEqual(['/auth/refresh'])
  })
  it('does not sign out a newer login when an old request comes back with 401', async () => {
    const answer = deferred<Response>()
    const fetch = vi.fn(() => answer.promise)
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(signedIn('old-token'))
    const pending = session.call('/me', {}, 'required')
    session.setSession({ id: 2, username: 'bob', token: 'new-token' })
    answer.resolve(json({ error: '登录已失效，请重新登录' }, 401))
    await expect(pending).rejects.toMatchObject({ status: 401 })
    expect(session.user?.username).toBe('bob'); expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('keeps the current login when refreshing is only temporarily unavailable', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ error: '登录服务暂时不可用，请稍后再试' }, 503)))
    const session = useSessionStore(); session.setSession(signedIn('still-valid-token'))
    await expect(session.refresh()).resolves.toBe(false)
    expect(session.token).toBe('still-valid-token'); expect(session.notice).toBe('')
  })
})

describe('logging out', () => {
  it('leaves at once, asks the server to end the session, and ignores a refresh that finishes later', async () => {
    const refreshed = deferred<Response>(), loggedOut = deferred<Response>()
    const fetch = vi.fn((path: string) => path === '/auth/refresh' ? refreshed.promise : loggedOut.promise)
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(signedIn('token'))
    const pending = session.refresh()
    const leaving = session.logout()
    // The page is signed out before the server has answered.
    expect(session.user).toBeNull(); expect(session.token).toBe('')
    expect(fetch.mock.calls.map(pathOf)).toEqual(['/auth/refresh', '/auth/logout'])
    loggedOut.resolve(empty()); await leaving
    refreshed.resolve(json(signedIn('late-token')))
    await expect(pending).resolves.toBe(false)
    expect(session.user).toBeNull(); expect(session.token).toBe('')
  })
  it('cancels the planned refresh, so a failed logout cannot sign the page back in', async () => {
    vi.setSystemTime(new Date('2026-11-10T08:00:00Z'))
    const fetch = vi.fn((path: string) => Promise.resolve(path === '/auth/refresh' ? json(signedIn('revived-token')) : json({ error: '服务暂时不可用' }, 503)))
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(signedIn(jwt(Date.now() / 1000 + 15 * 60)))
    await session.logout()
    await vi.advanceTimersByTimeAsync(20 * 60_000)
    expect(fetch.mock.calls.map(pathOf)).toEqual(['/auth/logout'])
    expect(session.user).toBeNull()
  })
  it('still leaves when the logout request fails', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')))
    const session = useSessionStore(); session.setSession(signedIn('token'))
    await session.logout()
    expect(session.user).toBeNull()
  })
})
