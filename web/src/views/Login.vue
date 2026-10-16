<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-logo">
        <div class="logo-icon">
          <el-icon :size="34"><Ship /></el-icon>
        </div>
        <h1>Kube Console</h1>
        <p>Kubernetes 集群管理平台</p>
      </div>
      <el-form :model="form" size="large" @submit.prevent="onLogin">
        <el-form-item>
          <el-input v-model="form.username" placeholder="用户名" :prefix-icon="User" autofocus />
        </el-form-item>
        <el-form-item>
          <el-input v-model="form.password" type="password" placeholder="密码" :prefix-icon="Lock" show-password @keyup.enter="onLogin" />
        </el-form-item>
        <el-button type="primary" size="large" class="login-btn" :loading="loading" @click="onLogin">登 录</el-button>
      </el-form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { User, Lock } from '@element-plus/icons-vue'
import { authApi } from '../api'
import { useUserStore } from '../store/user'
import { useClusterStore } from '../store/cluster'

const router = useRouter()
const userStore = useUserStore()
const clusterStore = useClusterStore()
const form = reactive({ username: 'admin', password: '' })
const loading = ref(false)

async function onLogin() {
  if (!form.username || !form.password) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    const data = await authApi.login(form.username, form.password)
    userStore.setAuth(data.token, data.username, (data as any).role || '')
    await clusterStore.load()
    router.push('/')
    ElMessage.success('登录成功')
  } catch {
    /* 错误提示由拦截器处理 */
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-page {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background-color: var(--kc-canvas);
  background-image:
    radial-gradient(circle at 12% 18%, rgba(23, 184, 154, 0.22) 0%, transparent 46%),
    radial-gradient(circle at 88% 78%, rgba(255, 122, 89, 0.16) 0%, transparent 44%),
    radial-gradient(rgba(13, 42, 37, 0.06) 1px, transparent 1px);
  background-size: auto, auto, 22px 22px;
}
.login-card {
  width: 380px;
  background: var(--kc-surface);
  border-radius: var(--kc-radius-lg);
  padding: 40px 36px;
  box-shadow: var(--kc-shadow-lift);
  border: 1px solid var(--kc-line);
}
.login-logo { text-align: center; margin-bottom: 28px; }
.logo-icon {
  width: 64px;
  height: 64px;
  margin: 0 auto;
  border-radius: 20px;
  background: var(--kc-logo-gradient);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 10px 22px rgba(23, 184, 154, 0.36);
}
.login-logo h1 { font-family: var(--kc-font-display); font-size: 24px; font-weight: 800; letter-spacing: -0.02em; margin: 14px 0 4px; color: var(--kc-title); }
.login-logo p { color: var(--kc-mute); margin: 0; font-size: 13px; }
.login-btn { width: 100%; }
html.dark .login-page {
  background-color: var(--kc-canvas);
  background-image:
    radial-gradient(circle at 12% 18%, rgba(47, 208, 171, 0.16) 0%, transparent 46%),
    radial-gradient(circle at 88% 78%, rgba(255, 138, 107, 0.1) 0%, transparent 44%),
    radial-gradient(rgba(228, 241, 236, 0.05) 1px, transparent 1px);
  background-size: auto, auto, 22px 22px;
}
html.dark .login-card { box-shadow: 0 18px 40px rgba(0, 0, 0, 0.5); }
</style>
