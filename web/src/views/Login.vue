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
.login-page { height: 100%; display: flex; align-items: center; justify-content: center; background: linear-gradient(135deg, #e8f8f6 0%, #d1f2ee 40%, #b8ece5 100%); }
.login-card { width: 380px; background: #fff; border-radius: 12px; padding: 40px 36px; box-shadow: 0 8px 32px rgba(0, 184, 169, 0.15); border: 1px solid #e8f0ef; }
.login-logo { text-align: center; margin-bottom: 28px; }
.logo-icon {
  width: 64px;
  height: 64px;
  margin: 0 auto;
  border-radius: 16px;
  background: linear-gradient(135deg, #00b8a9 0%, #00d2c0 100%);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 6px 16px rgba(0, 184, 169, 0.35);
}
.login-logo h1 { font-size: 22px; margin: 14px 0 4px; color: #1f2937; }
.login-logo p { color: #9ca3af; margin: 0; font-size: 13px; }
.login-btn { width: 100%; }
</style>
