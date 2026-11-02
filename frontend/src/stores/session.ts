import { defineStore } from 'pinia'
import { ref } from 'vue'
import { ApiError, request, type RequestOptions } from '../api/client'
import type { User, LoginResponse } from '../types'

export const useSessionStore = defineStore('session', () => {
  // 不写 localStorage，也不把密钥发给浏览器；刷新页面后重新登录。
  const token = ref('')
  const user = ref<User | null>(null)
  const revision = ref(0)
  const authOpen = ref(false)
  const destination = ref('')
  const notice = ref('')
  const publishing = ref(false)
  let noticeTimer: ReturnType<typeof setTimeout> | undefined
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
  }
  function clear() { token.value = ''; user.value = null; revision.value++ }
  function requireLogin(path = '') { destination.value = path; authOpen.value = true }
  async function call<T>(path: string, options: RequestOptions = {}, auth: 'none' | 'optional' | 'required' = 'none'): Promise<T> {
    const captured = auth === 'none' ? '' : token.value
    if (auth === 'required' && !captured) throw new ApiError('请先登录', 401)
    try { return await request<T>(path, { ...options, token: captured }) }
    catch (error) {
      // 旧请求不能把稍后登录的新会话清掉。
      if (error instanceof ApiError && error.status === 401 && captured && token.value === captured) {
        clear(); notify('登录状态已失效，请重新登录')
      }
      throw error
    }
  }
  return { token, user, revision, authOpen, destination, notice, publishing, notify, setSession, clear, requireLogin, call }
})
