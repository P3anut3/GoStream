import { afterEach, describe, expect, it, vi } from 'vitest'
import { createEventParser, readStream, type StreamEvent } from '../../src/api/stream'
import { eventStream } from './event-stream'

function parse(chunks: string[]) {
  const events: StreamEvent[] = []
  const feed = createEventParser(event => events.push(event))
  for (const chunk of chunks) feed(chunk)
  return events
}

describe('event parser', () => {
  it('reads named events and skips comments', () => {
    expect(parse([': connected\n\nevent: notification\ndata: {}\n\n: ping\n\n'])).toEqual([{ type: 'notification', data: '{}' }])
  })
  it('accepts \\n, \\r\\n and \\r as line ends', () => {
    for (const text of ['event: a\ndata: 1\n\n', 'event: a\r\ndata: 1\r\n\r\n', 'event: a\rdata: 1\r\r']) {
      expect(parse([text]), JSON.stringify(text)).toEqual([{ type: 'a', data: '1' }])
    }
  })
  it('gives the same events however the text is split', () => {
    const text = 'event: notification\r\ndata: {}\r\n\r\n: ping\r\n\r\ndata: line one\r\ndata: line two\r\n\r\n'
    const want = [{ type: 'notification', data: '{}' }, { type: 'message', data: 'line one\nline two' }]
    expect(parse([text])).toEqual(want)
    expect(parse([...text])).toEqual(want) // one character at a time, \r and \n apart
    for (let cut = 1; cut < text.length; cut++) expect(parse([text.slice(0, cut), '', text.slice(cut)])).toEqual(want)
  })
  it('follows the field rules', () => {
    expect(parse(['data:no space\n\n'])).toEqual([{ type: 'message', data: 'no space' }])
    expect(parse(['data:  two spaces\n\n'])).toEqual([{ type: 'message', data: ' two spaces' }]) // only one space is dropped
    expect(parse(['data\n\n'])).toEqual([{ type: 'message', data: '' }]) // a field without a colon has an empty value
    expect(parse(['event: notification\n\n', 'data: {}\n\n'])).toEqual([{ type: 'message', data: '{}' }]) // no data: no event, and the name resets
    expect(parse(['id: 3\nretry: 10\nunknown: x\ndata: {}\n\n'])).toEqual([{ type: 'message', data: '{}' }])
    expect(parse(['data: unfinished\n'])).toEqual([]) // an event needs its blank line
  })
})

describe('readStream', () => {
  afterEach(() => { vi.unstubAllGlobals() })
  const handlers = () => ({ onOpen: vi.fn(), onEvent: vi.fn(), onData: vi.fn() })

  it('sends the token, reports the events and ends when the server ends the response', async () => {
    let server!: ReturnType<typeof eventStream>
    const fetch = vi.fn(async (_path: string, init: RequestInit) => { server = eventStream(init.signal); return server.response })
    vi.stubGlobal('fetch', fetch)
    const on = handlers()
    const reading = readStream('/notifications/stream', 'alice-credential', new AbortController().signal, on)
    await vi.waitFor(() => expect(on.onOpen).toHaveBeenCalledOnce())
    expect(fetch.mock.calls[0][0]).toBe('/notifications/stream')
    expect(new Headers(fetch.mock.calls[0][1].headers).get('Authorization')).toBe('Bearer alice-credential')
    server.send(': connected\n\nevent: notif')
    server.send('ication\ndata: {}\n\n')
    await vi.waitFor(() => expect(on.onEvent).toHaveBeenCalledWith({ type: 'notification', data: '{}' }))
    expect(on.onData).toHaveBeenCalledTimes(2) // every chunk, comments included, shows the connection is alive
    server.end()
    await expect(reading).resolves.toBe('ended')
  })
  it('tells why it could not connect', async () => {
    for (const [answer, want] of [
      [() => new Response('{"error":"登录凭证无效或已过期"}', { status: 401 }), 'unauthorized'],
      [() => new Response('404 page not found', { status: 404 }), 'unsupported'],
      [() => new Response('{"error":"推送连接太多"}', { status: 429 }), 'failed'],
      [() => new Response('{"error":"服务正在关闭"}', { status: 503 }), 'failed'],
      [() => new Response('<!doctype html>', { status: 200, headers: { 'Content-Type': 'text/html' } }), 'failed'],
      [() => { throw new TypeError('Failed to fetch') }, 'failed'],
    ] as const) {
      vi.stubGlobal('fetch', vi.fn(async () => answer()))
      const on = handlers()
      expect(await readStream('/notifications/stream', 't', new AbortController().signal, on)).toBe(want)
      expect(on.onOpen).not.toHaveBeenCalled()
    }
  })
  it('reports an abort as aborted, before and after connecting', async () => {
    vi.stubGlobal('fetch', vi.fn((_path: string, init: RequestInit) => new Promise((_, reject) => {
      init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    })))
    const early = new AbortController()
    const connecting = readStream('/notifications/stream', 't', early.signal, handlers())
    early.abort()
    await expect(connecting).resolves.toBe('aborted')

    vi.stubGlobal('fetch', vi.fn(async (_path: string, init: RequestInit) => eventStream(init.signal).response))
    const late = new AbortController(), on = handlers()
    const reading = readStream('/notifications/stream', 't', late.signal, on)
    await vi.waitFor(() => expect(on.onOpen).toHaveBeenCalledOnce())
    late.abort()
    await expect(reading).resolves.toBe('aborted')
  })
})
