import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, request } from '../../src/api/client'
afterEach(() => vi.unstubAllGlobals())
describe('HTTP contract', () => {
  it('sends JSON and an existing bearer credential', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response('{"id":1}', { status: 201 }))
    vi.stubGlobal('fetch', fetch)
    expect(await request('/users', { method: 'POST', body: { username: 'test' }, token: 'issued-token' })).toEqual({ id: 1 })
    const options = fetch.mock.calls[0][1]
    expect(options.headers.get('Authorization')).toBe('Bearer issued-token')
    expect(options.headers.get('Content-Type')).toBe('application/json')
    expect(JSON.parse(options.body)).toEqual({ username: 'test' })
  })
  it('lets the browser generate multipart boundaries and accepts 204', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetch)
    const body = new FormData(); body.append('file', new Blob(['fixture']), 'fixture.mp4')
    expect(await request('/uploads/videos', { method: 'POST', body })).toBeNull()
    expect(fetch.mock.calls[0][1].headers.has('Content-Type')).toBe(false)
    expect(fetch.mock.calls[0][1].body).toBe(body)
  })
  it('preserves business error codes and hides non-JSON server failures', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(new Response('{"code":"INVALID_CURSOR","error":"请刷新"}', { status: 400 })).mockResolvedValueOnce(new Response('internal SQL detail', { status: 500 })))
    await expect(request('/feed')).rejects.toMatchObject({ status: 400, code: 'INVALID_CURSOR', message: '请刷新' })
    await expect(request('/feed')).rejects.toMatchObject({ message: '请求失败（500）' })
  })
  it('rejects malformed successful JSON and network failures', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(new Response('<html>wrong</html>')).mockRejectedValueOnce(new Error('network')))
    await expect(request('/feed')).rejects.toBeInstanceOf(ApiError)
    await expect(request('/feed')).rejects.toMatchObject({ status: 0 })
  })
})
