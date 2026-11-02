<script setup lang="ts">
import type { Video } from '../types'
import { mediaURL, formatDate } from '../lib/media'
defineProps<{ video: Video }>()
function fallback(event: Event) {
  const image = event.target as HTMLImageElement
  if (!image.src.endsWith('/cover.svg')) image.src = '/cover.svg'
}
</script>
<template>
  <article class="video-card">
    <RouterLink class="card-preview" :to="`/videos/${video.id}`" :aria-label="`播放：${video.title}`">
      <img :src="mediaURL(video.cover_url) || '/cover.svg'" alt="" loading="lazy" referrerpolicy="no-referrer" @error="fallback" />
      <span class="play-circle">▶</span><span class="card-id">片段 #{{ video.id }}</span>
    </RouterLink>
    <div class="card-content">
      <RouterLink class="card-title" :to="`/videos/${video.id}`">{{ video.title }}</RouterLink>
      <div class="card-meta"><span class="author-tag"><span class="mini-avatar">{{ String(video.author_id).slice(-2) }}</span>{{ video.author_name || `创作者 ${video.author_id}` }}</span><time>{{ formatDate(video.created_at) }}</time></div>
      <p v-if="video.like_count !== undefined" class="card-likes">{{ video.liked ? '♥' : '♡' }} {{ video.like_count }} 人点赞{{ video.liked ? ' · 已点赞' : '' }}</p>
    </div>
  </article>
</template>
