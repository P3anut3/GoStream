<script setup lang="ts">
import { watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from './stores/session'
import AuthDialog from './components/AuthDialog.vue'
const session = useSessionStore(), route = useRoute(), router = useRouter()
function logout() {
  if (session.publishing) { session.notify('请等待当前发布完成'); return }
  session.clear(); session.notify('已退出当前页面的登录状态')
}
watch(() => session.token, token => { if (!token && route.meta.auth) void router.replace('/') })
</script>

<template>
  <aside class="sidebar">
    <RouterLink class="brand" to="/" aria-label="GoStream 首页"><span class="brand-icon">▶</span>GoStream<span class="orange">.</span></RouterLink>
    <p class="nav-label">个人工作空间</p>
    <nav aria-label="主导航">
      <RouterLink id="nav-library" class="nav-item" exact-active-class="active" to="/"><span>▦</span>视频库<span class="arrow">↗</span></RouterLink>
      <RouterLink id="nav-mine" class="nav-item" active-class="active" to="/mine"><span>▷</span>我的视频</RouterLink>
      <RouterLink class="nav-item" active-class="active" to="/publish"><span>＋</span>发布视频</RouterLink>
      <RouterLink class="nav-item" active-class="active" to="/account"><span>◎</span>当前用户</RouterLink>
    </nav>
    <div class="sidebar-bottom"><strong>从一个片段开始。</strong><p>收集灵感，分享你的视角。</p><span class="local"><i></i>本地工作台</span></div>
  </aside>
  <div class="shell">
    <header class="topbar">
      <div class="breadcrumb">工作台 <span>/</span><strong>{{ route.meta.title }}</strong></div>
      <div class="account">
        <span id="account-name">{{ session.user?.username || '访客模式' }}</span>
        <RouterLink v-if="session.user" class="button small" to="/account">账户信息</RouterLink>
        <button v-else class="button small" @click="session.requireLogin()">登录 / 注册</button>
        <button v-if="session.user" class="text-button" :disabled="session.publishing" @click="logout">退出</button>
      </div>
    </header>
    <main><RouterView /><footer>GoStream · 让故事流动起来 <span>用户 · 视频 · 你的创作</span></footer></main>
  </div>
  <AuthDialog />
  <div v-if="session.notice" class="toast" role="status">{{ session.notice }}</div>
</template>
