<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useSessionStore } from '../stores/session'
import { errorMessage } from '../api/client'
import type { User } from '../types'
const session = useSessionStore(), error = ref(''), loading = ref(false)
async function load() {
  const revision = session.revision
  loading.value = true; error.value = ''
  try {
    const user = await session.call<User>('/me', {}, 'required')
    if (revision === session.revision) session.user = user
  } catch (cause) { error.value = errorMessage(cause) }
  finally { loading.value = false }
}
onMounted(load)
</script>
<template>
  <section class="intro"><div><span class="eyebrow">YOUR ACCOUNT</span><h1>当前用户<span class="orange">。</span></h1><p>身份来自服务端验证后的登录凭证。</p></div></section>
  <section class="page-panel account-panel">
    <p v-if="loading" role="status">正在读取账户信息……</p><p v-if="error" class="form-message" role="alert">{{ error }}</p>
    <template v-if="session.user"><div class="profile-avatar">{{ session.user.username.slice(0, 1) }}</div><h2>{{ session.user.username }}</h2><p class="muted">用户 ID：{{ session.user.id }}</p></template>
    <div class="detail-actions"><button class="button" :disabled="loading" @click="load">刷新账户</button><RouterLink class="button primary" to="/mine">查看我的视频</RouterLink></div>
    <p class="fine-print">刷新浏览器后需要重新登录。当前退出只清除页面凭证，服务端撤销将在后续认证模块实现。</p>
  </section>
</template>
