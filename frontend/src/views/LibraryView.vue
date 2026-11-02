<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useLibraryStore } from '../stores/library'
import { useSessionStore } from '../stores/session'
import VideoCard from '../components/VideoCard.vue'
const route = useRoute(), router = useRouter(), library = useLibraryStore(), session = useSessionStore()
const mine = computed(() => route.path === '/mine'), lookup = ref('')
watch([mine, () => session.revision], () => { void library.reset(mine.value ? 'mine' : 'all') }, { immediate: true })
onUnmounted(() => library.invalidate())
function resize() { library.page = 1; void library.load() }
function changePage(step: number) { library.page += step; void library.load() }
function find() { if (/^[1-9][0-9]*$/.test(lookup.value)) void router.push(`/videos/${lookup.value}`) }
</script>
<template>
  <section class="intro"><div><span class="eyebrow">YOUR VIDEO SPACE</span><h1>每一个片段，都值得分享<span class="orange">。</span></h1><p>上传、整理和播放，把你的故事留在这里。</p></div><RouterLink class="button primary" to="/publish">＋ 发布视频</RouterLink></section>
  <section class="feature-strip"><span class="strip-icon">↗</span><div><strong>从灵感，到你的第一支视频</strong><p>选择一个 MP4，添加标题，即可开始。单个文件最大 100 MiB。</p></div><RouterLink class="text-button" to="/publish">开始上传 →</RouterLink></section>
  <section aria-labelledby="library-title">
    <div class="toolbar"><div class="heading"><h2 id="library-title">{{ mine ? '我的视频' : '视频库' }}</h2><span class="badge">{{ mine ? '本页' : '已加载' }} {{ library.videos.length }} 条</span></div><div class="tools"><form class="lookup" @submit.prevent="find"><label class="sr-only" for="lookup-id">按视频 ID 查找</label><input id="lookup-id" v-model="lookup" inputmode="numeric" pattern="[1-9][0-9]*" required placeholder="输入视频 ID" /><button aria-label="查找视频">↗</button></form><button class="icon-button" aria-label="刷新列表" @click="library.load()">↻</button></div></div>
    <p v-if="library.error" class="form-message" role="alert">{{ library.error }} <button class="text-button" @click="library.load()">刷新重试</button></p>
    <div v-if="library.loading && !library.videos.length" class="list-message" role="status">正在加载视频……</div>
    <div v-else-if="!library.videos.length && !library.error" class="empty-state">{{ mine ? '这一页暂无作品，可以发布视频或返回上一页。' : '这里还没有视频。发布你的第一个片段吧。' }}</div>
    <div class="video-grid" :aria-busy="library.loading"><VideoCard v-for="video in library.videos" :key="video.id" :video="video" /></div>
    <div class="pagination"><span>{{ mine ? `第 ${library.page} 页 · 我的作品` : '最新视频 · 刷新获取新作品' }}</span><div>
      <label>{{ mine ? '每页' : '每批' }} <select v-model.number="library.size" aria-label="每批视频数量" :disabled="library.loading" @change="resize"><option :value="6">6 条</option><option :value="12">12 条</option><option :value="24">24 条</option></select></label>
      <template v-if="mine"><button class="icon-button" aria-label="上一页" :disabled="library.loading || library.page <= 1" @click="changePage(-1)">←</button><span>{{ library.page }}</span><button class="icon-button" aria-label="下一页" :disabled="library.loading || !!library.error || library.videos.length < library.size || library.page >= 10000" @click="changePage(1)">→</button></template>
      <button v-else class="button" :disabled="library.loading || !library.hasMore || library.cursorInvalid" @click="library.load(true)">{{ library.loading ? '正在加载……' : library.cursorInvalid ? '请刷新列表' : library.hasMore ? (library.error ? '重试加载更多' : '加载更多') : '没有更多视频了' }}</button>
    </div></div>
  </section>
</template>
