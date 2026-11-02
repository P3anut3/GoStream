<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSessionStore } from '../stores/session'
import { errorMessage } from '../api/client'
import { mediaURL, formatDate } from '../lib/media'
import type { Video, LikeStatus } from '../types'
import CommentSection from '../components/CommentSection.vue'
const route = useRoute(), router = useRouter(), session = useSessionStore()
const video = ref<Video | null>(null), loading = ref(false), error = ref(''), playbackError = ref('')
const likes = ref<LikeStatus | null>(null), likeLoading = ref(false), likeBusy = ref(false), likeError = ref('')
const deleteBusy = ref(false), deleteError = ref('')
const player = ref<HTMLVideoElement>(), owned = computed(() => !!video.value && video.value.author_id === session.user?.id)
const id = computed(() => String(route.params.id)), url = computed(() => mediaURL(video.value?.play_url))
let sequence = 0, likeSequence = 0
async function load() {
  const seq = ++sequence; likeSequence++; player.value?.pause()
  video.value = null; likes.value = null; loading.value = true; error.value = ''; playbackError.value = ''; deleteError.value = ''
  if (!/^[1-9][0-9]*$/.test(id.value)) { loading.value = false; error.value = '视频 ID 必须是正整数'; return }
  try {
    const result = await session.call<Video>(`/videos/${encodeURIComponent(id.value)}`)
    if (seq !== sequence) return
    video.value = result; void loadLikes()
  } catch (cause) { if (seq === sequence) error.value = errorMessage(cause) }
  finally { if (seq === sequence) loading.value = false }
}
async function loadLikes() {
  const seq = ++likeSequence, revision = session.revision, target = id.value
  likes.value = null; likeError.value = ''; likeLoading.value = false
  if (!session.token || !video.value) return
  likeLoading.value = true
  try {
    const result = await session.call<LikeStatus>(`/videos/${target}/like`, {}, 'required')
    if (seq === likeSequence && revision === session.revision) likes.value = result
  } catch (cause) { if (seq === likeSequence && revision === session.revision) likeError.value = errorMessage(cause) }
  finally { if (seq === likeSequence) likeLoading.value = false }
}
async function toggleLike() {
  if (!session.token) { session.requireLogin(); return }
  if (likeBusy.value || likeLoading.value || !likes.value) return
  const target = id.value, revision = session.revision
  likeBusy.value = true; likeError.value = ''
  try {
    await session.call(`/videos/${target}/like`, { method: likes.value.liked ? 'DELETE' : 'PUT' }, 'required')
    if (target === id.value && revision === session.revision) await loadLikes()
  } catch (cause) { if (target === id.value && revision === session.revision) likeError.value = errorMessage(cause) }
  finally { likeBusy.value = false }
}
async function remove() {
  if (!video.value || !owned.value || deleteBusy.value || !window.confirm('删除这个视频？记录删除后无法恢复，已上传文件仍会保留。')) return
  const target = video.value.id, revision = session.revision
  deleteBusy.value = true; deleteError.value = ''
  try {
    await session.call(`/videos/${target}`, { method: 'DELETE' }, 'required')
    if (revision === session.revision && target === video.value?.id) { session.notify('视频记录已删除，已上传文件仍保留'); await router.push('/mine') }
  } catch (cause) { if (revision === session.revision) deleteError.value = errorMessage(cause) }
  finally { deleteBusy.value = false }
}
watch(id, load, { immediate: true })
watch(() => session.revision, () => { likes.value = null; void loadLikes() })
onUnmounted(() => { sequence++; likeSequence++; player.value?.pause() })
</script>
<template>
  <div class="back-row"><RouterLink class="text-button" to="/">← 返回视频库</RouterLink></div>
  <p v-if="loading" class="list-message" role="status">正在加载视频……</p>
  <div v-if="error" class="empty-state" role="alert">{{ error }} <button class="button" @click="load">重试</button></div>
  <article v-if="video" class="page-panel video-detail">
    <video ref="player" id="detail-player" :src="url || undefined" :poster="mediaURL(video.cover_url) || '/cover.svg'" controls playsinline preload="metadata" @error="playbackError = '视频暂时无法播放，请检查文件或尝试打开原视频。'"></video>
    <div class="video-detail-content"><span class="eyebrow">VIDEO / {{ video.id }}</span><h1>{{ video.title }}</h1><p class="muted">创作者 {{ video.author_id }} · {{ formatDate(video.created_at) }}</p><p class="form-message" role="status">{{ playbackError || (!url ? '视频地址格式不受支持' : '') }}</p>
      <div class="like-controls"><button class="button like-button" :class="{ liked: likes?.liked }" :aria-pressed="!!likes?.liked" :disabled="likeBusy || likeLoading || (!!session.token && !likes)" @click="toggleLike">{{ likes?.liked ? '♥ 已点赞' : '♡ 点赞' }}</button><span aria-live="polite">{{ likeLoading ? '正在读取点赞……' : likes ? `${likes.like_count} 人点赞` : '登录后查看点赞数量' }}</span><button class="text-button" :disabled="likeLoading || likeBusy || !session.token" @click="loadLikes">刷新点赞</button></div><p class="form-message" role="alert">{{ likeError }}</p>
      <div class="detail-actions"><a v-if="url" class="button" :href="url" target="_blank" rel="noopener noreferrer">打开原视频 ↗</a><button v-if="owned" class="button danger" :disabled="deleteBusy" @click="remove">{{ deleteBusy ? '正在删除……' : '删除视频' }}</button></div><p class="form-message" role="alert">{{ deleteError }}</p><p class="fine-print">删除操作仅移除视频记录，当前不会删除已上传文件。</p>
      <CommentSection :key="video.id" :video-id="video.id" />
    </div>
  </article>
</template>
