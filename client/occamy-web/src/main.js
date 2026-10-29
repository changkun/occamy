import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import 'element-plus/dist/index.css'
import {
  ElButton,
  ElSelect,
  ElOption,
  ElInput,
  ElForm,
  ElFormItem,
} from 'element-plus'
import App from './App.vue'
import Desktop from './components/Desktop.vue'
import Login from './components/Login.vue'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [{
    path: '/',
    component: Login,
    name: 'login'
}, {
    path: '/desktop',
    component: Desktop,
    name: 'desktop',
}]})

createApp(App)
  .use(ElButton)
  .use(ElSelect)
  .use(ElOption)
  .use(ElInput)
  .use(ElForm)
  .use(ElFormItem)
  .use(router)
  .mount('#app')
