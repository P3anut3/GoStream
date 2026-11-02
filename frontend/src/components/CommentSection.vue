<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useSessionStore } from '../stores/session'
import { errorMessage } from '../api/client'
import { formatDate } from '../lib/media'
import type { Comment, CommentPage } from '../types'
const props = defineProps<{ videoId: number }>(), session = useSessionStore()
const comments = ref<Comment[]>([]), page = ref(1), loading = ref(false), busy = ref(false), hasNext = ref(false)
const content = ref(''), listError = ref(''), message = ref('')
const count = computed(() => [...content.value.trim()].length)
let sequence = 0, mutation = 0
async function load() {
  const seq = ++sequence
  comments.value = []; loading.value = true; hasNext.value = false; listError.value = ''
  try {
    const result = await session.call<CommentPage>(`/videos/${props.videoId}/comments?page=${page.value}&page_size=5`)
    if (seq !== sequence) return
    if (!Array.isArray(result?.comments)) throw new Error('评论列表响应格式异常')
    comments.value = result.comments; hasNext.value = comments.value.length === 5
  } catch (cause) { if (seq === sequence) listError.value = errorMessage(cause) }
  finally { if (seq === sequence) loading.value = false }
}
function changePage(step: number) { page.value += step; void load() }
async function submit() {
  if (!session.token) { session.requireLogin(); return }
  if (busy.value) return
  if (!count.value || count.value > 500) { message.value = '评论需为 1～500 个字符'; return }
  const revision = session.revision, version = ++mutation, text = content.value.trim()
  busy.value = true; message.value = ''
  try {
    await session.call(`/videos/${props.videoId}/comments`, { method: 'POST', body: { content: text } }, 'required')
    if (revision !== session.revision || version !== mutation) return
    content.value = ''; message.value = '评论已发表'; page.value = 1; await load()
  } catch (cause) { if (revision === session.revision && version === mutation) message.value = errorMessage(cause) }
  finally { if (version === mutation) busy.value = false }
}
async function remove(comment: Comment) {
  if (busy.value || comment.user_id !== session.user?.id || !window.confirm('删除这条评论？删除后无法恢复。')) return
  const revision = session.revision, version = ++mutation
  busy.value = true; message.value = ''
  try {
    await session.call(`/comments/${comment.id}`, { method: 'DELETE' }, 'required')
    if (revision !== session.revision || version !== mutation) return
    if (comments.value.length === 1 && page.value > 1) page.value--
    await load(); message.value = '评论已删除'
  } catch (cause) { if (revision === session.revision && version === mutation) message.value = errorMessage(cause) }
  finally { if (version === mutation) busy.value = false }
}
watch(() => props.videoId, () => { page.value = 1; content.value = ''; message.value = ''; void load() }, { immediate: true })
watch(() => session.revision, () => { mutation++; busy.value = false; content.value = ''; message.value = ''; void load() })
onUnmounted(() => { sequence++; mutation++ })
</script>
<template>
  <section class="comments-section" aria-labelledby="comments-title">
    <div class="comments-heading"><h2 id="comments-title">评论</h2><span class="badge">本页 {{ comments.length }} 条</span><button class="text-button" :disabled="loading || busy" @click="load">刷新评论</button></div>
    <p v-if="loading" class="muted" role="status">正在读取评论……</p><p v-else-if="listError" class="form-message" role="alert">{{ listError }}</p><p v-else-if="!comments.length" class="muted">这一页还没有评论。</p>
    <div aria-live="polite"><article v-for="comment in comments" :key="comment.id" class="comment-item"><div class="comment-meta"><strong>用户 {{ comment.user_id }}</strong><time>{{ formatDate(comment.created_at) }}</time><button v-if="session.user?.id === comment.user_id" class="text-button comment-delete" :disabled="busy" @click="remove(comment)">删除评论</button></div><p class="comment-text">{{ comment.content }}</p></article></div>
    <div class="comments-pagination"><button class="button small" :disabled="loading || busy || page <= 1" @click="changePage(-1)">上一页</button><span>第 {{ page }} 页</span><button class="button small" :disabled="loading || busy || !hasNext || page >= 10000" @click="changePage(1)">下一页</button></div>
    <form @submit.prevent="submit"><label for="comment-content">写下你的评论</label><textarea id="comment-content" v-model="content" :disabled="busy || !session.token" rows="3" maxlength="1000" :placeholder="session.token ? '评论内容，最多 500 个字符' : '登录后发表评论'"></textarea><div class="comment-form-footer"><span class="muted">{{ count }} / 500 字符</span><button type="submit" class="button primary" :disabled="busy">{{ busy ? '正在处理……' : session.token ? '发表评论' : '登录后评论' }}</button></div><p class="fine-print">评论公开可见，仅评论作者可以删除。</p><p class="form-message" role="status">{{ message }}</p></form>
  </section>
</template>
