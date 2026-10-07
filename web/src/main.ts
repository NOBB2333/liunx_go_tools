import { createApp } from 'vue'
import '@file-viewer/vue3/dist/file-viewer3.css'
import App from './App.vue'
import router from './router'
import './styles.css'

createApp(App).use(router).mount('#app')
