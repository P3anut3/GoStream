<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { onBeforeRouteLeave, useRouter } from 'vue-router'
import { usePublishStore } from '../stores/publish'
import { useSessionStore } from '../stores/session'
const draft = usePublishStore(), session = useSessionStore(), router = useRouter()
const preview = ref(''), coverPreview = ref(''), dragging = ref(false)
watch(() => draft.file, file => { if (preview.value) URL.revokeObjectURL(preview.value); preview.value = file ? URL.createObjectURL(file) : '' }, { immediate: true })
watch(() => draft.cover, file => { if (coverPreview.value) URL.revokeObjectURL(coverPreview.value); coverPreview.value = file ? URL.createObjectURL(file) : '' }, { immediate: true })
function input(event: Event, kind: 'video' | 'cover') {
  const element = event.target as HTMLInputElement
  draft.choose(element.files?.[0], kind); element.value = ''
}
function drop(event: DragEvent) { dragging.value = false; draft.choose(event.dataTransfer?.files[0], 'video') }
async function submit() { if (await draft.publish()) await router.push('/') }
function beforeUnload(event: BeforeUnloadEvent) { if (draft.busy) { event.preventDefault(); event.returnValue = '' } }
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onBeforeRouteLeave(() => { if (draft.busy && session.token) { session.notify('正在处理发布，请稍候'); return false } })
onBeforeUnmount(() => { window.removeEventListener('beforeunload', beforeUnload); if (preview.value) URL.revokeObjectURL(preview.value); if (coverPreview.value) URL.revokeObjectURL(coverPreview.value) })
</script>
<template>
  <section class="intro"><div><span class="eyebrow">CREATE SOMETHING</span><h1>发布一个新片段<span class="orange">。</span></h1><p>让一个瞬间，成为一个故事。</p></div></section>
  <section class="page-panel publish-panel">
    <form @submit.prevent="submit"><fieldset :disabled="draft.busy">
      <div class="tabs"><button type="button" :class="{ selected: draft.source === 'file' }" @click="draft.source = 'file'">上传文件</button><button type="button" :class="{ selected: draft.source === 'url' }" @click="draft.source = 'url'">使用已上传地址</button></div>
      <div v-if="draft.source === 'file'">
        <label class="drop-zone" :class="{ dragover: dragging }" for="video-file" @dragover.prevent="dragging = true" @dragleave="dragging = false" @drop.prevent="drop"><span class="upload-icon">↑</span><strong>{{ draft.file?.name || '点击选择，或拖入视频文件' }}</strong><span>MP4 格式 · 最大 100 MiB</span><input id="video-file" type="file" accept=".mp4,video/mp4" @change="input($event, 'video')" /></label>
        <video v-if="preview" id="upload-preview" :src="preview" controls playsinline preload="metadata"></video>
      </div>
      <template v-else><label for="play-url">视频地址</label><input id="play-url" v-model="draft.playURL" required maxlength="1024" placeholder="/media/videos/…（本人上传）" /></template>
      <label for="video-title">视频标题</label><input id="video-title" v-model="draft.title" required maxlength="200" placeholder="为这个片段起个名字" /><p class="fine-print">最多 100 个字符，中文和表情均按字符计算。</p>
      <label for="cover-file">视频封面 <span class="optional">选填</span></label>
      <div class="cover-picker"><input id="cover-file" type="file" accept=".jpg,.jpeg,.png,image/jpeg,image/png" @change="input($event, 'cover')" /><p class="fine-print">{{ draft.cover?.name || '未选择，使用默认封面。' }}</p><img v-if="coverPreview" id="cover-preview" :src="coverPreview" alt="所选视频封面预览" /><button v-if="draft.cover" type="button" class="text-button" @click="draft.removeCover">移除图片，使用默认封面</button></div>
      <p class="fine-print">支持 JPEG、PNG，最大 5 MiB。</p>
      <p class="form-message" role="status">{{ draft.message }}</p>
      <button type="submit" class="button primary full">{{ draft.busy ? '正在处理……' : draft.source === 'file' ? '上传并发布' : '发布视频' }}</button>
      <p class="fine-print">上传与发布分两步完成。发布失败可重试；离开页面暂存本次草稿，刷新后丢失。未发布文件按现有清理规则处理。</p>
    </fieldset></form>
  </section>
</template>
