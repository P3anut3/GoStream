import { defineStore } from 'pinia'
import { ref } from 'vue'
import { ApiError, errorMessage } from '../api/client'
import type { FeedResponse, Video, VideoPage } from '../types'
import { useSessionStore } from './session'

export const useLibraryStore = defineStore('library', () => {
  const session = useSessionStore()
  const videos = ref<Video[]>([])
  const scope = ref<'all' | 'mine'>('all')
  const size = ref(6)
  const page = ref(1)
  const nextCursor = ref('')
  const hasMore = ref(false)
  const cursorInvalid = ref(false)
  const loading = ref(false)
  const error = ref('')
  let sequence = 0
  function invalidate() { sequence++; loading.value = false }
  async function load(append = false) {
    if (append && (loading.value || scope.value !== 'all' || !hasMore.value || cursorInvalid.value)) return
    const currentSequence = ++sequence, revision = session.revision, mine = scope.value === 'mine'
    const cursor = append ? nextCursor.value : ''
    const current = () => currentSequence === sequence && revision === session.revision
    loading.value = true; error.value = ''
    if (!append) {
      videos.value = []; nextCursor.value = ''; hasMore.value = false; cursorInvalid.value = false
    }
    try {
      const query = mine ? `/me/videos?page=${page.value}&page_size=${size.value}` : `/feed?${new URLSearchParams({ limit: String(size.value), ...(cursor ? { cursor } : {}) })}`
      const data = await session.call<FeedResponse | VideoPage>(query, {}, mine ? 'required' : 'optional')
      if (!current()) return
      if (!Array.isArray(data?.videos)) throw new Error('视频列表响应格式异常')
      if (!mine) {
        const feed = data as FeedResponse
        if (typeof feed.next_cursor !== 'string' || typeof feed.has_more !== 'boolean' || feed.has_more !== Boolean(feed.next_cursor) || (append && feed.has_more && feed.next_cursor === cursor)) {
          throw new Error('视频流分页信息异常，请刷新列表')
        }
        nextCursor.value = feed.next_cursor; hasMore.value = feed.has_more
      }
      const unique = new Map<number, Video>()
      for (const item of append ? [...videos.value, ...data.videos] : data.videos) unique.set(item.id, item)
      videos.value = [...unique.values()]
    } catch (cause) {
      if (!current()) return
      error.value = errorMessage(cause)
      cursorInvalid.value = cause instanceof ApiError && cause.code === 'INVALID_CURSOR'
    } finally { if (current()) loading.value = false }
  }
  function reset(target: 'all' | 'mine') { invalidate(); scope.value = target; page.value = 1; return load() }
  return { videos, scope, size, page, nextCursor, hasMore, cursorInvalid, loading, error, load, reset, invalidate }
})
