// eventStream answers like a server that keeps the response open: the test writes into it and
// ends it, and aborting the request ends it the way a real fetch would.
export function eventStream(signal?: AbortSignal | null, contentType = 'text/event-stream') {
  let controller!: ReadableStreamDefaultController<Uint8Array>
  let open = true
  const body = new ReadableStream<Uint8Array>({ start(c) { controller = c } })
  signal?.addEventListener('abort', () => {
    if (open) { open = false; controller.error(new DOMException('aborted', 'AbortError')) }
  })
  return {
    response: new Response(body, { headers: { 'Content-Type': contentType } }),
    get open() { return open },
    send(text: string) { if (open) controller.enqueue(new TextEncoder().encode(text)) },
    end() { if (open) { open = false; controller.close() } },
  }
}
export type EventStream = ReturnType<typeof eventStream>
