export class ApiError extends Error {
  constructor(message: string, public status = 0, public code = '') { super(message) }
}
export interface RequestOptions {
  method?: string
  body?: unknown
  token?: string
  signal?: AbortSignal
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers = new Headers()
  if (options.token) headers.set('Authorization', `Bearer ${options.token}`)
  const form = options.body instanceof FormData
  if (options.body !== undefined && !form) headers.set('Content-Type', 'application/json')
  let response: Response
  try {
    response = await fetch(path, {
      method: options.method || 'GET', headers, signal: options.signal,
      body: options.body === undefined ? undefined : form ? options.body as FormData : JSON.stringify(options.body),
    })
  } catch (error) {
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw new ApiError('无法连接服务，请检查网络并确认 GoStream 正在运行')
  }
  const text = await response.text()
  let data: any = null
  try { data = text ? JSON.parse(text) : null } catch {
    if (response.ok) throw new ApiError('服务器响应格式异常', response.status)
  }
  if (!response.ok) {
    throw new ApiError(
      typeof data?.error === 'string' ? data.error : response.status === 413 ? '文件或请求过大，请检查上传限制' : `请求失败（${response.status}）`,
      response.status, typeof data?.code === 'string' ? data.code : '',
    )
  }
  return data as T // 204 的响应为空，调用处使用 void。
}
export const errorMessage = (error: unknown) => error instanceof Error ? error.message : '操作失败，请重试'
