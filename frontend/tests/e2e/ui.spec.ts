import { test, expect, type Page, type Route } from '@playwright/test'

const video = (id: number, author = 1) => ({ id, author_id: author, title: `测试作品 ${id}`, play_url: '/media/videos/a.mp4', cover_url: '/cover.svg', created_at: '2026-10-17T12:00:00+08:00', author_name: '测试作者', like_count: 0, liked: false })
const ok = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
async function mockAPI(page: Page, override?: (route: Route, path: string, method: string) => Promise<boolean>) {
  let liked = false, following = false, comments: any[] = []
  await page.route('**/*', async route => {
    const url = new URL(route.request().url()), path = url.pathname, method = route.request().method()
    if (override && await override(route, path, method)) return
    if (path === '/feed') return ok(route, { videos: [video(1)], next_cursor: '', has_more: false })
    if (path === '/feed/following') return ok(route, { videos: [], next_cursor: '', has_more: false })
    if (path === '/users' && method === 'POST') return ok(route, { id: 1, username: 'alice' }, 201)
    if (path === '/login') return ok(route, { id: 1, username: 'alice', token: 'test-issued-token' })
    if (path === '/me') return ok(route, { id: 1, username: 'alice' })
    if (path === '/me/videos') return ok(route, { videos: [video(1)], page: 1, page_size: 6 })
    if (/^\/videos\/\d+$/.test(path) && method === 'GET') return ok(route, video(Number(path.split('/')[2])))
    if (/^\/videos\/\d+\/like$/.test(path)) {
      if (method === 'GET') return ok(route, { like_count: liked ? 1 : 0, liked })
      liked = method === 'PUT'; return route.fulfill({ status: 204 })
    }
    if (/^\/users\/\d+\/follow$/.test(path)) {
      if (method === 'GET') return ok(route, { following })
      following = method === 'PUT'; return route.fulfill({ status: 204 })
    }
    if (/^\/videos\/\d+\/comments$/.test(path)) {
      if (method === 'POST') { const body = route.request().postDataJSON(); comments = [{ id: 1, video_id: 1, user_id: 1, content: body.content, created_at: '2026-10-17' }, ...comments]; return ok(route, comments[0], 201) }
      return ok(route, { comments, page: 1, page_size: 5, video_id: 1 })
    }
    if (path === '/comments/1' && method === 'DELETE') { comments = []; return route.fulfill({ status: 204 }) }
    await route.continue()
  })
}
async function login(page: Page) {
  await page.getByRole('button', { name: '登录 / 注册', exact: true }).click()
  await page.getByLabel('用户名', { exact: true }).fill('alice')
  await page.getByLabel('密码', { exact: true }).fill('test-password-123')
  await page.locator('.auth-modal button[type="submit"]').click()
  await expect(page.locator('#account-name')).toHaveText('alice')
}

test('cursor append failure retains the first batch and retries the same cursor', async ({ page }) => {
  const cursors: string[] = []; let failed = false
  await mockAPI(page, async (route, path) => {
    if (path !== '/feed') return false
    const cursor = new URL(route.request().url()).searchParams.get('cursor') || ''; cursors.push(cursor)
    if (!cursor) await ok(route, { videos: [video(2)], next_cursor: 'opaque+/=', has_more: true })
    else if (!failed) { failed = true; await ok(route, { error: '暂时失败' }, 500) }
    else await ok(route, { videos: [video(1)], next_cursor: '', has_more: false })
    return true
  })
  await page.goto('/')
  await expect(page.locator('.video-card')).toHaveCount(1)
  await page.getByRole('button', { name: '加载更多', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('暂时失败')
  await expect(page.locator('.video-card')).toHaveCount(1)
  await page.getByRole('button', { name: '重试加载更多', exact: true }).click()
  await expect(page.locator('.video-card')).toHaveCount(2)
  await expect(page.getByRole('button', { name: '没有更多视频了' })).toBeDisabled()
  expect(cursors).toEqual(['', 'opaque+/=', 'opaque+/='])
  await page.getByRole('button', { name: '刷新列表' }).click()
  await expect(page.locator('.video-card')).toHaveCount(1)
})

test('protected routes open login, personal pagination is independent, refresh clears credentials', async ({ page }) => {
  const paths: string[] = []
  await mockAPI(page, async (route, path) => {
    if (path === '/me/videos') {
      expect(route.request().headers().authorization).toBe('Bearer test-issued-token')
      paths.push(route.request().url()); await ok(route, { videos: [], page: 1, page_size: 6 }); return true
    }
    return false
  })
  await page.goto('/#/mine')
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('test-password-123')
  await page.locator('.auth-modal button[type="submit"]').click()
  await expect(page).toHaveURL(/#\/mine$/)
  await expect(page.getByRole('heading', { name: '我的视频', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '上一页', exact: true })).toBeDisabled()
  expect(paths[0]).toContain('page=1&page_size=6'); expect(paths[0]).not.toContain('cursor')
  await page.reload()
  await expect(page.locator('#account-name')).toHaveText('访客模式')
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length }))).toEqual({ local: 0, session: 0 })
})

test('publish retries reuse uploaded video and cover; payload contains no author ID', async ({ page }) => {
  let uploads = 0, covers = 0, publishes = 0
  await mockAPI(page, async (route, path, method) => {
    if (path === '/uploads/videos') { uploads++; expect(route.request().headers()['content-type']).toContain('multipart/form-data; boundary='); await ok(route, { play_url: '/media/videos/a.mp4' }); return true }
    if (path === '/uploads/covers') { covers++; await ok(route, { cover_url: '/media/covers/a.png' }); return true }
    if (path === '/videos' && method === 'POST') {
      publishes++; expect(route.request().postDataJSON()).toEqual({ title: '我的 Vue 作品', play_url: '/media/videos/a.mp4', cover_url: '/media/covers/a.png' })
      await ok(route, publishes === 1 ? { error: '临时发布失败' } : { id: 2 }, publishes === 1 ? 500 : 201); return true
    }
    return false
  })
  await page.goto('/'); await login(page)
  await page.locator('.intro a[href="#/publish"]').click()
  await page.locator('#video-file').setInputFiles({ name: 'a.mp4', mimeType: 'video/mp4', buffer: Buffer.from('fixture') })
  await page.locator('#cover-file').setInputFiles({ name: 'a.png', mimeType: 'image/png', buffer: Buffer.from('fixture') })
  await page.getByLabel('视频标题', { exact: true }).fill('我的 Vue 作品')
  await page.getByRole('button', { name: '上传并发布', exact: true }).click()
  await expect(page.getByRole('status').filter({ hasText: '临时发布失败' })).toContainText('复用')
  await page.getByRole('button', { name: '上传并发布', exact: true }).click()
  await expect(page).toHaveURL(/#\/$/)
  expect({ uploads, covers, publishes }).toEqual({ uploads: 1, covers: 1, publishes: 2 })
})

test('detail uses escaped text and supports authenticated like/comment/delete', async ({ page }) => {
  await mockAPI(page, async (route, path, method) => {
    if (path === '/videos/1' && method === 'GET') { await ok(route, { ...video(1), title: '<img src=x onerror="window.injected=1">' }); return true }
    return false
  })
  await page.goto('/'); await login(page); await page.goto('/#/videos/1')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('<img src=x onerror="window.injected=1">')
  expect(await page.evaluate(() => (window as any).injected)).toBeUndefined()
  await expect(page.locator('#detail-player')).toHaveAttribute('src', /\/media\/videos\/a.mp4/)
  await page.getByRole('button', { name: '♡ 点赞', exact: true }).click()
  await expect(page.getByRole('button', { name: '♥ 已点赞', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await page.getByLabel('写下你的评论').fill('第一条评论 <script>不会执行</script>')
  await page.getByRole('button', { name: '发表评论', exact: true }).click()
  await expect(page.locator('.comment-text')).toHaveText('第一条评论 <script>不会执行</script>')
  page.once('dialog', dialog => dialog.accept())
  await page.getByRole('button', { name: '删除评论', exact: true }).click()
  await expect(page.locator('.comment-item')).toHaveCount(0)
  await page.getByRole('button', { name: '退出', exact: true }).click()
  await expect(page.getByRole('button', { name: '删除视频', exact: true })).toHaveCount(0)
  await expect(page.getByLabel('写下你的评论')).toBeDisabled()
})

test('mobile navigation remains usable and invalid cursor requires refresh', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockAPI(page, async (route, path) => {
    if (path !== '/feed') return false
    const more = new URL(route.request().url()).searchParams.has('cursor')
    await ok(route, more ? { error: '游标无效，请刷新', code: 'INVALID_CURSOR' } : { videos: [video(1)], next_cursor: 'stale', has_more: true }, more ? 400 : 200); return true
  })
  await page.goto('/'); await page.getByRole('button', { name: '加载更多', exact: true }).click()
  await expect(page.getByRole('button', { name: '请刷新列表', exact: true })).toBeDisabled()
  await expect(page.locator('#nav-mine')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
})

test('author follow button: guests log in first, state comes from the server, own videos hide it', async ({ page }) => {
  const calls: string[] = []
  let following = false
  await mockAPI(page, async (route, path, method) => {
    if (path === '/videos/2' && method === 'GET') { await ok(route, video(2, 2)); return true }
    if (path === '/users/2/follow') {
      calls.push(method)
      expect(route.request().headers().authorization).toBe('Bearer test-issued-token')
      if (method === 'GET') await ok(route, { following })
      else { following = method === 'PUT'; await route.fulfill({ status: 204 }) }
      return true
    }
    return false
  })
  await page.goto('/#/videos/2')
  await expect(page.getByText('登录后关注作者', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '+ 关注', exact: true }).click()
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(calls).toEqual([])
  await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('test-password-123')
  await page.locator('.auth-modal button[type="submit"]').click()
  const follow = page.locator('.follow-button')
  await expect(follow).toBeEnabled(); await expect(follow).toHaveText('+ 关注'); await expect(follow).toHaveAttribute('aria-pressed', 'false')
  await follow.click()
  await expect(follow).toHaveText('✓ 已关注'); await expect(follow).toHaveAttribute('aria-pressed', 'true'); await expect(follow).toBeEnabled()
  await follow.click()
  await expect(follow).toHaveAttribute('aria-pressed', 'false'); await expect(follow).toBeEnabled(); await expect(follow).toHaveText('+ 关注')
  // Every change is confirmed by a fresh GET instead of flipping the button locally.
  expect(calls).toEqual(['GET', 'PUT', 'GET', 'DELETE', 'GET'])
  await page.goto('/#/videos/1')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('测试作品 1')
  await expect(page.locator('.follow-button')).toHaveCount(0)
})

test('follow status failure keeps the button disabled until a retry succeeds', async ({ page }) => {
  let attempts = 0
  await mockAPI(page, async (route, path, method) => {
    if (path === '/videos/2' && method === 'GET') { await ok(route, video(2, 2)); return true }
    if (path === '/users/2/follow' && method === 'GET') {
      attempts++; await ok(route, attempts === 1 ? { error: '查询关注状态失败' } : { following: true }, attempts === 1 ? 500 : 200); return true
    }
    return false
  })
  await page.goto('/'); await login(page); await page.goto('/#/videos/2')
  await expect(page.getByText('查询关注状态失败', { exact: true })).toBeVisible()
  await expect(page.locator('.follow-button')).toBeDisabled()
  await page.getByRole('button', { name: '重新读取关注状态', exact: true }).click()
  await expect(page.locator('.follow-button')).toHaveText('✓ 已关注')
  await expect(page.getByText('查询关注状态失败', { exact: true })).toHaveCount(0)
})

test('following tab requires login and pages with its own cursor, never sent to the latest feed', async ({ page }) => {
  const requests: string[] = []
  await mockAPI(page, async (route, path) => {
    const cursor = new URL(route.request().url()).searchParams.get('cursor') || ''
    if (path === '/feed/following') {
      expect(route.request().headers().authorization).toBe('Bearer test-issued-token')
      requests.push(`following:${cursor}`)
      await ok(route, cursor ? { videos: [video(3, 2)], next_cursor: '', has_more: false } : { videos: [video(4, 2)], next_cursor: 'following-cursor', has_more: true })
      return true
    }
    if (path === '/feed') requests.push(`latest:${cursor}`)
    return false
  })
  await page.goto('/#/following')
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByLabel('用户名', { exact: true }).fill('alice'); await page.getByLabel('密码', { exact: true }).fill('test-password-123')
  await page.locator('.auth-modal button[type="submit"]').click()
  await expect(page).toHaveURL(/#\/following$/)
  await expect(page.getByRole('heading', { name: '关注', exact: true })).toBeVisible()
  await expect(page.locator('.video-card')).toHaveCount(1)
  await page.getByRole('button', { name: '加载更多', exact: true }).click()
  await expect(page.locator('.video-card')).toHaveCount(2)
  await expect(page.getByRole('button', { name: '没有更多视频了' })).toBeDisabled()
  await page.locator('#nav-library').click()
  await expect(page.getByRole('heading', { name: '视频库', exact: true })).toBeVisible()
  await expect(page.locator('.video-card')).toHaveCount(1)
  await page.locator('#nav-following').click()
  await expect(page.locator('.video-card')).toHaveCount(1)
  // The following cursor only goes back to /feed/following, and switching tabs restarts each feed.
  expect(requests.filter(r => r.startsWith('following:')).slice(-3)).toEqual(['following:', 'following:following-cursor', 'following:'])
  expect(requests.filter(r => r.startsWith('latest:')).every(r => r === 'latest:')).toBe(true)
})

test('401 clears expired identity and foreign video exposes no delete action', async ({ page }) => {
  await mockAPI(page, async (route, path, method) => {
    if (path === '/videos/2' && method === 'GET') { await ok(route, video(2, 2)); return true }
    if (path === '/videos/2/like') { await ok(route, { error: '登录凭证无效或已过期' }, 401); return true }
    return false
  })
  await page.goto('/'); await login(page); await page.goto('/#/videos/2')
  await expect(page.locator('#account-name')).toHaveText('访客模式')
  await expect(page.getByRole('button', { name: '删除视频', exact: true })).toHaveCount(0)
})
