// 读服务端推送（SSE）。浏览器自带的 EventSource 不能带 Authorization 头，只能把 token 放进 URL，
// 会留在日志和浏览记录里；所以用 fetch 带上头，自己读响应体、按空行切出一条条事件。

export interface StreamEvent { type: string; data: string }

// createEventParser 返回一个函数：把收到的文字一段段喂进去，每凑齐一条事件就回调一次。
// 按 SSE 的规则：\n、\r\n、\r 都算换行（\r\n 可能被拆在两段里）；冒号开头的行是注释；
// event 是事件名（不写就是 message）；data 可以有多行；空行表示一条事件结束，没有 data 的不算。
export function createEventParser(onEvent: (event: StreamEvent) => void): (chunk: string) => void {
  let buffer = '', type = '', data: string[] = [], afterCR = false
  function line(text: string) {
    if (text === '') {
      if (data.length) onEvent({ type: type || 'message', data: data.join('\n') })
      type = ''; data = []
      return
    }
    if (text.startsWith(':')) return
    const colon = text.indexOf(':')
    const field = colon === -1 ? text : text.slice(0, colon)
    let value = colon === -1 ? '' : text.slice(colon + 1)
    if (value.startsWith(' ')) value = value.slice(1)
    if (field === 'event') type = value
    else if (field === 'data') data.push(value)
  }
  return chunk => {
    if (!chunk) return
    if (afterCR && chunk.startsWith('\n')) chunk = chunk.slice(1) // 上一段以 \r 结尾：这个 \n 属于同一个换行
    afterCR = false
    buffer += chunk
    let start = 0
    for (let i = 0; i < buffer.length; i++) {
      const char = buffer[i]
      if (char !== '\n' && char !== '\r') continue
      line(buffer.slice(start, i))
      if (char === '\r') {
        if (i + 1 === buffer.length) afterCR = true
        else if (buffer[i + 1] === '\n') i++
      }
      start = i + 1
    }
    buffer = buffer.slice(start)
  }
}

export interface StreamHandlers {
  onOpen(): void // 连上了
  onEvent(event: StreamEvent): void
  onData(): void // 收到任何字节（心跳也算）：连接还活着
}

// readStream 打开推送连接，一直读到结束，返回结束的原因：
// aborted 是调用方用 signal 取消的；unauthorized 是 401（token 过期或会话已结束）；
// unsupported 是 404（服务端没有开推送）；failed 是没连上（网络错误、其他状态码、不是事件流）；
// ended 是连上以后断开了（服务端到点结束、服务重启、网络中断）。
export type StreamEnd = 'aborted' | 'unauthorized' | 'unsupported' | 'failed' | 'ended'

export async function readStream(path: string, token: string, signal: AbortSignal, handlers: StreamHandlers): Promise<StreamEnd> {
  let response: Response
  try {
    response = await fetch(path, { headers: { Authorization: `Bearer ${token}` }, signal, cache: 'no-store' })
  } catch {
    return signal.aborted ? 'aborted' : 'failed'
  }
  if (response.status === 401) return 'unauthorized'
  if (response.status === 404) return 'unsupported'
  if (!response.ok || !response.body || !response.headers.get('Content-Type')?.startsWith('text/event-stream')) {
    void response.body?.cancel().catch(() => {})
    return 'failed'
  }
  handlers.onOpen()
  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  const parse = createEventParser(handlers.onEvent)
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      handlers.onData()
      parse(decoder.decode(value, { stream: true }))
    }
  } catch { /* 读的过程中断开了，包括被取消 */ }
  return signal.aborted ? 'aborted' : 'ended'
}
