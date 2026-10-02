import { createRouter, createWebHistory } from 'vue-router'
import DashboardView from './views/DashboardView.vue'
import SearchView from './views/SearchView.vue'

export default createRouter({
  history: createWebHistory(),
  scrollBehavior: (to, from, savedPosition) => savedPosition || false,
  routes: [
    { path: '/', name: 'dashboard', component: DashboardView },
    { path: '/search', name: 'search', component: SearchView },
  ],
})
