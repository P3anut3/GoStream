import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useSessionStore } from '../../src/stores/session'
import { useLibraryStore } from '../../src/stores/library'
import { usePublishStore } from '../../src/stores/publish'
import { validateFile, mediaURL } from '../../src/lib/media'

const login = (name = 'alice') => ({ id: name === 'alice' ? 1 : 2, username: name, token: name + '-credential' })
const item = (id: number) => ({ id, author_id: 1, title: `video ${id}`, play_url: '/media/videos/f.mp4', cover_url: '/cover.svg', created_at: '' })
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status })
const deferred = <T>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
beforeEach(() => { setActivePinia(createPinia()); vi.useFakeTimers() })
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

describe('session isolation', () => {
  it('clears the current invalid session, but not a later login', async () => {
    const session = useSessionStore(); session.setSession(login())
    const pending = deferred<Response>(); vi.stubGlobal('fetch', vi.fn().mockReturnValueOnce(pending.promise))
    const request = session.call('/me', {}, 'required')
    session.setSession(login('bob'))
    pending.resolve(json({ error: '失效' }, 401))
    await expect(request).rejects.toMatchObject({ status: 401 })
    expect(session.user?.username).toBe('bob')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json({ error: '失效' }, 401)))
    await expect(session.call('/me', {}, 'required')).rejects.toMatchObject({ status: 401 })
    expect(session.token).toBe(''); expect(session.user).toBeNull()
  })
})

describe('feed and personal pagination', () => {
  it('encodes the server cursor, deduplicates IDs, and does not load beyond the end', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(json({ videos: [item(3), item(2)], next_cursor: 'opaque+/=', has_more: true })).mockResolvedValueOnce(json({ videos: [item(2), item(1)], next_cursor: '', has_more: false }))
    vi.stubGlobal('fetch', fetch)
    const library = useLibraryStore(); await library.reset('all'); await library.load(true); await library.load(true)
    expect(fetch.mock.calls[1][0]).toContain('cursor=opaque%2B%2F%3D')
    expect(library.videos.map(v => v.id)).toEqual([3, 2, 1]); expect(fetch).toHaveBeenCalledTimes(2)
  })
  it('keeps items and cursor after a failed append, retrying the same location', async () => {
    const fetch = vi.fn().mockResolvedValueOnce(json({ videos: [item(2)], next_cursor: 'cursor', has_more: true })).mockResolvedValueOnce(json({ error: '暂时失败' }, 500)).mockResolvedValueOnce(json({ videos: [item(1)], next_cursor: '', has_more: false }))
    vi.stubGlobal('fetch', fetch)
    const library = useLibraryStore(); await library.load(); await library.load(true)
    expect(library.videos).toHaveLength(1); expect(library.nextCursor).toBe('cursor')
    await library.load(true); expect(fetch.mock.calls[1][0]).toBe(fetch.mock.calls[2][0]); expect(library.videos).toHaveLength(2)
  })
  it('requires refresh on invalid cursor and ignores old responses after switching scope', async () => {
    const pending = deferred<Response>()
    const fetch = vi.fn().mockReturnValueOnce(pending.promise).mockResolvedValueOnce(json({ videos: [item(7)], page: 1, page_size: 6 }))
    vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(login())
    const library = useLibraryStore(); const old = library.load(); await library.reset('mine')
    pending.resolve(json({ videos: [item(9)], next_cursor: '', has_more: false })); await old
    expect(library.videos.map(v => v.id)).toEqual([7]); expect(fetch.mock.calls[1][0]).toBe('/me/videos?page=1&page_size=6')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(json({ videos: [item(2)], next_cursor: 'old', has_more: true })).mockResolvedValueOnce(json({ code: 'INVALID_CURSOR', error: '请刷新' }, 400)))
    await library.reset('all'); await library.load(true); expect(library.cursorInvalid).toBe(true)
  })
  it('reads the following feed with the token and never sends its cursor to the latest feed', async () => {
    const session = useSessionStore(); session.setSession(login())
    const fetch = vi.fn()
      .mockResolvedValueOnce(json({ videos: [item(5)], next_cursor: 'following-cursor', has_more: true }))
      .mockResolvedValueOnce(json({ videos: [item(4)], next_cursor: '', has_more: false }))
      .mockResolvedValueOnce(json({ videos: [item(9)], next_cursor: 'latest-cursor', has_more: true }))
    vi.stubGlobal('fetch', fetch)
    const library = useLibraryStore(); await library.reset('following'); await library.load(true)
    expect(fetch.mock.calls[0][0]).toBe('/feed/following?limit=6')
    expect(fetch.mock.calls[1][0]).toBe('/feed/following?limit=6&cursor=following-cursor')
    expect(fetch.mock.calls[0][1].headers.get('Authorization')).toBe('Bearer alice-credential')
    expect(library.videos.map(v => v.id)).toEqual([5, 4])
    await library.reset('all')
    expect(fetch.mock.calls[2][0]).toBe('/feed?limit=6'); expect(library.videos.map(v => v.id)).toEqual([9])
  })
  it('asks for a login instead of requesting the following feed anonymously', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    const library = useLibraryStore(); await library.reset('following')
    expect(fetch).not.toHaveBeenCalled(); expect(library.error).toBe('请先登录')
  })
  it('does not expose a previous user’s personal response after login changes', async () => {
    const session = useSessionStore(); session.setSession(login())
    const pending = deferred<Response>(); vi.stubGlobal('fetch', vi.fn().mockReturnValueOnce(pending.promise))
    const library = useLibraryStore(); library.scope = 'mine'; const old = library.load()
    session.setSession(login('bob')); pending.resolve(json({ videos: [item(1)], page: 1, page_size: 6 })); await old
    expect(library.videos).toEqual([])
  })
})

describe('publish workflow', () => {
  it('reuses successful uploads on publish retry and uses the authenticated identity', async () => {
    const session = useSessionStore(); session.setSession(login())
    const fetch = vi.fn().mockResolvedValueOnce(json({ play_url: '/media/videos/a.mp4' })).mockResolvedValueOnce(json({ cover_url: '/media/covers/a.png' })).mockResolvedValueOnce(json({ error: '暂时失败' }, 500)).mockResolvedValueOnce(json({ id: 1 }, 201))
    vi.stubGlobal('fetch', fetch)
    const draft = usePublishStore(); draft.title = '作品'; draft.choose(new File(['video'], 'a.mp4', { type: 'video/mp4' }), 'video'); draft.choose(new File(['image'], 'a.png', { type: 'image/png' }), 'cover')
    expect(await draft.publish()).toBe(false); expect(draft.uploaded?.play_url).toBe('/media/videos/a.mp4')
    expect(await draft.publish()).toBe(true)
    expect(fetch.mock.calls.map(c => c[0])).toEqual(['/uploads/videos', '/uploads/covers', '/videos', '/videos'])
    const body = JSON.parse(fetch.mock.calls[3][1].body)
    expect(body).toEqual({ title: '作品', play_url: '/media/videos/a.mp4', cover_url: '/media/covers/a.png' }); expect(body).not.toHaveProperty('author_id')
    expect(draft.file).toBeNull()
  })
  it('prevents duplicate requests while an upload is pending and clears account-bound drafts', async () => {
    const session = useSessionStore(); session.setSession(login())
    const pending = deferred<Response>(), fetch = vi.fn().mockReturnValueOnce(pending.promise)
    vi.stubGlobal('fetch', fetch)
    const draft = usePublishStore(); draft.title = '作品'; draft.choose(new File(['v'], 'a.mp4'), 'video')
    const first = draft.publish(); expect(await draft.publish()).toBe(false); expect(fetch).toHaveBeenCalledTimes(1)
    session.clear(); session.setSession(login('bob')); pending.resolve(json({ play_url: '/media/videos/alice.mp4' }))
    expect(await first).toBe(false); expect(draft.uploaded).toBeNull(); expect(draft.title).toBe(''); expect(fetch).toHaveBeenCalledTimes(1)
  })
  it('validates files, Unicode title length, and local upload paths before sending', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch)
    const session = useSessionStore(); session.setSession(login()); const draft = usePublishStore()
    draft.source = 'url'; draft.playURL = 'https://example.com/file.mp4'; draft.title = '作品'
    expect(await draft.publish()).toBe(false)
    draft.playURL = '/media/videos/a.mp4'; draft.title = '😀'.repeat(101)
    expect(await draft.publish()).toBe(false); expect(fetch).not.toHaveBeenCalled()
    expect(validateFile(new File(['x'], 'wrong.txt'), 'video')).toContain('MP4')
    expect(validateFile(new File([], 'empty.mp4'), 'video')).toContain('空')
    expect(validateFile({ name: 'a.mp4', type: 'video/mp4', size: 100 * 1024 * 1024 + 1 } as File, 'video')).toContain('100 MiB')
    expect(validateFile({ name: 'a.png', type: 'image/png', size: 5 * 1024 * 1024 + 1 } as File, 'cover')).toContain('5 MiB')
    expect(mediaURL('javascript:alert(1)')).toBe('')
  })
})
