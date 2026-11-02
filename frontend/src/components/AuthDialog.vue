<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { errorMessage } from '../api/client'
import type { LoginResponse } from '../types'
const session = useSessionStore(), router = useRouter()
const dialog = ref<HTMLDialogElement>(), mode = ref<'login' | 'register'>('login')
const username = ref(''), password = ref(''), busy = ref(false), error = ref('')
watch(() => session.authOpen, open => {
  error.value = ''; password.value = ''
  if (open) dialog.value?.showModal(); else dialog.value?.close()
}, { flush: 'post' })
onMounted(() => { if (session.authOpen) dialog.value?.showModal() })
function close() { if (!busy.value) { session.authOpen = false; session.destination = ''; password.value = '' } }
async function submit() {
  if (busy.value) return
  const registering = mode.value === 'register'
  const length = new TextEncoder().encode(password.value).length
  if (registering && (length < 8 || length > 72)) { error.value = '密码需为 8～72 字节'; return }
  busy.value = true; error.value = ''; let registered = false
  try {
    const credentials = { username: username.value, password: password.value }
    if (registering) { await session.call('/users', { method: 'POST', body: credentials }); registered = true }
    const result = await session.call<LoginResponse>('/login', { method: 'POST', body: credentials })
    session.setSession(result)
    const destination = session.destination
    session.authOpen = false; session.destination = ''; password.value = ''
    session.notify(registering ? '账号已创建，欢迎加入 GoStream' : `欢迎回来，${result.username}`)
    if (destination) await router.push(destination)
  } catch (cause) { error.value = (registered ? '账号已创建，但自动登录失败。请切换到登录后重试：' : '') + errorMessage(cause) }
  finally { busy.value = false }
}
</script>

<template>
  <dialog ref="dialog" class="modal auth-modal" aria-labelledby="auth-title" @cancel.prevent="close" @close="session.authOpen = false">
    <button class="close-button" :disabled="busy" aria-label="关闭登录窗口" @click="close">×</button>
    <span class="eyebrow">WELCOME TO GOSTREAM</span><h2 id="auth-title">{{ mode === 'login' ? '欢迎回来' : '创建你的账号' }}</h2>
    <p class="muted">登录后，发布属于你的视频。</p>
    <div class="tabs"><button :disabled="busy" :class="{ selected: mode === 'login' }" @click="mode = 'login'; error = ''">登录</button><button :disabled="busy" :class="{ selected: mode === 'register' }" @click="mode = 'register'; error = ''">注册账号</button></div>
    <form @submit.prevent="submit">
      <fieldset :disabled="busy">
        <label for="username">用户名</label><input id="username" v-model="username" autocomplete="username" required maxlength="32" placeholder="输入用户名" />
        <label for="password">密码</label><input id="password" v-model="password" type="password" :autocomplete="mode === 'login' ? 'current-password' : 'new-password'" required placeholder="输入密码" />
        <p class="form-message" role="alert">{{ error }}</p>
        <button type="submit" class="button primary full">{{ busy ? '正在处理……' : mode === 'login' ? '登录' : '创建账号' }}</button>
      </fieldset>
    </form>
    <p class="fine-print">登录状态仅保留在当前页面，刷新后需重新登录。</p>
  </dialog>
</template>
