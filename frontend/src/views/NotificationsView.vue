<script setup lang="ts">
import { computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import { errorMessage } from '../api/client'
import { useNotificationStore } from '../stores/notifications'
import { useSessionStore } from '../stores/session'
import type { NotificationItem } from '../types'
const notifications = useNotificationStore(), session = useSessionStore(), router = useRouter()
const anyUnread = computed(() => notifications.items.some(item => !item.read))
// 打开页面、换账号时读第一页。
watch(() => session.user?.id, id => { if (id) void notifications.load() }, { immediate: true })
const action = (item: NotificationItem) => item.type === 'comment.created' ? '评论了你的视频' : '赞了你的视频'
function when(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '' : date.toLocaleString('zh-CN', { hour12: false })
}
// 点一条通知：先标记已读，再打开视频。标记失败也照样打开，未读数下次刷新时会更正。
async function open(item: NotificationItem) {
  try { await notifications.markRead(item) } catch { /* 不影响打开视频 */ }
  await router.push(`/videos/${item.video_id}`)
}
async function readAll() {
  try { await notifications.markAllRead() } catch (cause) { session.notify(errorMessage(cause)) }
}
</script>
<template>
  <section aria-labelledby="notifications-title">
    <div class="toolbar">
      <div class="heading"><h2 id="notifications-title">通知</h2><span class="badge">未读 {{ notifications.unread }} 条</span></div>
      <div class="tools">
        <button class="button small" :disabled="!anyUnread" @click="readAll">全部已读</button>
        <button class="icon-button" aria-label="刷新通知" :disabled="notifications.loading" @click="notifications.load()">↻</button>
      </div>
    </div>
    <p v-if="notifications.stale" class="notification-fresh" role="status">有新通知 <button class="text-button" :disabled="notifications.loading" @click="notifications.load()">点击刷新</button></p>
    <p v-if="notifications.error" class="form-message" role="alert">{{ notifications.error }} <button class="text-button" @click="notifications.load()">刷新重试</button></p>
    <div v-if="notifications.loading && !notifications.items.length" class="list-message" role="status">正在加载通知……</div>
    <div v-else-if="!notifications.items.length && !notifications.error" class="empty-state">还没有通知。别人给你的视频点赞或评论时，会出现在这里。</div>
    <ul class="notification-list" :aria-busy="notifications.loading">
      <li v-for="item in notifications.items" :key="item.id">
        <button class="notification-item" :class="{ unread: !item.read }" @click="open(item)">
          <span class="notification-dot" aria-hidden="true"></span>
          <span class="notification-body">
            <span class="notification-text"><strong>{{ item.actor_name }}</strong> {{ action(item) }}《{{ item.video_title }}》</span>
            <span v-if="item.type === 'comment.created'" class="notification-comment">{{ item.comment_content ?? '评论已删除' }}</span>
            <span class="notification-time">{{ when(item.created_at) }}<template v-if="!item.read"> · 未读</template></span>
          </span>
        </button>
      </li>
    </ul>
    <div class="pagination"><span>点一条通知会标记已读并打开视频</span><div>
      <button class="button" :disabled="notifications.loading || !notifications.hasMore" @click="notifications.load(true)">{{ notifications.loading ? '正在加载……' : notifications.hasMore ? (notifications.error ? '重试加载更多' : '加载更多') : '没有更多通知了' }}</button>
    </div></div>
  </section>
</template>
