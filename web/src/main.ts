import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { ElLoading } from 'element-plus'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
// Meadow 主题字体：latin 子集自托管，避免运行时依赖外部字体源（内网部署不可达时退化成系统字）
import '@fontsource/gabarito/latin-500.css'
import '@fontsource/gabarito/latin-700.css'
import '@fontsource/gabarito/latin-800.css'
import '@fontsource/hanken-grotesk/latin-400.css'
import '@fontsource/hanken-grotesk/latin-500.css'
import '@fontsource/hanken-grotesk/latin-600.css'
import '@fontsource/hanken-grotesk/latin-700.css'
import '@fontsource/chivo-mono/latin-400.css'
import '@fontsource/chivo-mono/latin-500.css'
import '@fontsource/chivo-mono/latin-700.css'
import * as ElementPlusIconsVue from '@element-plus/icons-vue'

import App from './App.vue'
import router from './router'
import './styles/theme.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
// 组件按需引入（unplugin-vue-components）；指令与 locale 需手动处理
app.directive('loading', ElLoading.directive)

for (const [key, component] of Object.entries(ElementPlusIconsVue)) {
  app.component(key, component)
}

// 恢复暗色模式
if (localStorage.getItem('kc-dark') === '1') document.documentElement.classList.add('dark')

app.mount('#app')
