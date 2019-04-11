import Vue from 'vue'
import VueRouter from 'vue-router'
import 'element-ui/lib/theme-chalk/index.css';
import {
  Button, 
  Select, 
  Option, 
  Input,
  Form, 
  FormItem, 
  Message
} from 'element-ui'
import App from './App.vue'
import Desktop from './components/Desktop.vue'
import Login from './components/Login.vue'

Vue.config.productionTip = false
Vue.use(Button)
Vue.use(Select)
Vue.use(Option)
Vue.use(Input)
Vue.use(Form)
Vue.use(FormItem)
Vue.prototype.$message = Message
Vue.use(VueRouter)

const router = new VueRouter({
  routes: [{
    path: '/',
    component: Login,
    name: 'login'
}, {
    path: '/desktop',
    component: Desktop,
    name: 'desktop',
}]})

new Vue({
  el: '#app',
  template: '<App/>',
  router,
  render: h => h(App),
}).$mount('#app')
