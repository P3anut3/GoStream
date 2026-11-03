import { test, expect, type Page } from '@playwright/test'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'

// The Go Compose harness sets this only for its disposable random project.
test.skip(process.env.GOSTREAM_ISOLATED_BROWSER !== '1', 'run through the isolated Go Compose harness')
const fixture = fileURLToPath(new URL('../fixtures/playable.mp4', import.meta.url))
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=', 'base64')

async function register(page: Page, username: string) {
  await page.getByRole('button', { name: '登录 / 注册', exact: true }).click()
  await page.getByRole('button', { name: '注册账号', exact: true }).click()
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill('isolated-test-password-123')
  const [response] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === '/login' && response.request().method() === 'POST'),
    page.locator('.auth-modal button[type="submit"]').click(),
  ])
  expect(response.status()).toBe(200)
  await expect(page.locator('#account-name')).toHaveText(username)
  return await response.json() as { token: string; id: number }
}

test('real Vue → Gin → MySQL/media: publish/play, feed paging, two-account interaction and ownership', async ({ page, browser, baseURL }, testInfo) => {
  test.setTimeout(90000)
  const errors: string[] = []
  page.on('pageerror', error => errors.push(error.message))
  const name = `vue_${Date.now().toString(36)}`
  await page.goto('/')
  const alice = await register(page, `${name}_a`)
  await page.locator('.intro a[href="#/publish"]').click()
  await page.locator('#video-file').setInputFiles(fixture)
  await page.locator('#cover-file').setInputFiles({ name: 'cover.png', mimeType: 'image/png', buffer: png })
  const title = 'Vue 迁移验收 · 第一支视频'
  await page.getByLabel('视频标题', { exact: true }).fill(title)
  const [publishedResponse] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === '/videos' && response.request().method() === 'POST'),
    page.getByRole('button', { name: '上传并发布', exact: true }).click(),
  ])
  expect(publishedResponse.status()).toBe(201)
  expect(publishedResponse.request().postDataJSON()).not.toHaveProperty('author_id')
  const published = await publishedResponse.json() as { id: number }
  await expect(page.locator('.video-card')).toHaveCount(1)
  await page.locator('.card-title').click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(title)
  const player = page.locator('#detail-player')
  await expect.poll(() => player.evaluate(node => (node as HTMLVideoElement).readyState)).toBeGreaterThanOrEqual(2)
  await player.evaluate(async node => { const video = node as HTMLVideoElement; video.muted = true; await video.play() })
  await expect.poll(() => player.evaluate(node => (node as HTMLVideoElement).currentTime)).toBeGreaterThan(0.1)
  await player.evaluate(node => (node as HTMLVideoElement).pause())
  const media = await player.getAttribute('src'); expect(media).toBeTruthy()
  const range = await page.request.get(media!, { headers: { Range: 'bytes=0-15' } })
  expect(range.status()).toBe(206); expect((await range.body()).length).toBe(16)
  await page.getByRole('button', { name: '♡ 点赞', exact: true }).click()
  await expect(page.getByText('1 人点赞', { exact: true })).toBeVisible()
  await page.getByLabel('写下你的评论').fill('账号 A 的真实评论')
  const [commentResponse] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === `/videos/${published.id}/comments` && response.request().method() === 'POST'),
    page.getByRole('button', { name: '发表评论', exact: true }).click(),
  ])
  const comment = await commentResponse.json() as { id: number }
  await expect(page.locator('.comment-text')).toHaveText('账号 A 的真实评论')
  const videoDetail = await (await page.request.get(`/videos/${published.id}`)).json()

  const bytes = await readFile(fixture)
  for (let i = 0; i < 6; i++) {
    const upload = await page.request.post('/uploads/videos', { headers: { Authorization: `Bearer ${alice.token}` }, multipart: { file: { name: `seed-${i}.mp4`, mimeType: 'video/mp4', buffer: bytes } } })
    expect(upload.status()).toBe(200)
    const created = await page.request.post('/videos', { headers: { Authorization: `Bearer ${alice.token}` }, data: { title: `分页验收 ${i + 1}`, play_url: (await upload.json()).play_url, cover_url: '/cover.svg' } })
    expect(created.status()).toBe(201)
  }
  await page.locator('#nav-library').click(); await expect(page.locator('.video-card')).toHaveCount(6)
  await page.getByRole('button', { name: '加载更多', exact: true }).click(); await expect(page.locator('.video-card')).toHaveCount(7)
  await expect(page.getByRole('button', { name: '没有更多视频了' })).toBeDisabled()
  await page.evaluate(() => window.scrollTo(0, 0))
  await page.screenshot({ path: testInfo.outputPath('gostream-vue-desktop.png'), fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.locator('#nav-mine')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
  await page.screenshot({ path: testInfo.outputPath('gostream-vue-mobile.png'), fullPage: true })
  await page.setViewportSize({ width: 1280, height: 720 })
  await page.locator('#nav-mine').click(); await expect(page.locator('.video-card')).toHaveCount(6)
  await page.getByRole('button', { name: '下一页', exact: true }).click(); await expect(page.locator('.video-card')).toHaveCount(1)

  const context = await browser.newContext({ baseURL })
  try {
    const bobPage = await context.newPage(); await bobPage.goto('/')
    const bob = await register(bobPage, `${name}_b`)
    await bobPage.locator('#nav-mine').click(); await expect(bobPage.locator('.video-card')).toHaveCount(0)
    await bobPage.goto(`/#/videos/${published.id}`)
    await expect(bobPage.getByRole('heading', { level: 1 })).toHaveText(title)
    await expect(bobPage.getByRole('button', { name: '删除视频', exact: true })).toHaveCount(0)
    await expect(bobPage.getByRole('button', { name: '删除评论', exact: true })).toHaveCount(0)
    const headers = { Authorization: `Bearer ${bob.token}` }
    expect((await bobPage.request.delete(`/videos/${published.id}`, { headers })).status()).toBe(404)
    expect((await bobPage.request.delete(`/comments/${comment.id}`, { headers })).status()).toBe(404)
    expect((await bobPage.request.post('/videos', { headers, data: { title: '越权发布', play_url: videoDetail.play_url, cover_url: '/cover.svg' } })).status()).toBe(403)
    await bobPage.getByRole('button', { name: '♡ 点赞', exact: true }).click()
    await expect(bobPage.getByText('2 人点赞', { exact: true })).toBeVisible()
    await bobPage.getByLabel('写下你的评论').fill('账号 B 的真实评论')
    await bobPage.getByRole('button', { name: '发表评论', exact: true }).click()
    await expect(bobPage.locator('.comment-item')).toHaveCount(2)
    bobPage.once('dialog', dialog => dialog.accept())
    await bobPage.getByRole('button', { name: '删除评论', exact: true }).click()
    await expect(bobPage.locator('.comment-item')).toHaveCount(1)
    // B follows A from A's video; the relation is directed and survives navigation.
    const follow = bobPage.locator('.follow-button')
    await expect(follow).toBeEnabled(); await follow.click()
    await expect(follow).toHaveText('✓ 已关注'); await expect(follow).toHaveAttribute('aria-pressed', 'true')
    expect(await (await bobPage.request.get(`/users/${alice.id}/follow`, { headers })).json()).toEqual({ following: true })
    expect(await (await bobPage.request.get(`/users/${bob.id}/follow`, { headers: { Authorization: `Bearer ${alice.token}` } })).json()).toEqual({ following: false })
    // B publishes one video; it is in the latest feed but not in B's own following feed.
    const bobUpload = await bobPage.request.post('/uploads/videos', { headers, multipart: { file: { name: 'bob.mp4', mimeType: 'video/mp4', buffer: bytes } } })
    expect(bobUpload.status()).toBe(200)
    expect((await bobPage.request.post('/videos', { headers, data: { title: '账号 B 的作品', play_url: (await bobUpload.json()).play_url, cover_url: '/cover.svg' } })).status()).toBe(201)
    await bobPage.locator('#nav-following').click()
    await expect(bobPage.getByRole('heading', { name: '关注', exact: true })).toBeVisible()
    await expect(bobPage.locator('.video-card')).toHaveCount(6)
    await bobPage.getByRole('button', { name: '加载更多', exact: true }).click(); await expect(bobPage.locator('.video-card')).toHaveCount(7)
    await expect(bobPage.getByRole('button', { name: '没有更多视频了' })).toBeDisabled()
    await expect(bobPage.getByText('账号 B 的作品', { exact: true })).toHaveCount(0)
    await bobPage.locator('#nav-library').click()
    await expect(bobPage.locator('.card-title').first()).toHaveText('账号 B 的作品')
    await bobPage.goto(`/#/videos/${published.id}`)
    await expect(follow).toHaveText('✓ 已关注')
    await follow.click()
    await expect(follow).toHaveAttribute('aria-pressed', 'false'); await expect(follow).toBeEnabled()
    expect(await (await bobPage.request.get(`/users/${alice.id}/follow`, { headers })).json()).toEqual({ following: false })
    // After unfollowing, a fresh following feed is empty.
    await bobPage.locator('#nav-following').click()
    await expect(bobPage.locator('.empty-state')).toContainText('关注的作者还没有作品')
    await expect(bobPage.locator('.video-card')).toHaveCount(0)
  } finally { await context.close() }

  await page.goto(`/#/videos/${published.id}`)
  await expect(page.getByText('2 人点赞', { exact: true })).toBeVisible()
  await expect(page.locator('.follow-button')).toHaveCount(0) // authors cannot follow themselves
  page.once('dialog', dialog => dialog.accept())
  await page.getByRole('button', { name: '删除视频', exact: true }).click()
  await expect(page).toHaveURL(/#\/mine$/)
  expect((await page.request.get(`/videos/${published.id}`)).status()).toBe(404)
  expect((await page.request.get(media!)).status()).toBe(200)
  expect(errors).toEqual([])
})
