import { defineStore } from 'pinia'
import { ref } from 'vue'
import { ApiError, request, type RequestOptions } from '../api/client'
import type { User, LoginResponse } from '../types'

// tokenExpiry 读出 JWT 里的 exp（毫秒），只用来安排何时刷新，不做任何安全判断；读不出时返回 0。
function tokenExpiry(value: string): number {
  try {
    const payload = JSON.parse(atob(value.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')))
    return typeof payload.exp === 'number' ? payload.exp * 1000 : 0
  } catch { return 0 }
}

export const useSessionStore = defineStore('session', () => {
  // access token 只放在内存里，不写 localStorage；refresh token 在 HttpOnly Cookie 里，页面脚本读不到。
  // 打开页面时用 Cookie 换一次 access token，所以刷新页面不用重新登录。
  const token = ref('')
  const user = ref<User | null>(null)
  const revision = ref(0)
  const authOpen = ref(false)
  const destination = ref('')
  const notice = ref('')
  const publishing = ref(false)
  let noticeTimer: ReturnType<typeof setTimeout> | undefined
  let refreshTimer: ReturnType<typeof setTimeout> | undefined
  let refreshing: Promise<boolean> | null = null
  let restoring: Promise<void> | null = null
  function notify(message: string) {
    notice.value = message
    clearTimeout(noticeTimer)
    noticeTimer = setTimeout(() => { notice.value = '' }, 4500)
  }
  function setSession(value: LoginResponse) {
    if (!value.token || !value.id || !value.username) throw new Error('登录响应格式异常')
    token.value = value.token
    user.value = { id: value.id, username: value.username }
    revision.value++
    scheduleRefresh(value.token)
  }
  function clear() { clearTimeout(refreshTimer); token.value = ''; user.value = null; revision.value++ }
  function requireLogin(path = '') { destination.value = path; authOpen.value = true }
  // access token 过期前 1 分钟自动换新；读不出过期时间时不安排，等收到 401 再刷新。
  function scheduleRefresh(value: string) {
    clearTimeout(refreshTimer)
    const expiresAt = tokenExpiry(value)
    if (!expiresAt) return
    refreshTimer = setTimeout(() => { void refresh() }, Math.max(expiresAt - Date.now() - 60_000, 5_000))
  }
  // refresh 用 Cookie 换一对新凭证。同一时刻只发一个请求：服务端会把两次使用同一个旧 token 当成被盗用。
  function refresh(): Promise<boolean> {
    if (refreshing) return refreshing
    const before = revision.value
    refreshing = request<LoginResponse>('/auth/refresh', { method: 'POST' })
      .then(result => {
        if (revision.value !== before) return false // 刷新期间已经退出或换了账号：作废这次结果
        setSession(result)
        return true
      })
      .catch(error => {
        // 401 说明这次登录已经结束；其他错误（服务暂时不可用）先保留现状，access token 也许还能用。
        if (error instanceof ApiError && error.status === 401 && revision.value === before && token.value) {
          clear(); notify('登录状态已失效，请重新登录')
        }
        return false
      })
      .finally(() => { refreshing = null })
    return refreshing
  }
  // restore 在第一次导航前执行一次：Cookie 还有效就恢复登录。路由守卫要等它，避免误弹登录框。
  function restore(): Promise<void> {
    restoring ??= refresh().then(() => undefined)
    return restoring
  }
  // logout 先清本地状态，再请服务端删除会话；就算请求失败，这个页面也已经退出。
  async function logout() {
    clear()
    try { await request<void>('/auth/logout', { method: 'POST' }) } catch { /* 服务端的会话会在过期后消失 */ }
  }
  async function call<T>(path: string, options: RequestOptions = {}, auth: 'none' | 'optional' | 'required' = 'none'): Promise<T> {
    const captured = auth === 'none' ? '' : token.value
    if (auth === 'required' && !captured) throw new ApiError('请先登录', 401)
    try { return await request<T>(path, { ...options, token: captured }) }
    catch (error) {
      if (!(error instanceof ApiError) || error.status !== 401 || !captured) throw error
      // access token 过期或会话已结束：先换一个新的，成功就把这次请求重发一次（401 发生在处理之前，重发是安全的）。
      if (token.value === captured && await refresh()) {
        const renewed = token.value
        try { return await request<T>(path, { ...options, token: renewed }) }
        catch (retryError) {
          // 刚换到的新 token 也被拒绝：这次登录已经结束。
          if (retryError instanceof ApiError && retryError.status === 401 && token.value === renewed) { clear(); notify('登录状态已失效，请重新登录') }
          throw retryError
        }
      }
      // 旧请求不能把稍后登录的新会话清掉。
      if (token.value === captured) { clear(); notify('登录状态已失效，请重新登录') }
      throw error
    }
  }
  return { token, user, revision, authOpen, destination, notice, publishing, notify, setSession, clear, requireLogin, refresh, restore, logout, call }
})
