import { createRouter, createWebHashHistory } from 'vue-router'
import { useSessionStore } from './stores/session'
import LibraryView from './views/LibraryView.vue'

export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: LibraryView, meta: { title: '视频库' } },
    { path: '/mine', component: LibraryView, meta: { title: '我的视频', auth: true } },
    { path: '/publish', component: () => import('./views/PublishView.vue'), meta: { title: '发布视频', auth: true } },
    { path: '/account', component: () => import('./views/AccountView.vue'), meta: { title: '当前用户', auth: true } },
    { path: '/videos/:id', component: () => import('./views/DetailView.vue'), meta: { title: '视频详情' } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior: () => ({ top: 0 }),
})
router.beforeEach(to => {
  const session = useSessionStore()
  if (to.meta.auth && !session.token) { session.requireLogin(to.fullPath); return '/' }
})
