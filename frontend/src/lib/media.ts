export function mediaURL(value: string | undefined): string {
  if (!value) return ''
  try {
    const url = new URL(value, globalThis.location?.origin || 'http://localhost')
    return ['http:', 'https:'].includes(url.protocol) ? url.href : ''
  } catch { return '' }
}
export const formatDate = (value: string) => {
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? '时间未知' : date.toLocaleDateString('zh-CN')
}
export function validateFile(file: File, kind: 'video' | 'cover'): string {
  if (!file.size) return '不能上传空文件'
  if (kind === 'video') {
    if (!/\.mp4$/i.test(file.name) || (file.type && file.type !== 'video/mp4')) return '请选择 MP4 视频'
    if (file.size > 100 * 1024 * 1024) return '视频不能超过 100 MiB'
  } else {
    if (!/\.(jpe?g|png)$/i.test(file.name) || (file.type && !['image/jpeg', 'image/png'].includes(file.type))) return '封面仅支持 JPEG、PNG'
    if (file.size > 5 * 1024 * 1024) return '封面不能超过 5 MiB'
  }
  return ''
}
export const isUploadedVideoURL = (value: string) => /^\/media\/videos\/[^/\\?#%]+$/.test(value)
