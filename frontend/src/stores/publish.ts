import { defineStore } from 'pinia'
import { ref, shallowRef, watch } from 'vue'
import { useSessionStore } from './session'
import { isUploadedVideoURL, validateFile } from '../lib/media'
import { errorMessage } from '../api/client'
import type { CoverUpload, VideoUpload } from '../types'

export const usePublishStore = defineStore('publish', () => {
  const session = useSessionStore()
  const title = ref(''), source = ref<'file' | 'url'>('file'), playURL = ref('')
  const file = shallowRef<File | null>(null), cover = shallowRef<File | null>(null)
  const uploaded = ref<VideoUpload | null>(null), uploadedCover = ref<CoverUpload | null>(null)
  const message = ref(''), busy = ref(false)
  function reset() { title.value = ''; source.value = 'file'; playURL.value = ''; file.value = null; cover.value = null; uploaded.value = null; uploadedCover.value = null; message.value = '' }
  watch(() => session.revision, reset, { flush: 'sync' })
  function choose(value: File | undefined, kind: 'video' | 'cover') {
    if (busy.value || !value) return
    const error = validateFile(value, kind)
    if (error) { message.value = error; return }
    message.value = ''
    if (kind === 'video') { file.value = value; uploaded.value = null } else { cover.value = value; uploadedCover.value = null }
  }
  function removeCover() { if (!busy.value) { cover.value = null; uploadedCover.value = null } }
  async function publish(): Promise<boolean> {
    if (busy.value) return false
    message.value = ''
    const normalizedTitle = title.value.trim()
    if (!session.token) { message.value = '请先登录后再发布'; return false }
    if (!normalizedTitle || [...normalizedTitle].length > 100) { message.value = '请输入 1～100 个字符的标题'; return false }
    if (source.value === 'file' && !file.value) { message.value = '请先选择视频文件'; return false }
    if (source.value === 'url' && !isUploadedVideoURL(playURL.value.trim())) { message.value = '请填写本人上传接口返回的 /media/videos/ 视频地址'; return false }
    const revision = session.revision
    const checkSession = () => { if (revision !== session.revision) throw new Error('登录状态已变化，请重新登录后发布') }
    busy.value = true; session.publishing = true
    try {
      let videoURL = playURL.value.trim(), coverURL = '/cover.svg'
      if (source.value === 'file') {
        if (!uploaded.value) {
          message.value = '正在上传视频，请稍候……'
          const data = new FormData(); data.append('file', file.value!)
          const result = await session.call<VideoUpload>('/uploads/videos', { method: 'POST', body: data }, 'required')
          checkSession()
          if (!result?.play_url) throw new Error('上传响应缺少视频地址')
          uploaded.value = result
        }
        videoURL = uploaded.value.play_url
      }
      if (cover.value) {
        if (!uploadedCover.value) {
          message.value = '正在上传封面，请稍候……'
          const data = new FormData(); data.append('file', cover.value)
          const result = await session.call<CoverUpload>('/uploads/covers', { method: 'POST', body: data }, 'required')
          checkSession()
          if (!result?.cover_url) throw new Error('上传响应缺少封面地址')
          uploadedCover.value = result
        }
        coverURL = uploadedCover.value.cover_url
      }
      checkSession(); message.value = '正在发布视频信息……'
      await session.call('/videos', { method: 'POST', body: { title: normalizedTitle, play_url: videoURL, cover_url: coverURL } }, 'required')
      checkSession(); reset(); session.notify('视频已发布，去看看你的新片段吧'); return true
    } catch (cause) {
      if (revision === session.revision) message.value = errorMessage(cause) + (uploaded.value || uploadedCover.value ? '。已上传文件会在本次登录中复用，可重试。' : '')
      return false
    } finally { busy.value = false; session.publishing = false }
  }
  return { title, source, playURL, file, cover, uploaded, uploadedCover, message, busy, choose, removeCover, publish, reset }
})
