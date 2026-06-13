<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { copyText } from '@/lib/api'

const { t } = useI18n()
const copied = ref('')
async function copy(id: string, text: string) {
  if (await copyText(text)) {
    copied.value = id
    setTimeout(() => (copied.value = ''), 1200)
  }
}

const curlChat = `curl https://your-domain/v1/chat/completions \\
  -H "Authorization: Bearer $LAIAI_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "gpt-5.2",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'`

const pySdk = `from openai import OpenAI

client = OpenAI(
    base_url="https://your-domain/v1",
    api_key="sk-laiai-...",
)

resp = client.chat.completions.create(
    model="gpt-5.2",
    messages=[{"role": "user", "content": "Hello"}],
)
print(resp.choices[0].message.content)`

const sections = [
  { id: 'chat', path: '/v1/chat/completions', method: 'POST' },
  { id: 'embeddings', path: '/v1/embeddings', method: 'POST' },
  { id: 'images', path: '/v1/images/generations', method: 'POST' },
  { id: 'models', path: '/v1/models', method: 'GET' },
]
</script>

<template>
  <div class="mx-auto max-w-3xl px-6 py-16">
    <h1 class="text-3xl font-bold tracking-tight">{{ t('docs.title') }}</h1>
    <p class="mt-4 text-muted">{{ t('docs.lead') }}</p>

    <!-- 快速开始 -->
    <section class="mt-12">
      <h2 class="text-xl font-semibold">{{ t('docs.quickstart') }}</h2>
      <ol class="mt-4 space-y-2 text-muted">
        <li>1. {{ t('docs.step1') }}</li>
        <li>2. {{ t('docs.step2') }}</li>
        <li>3. {{ t('docs.step3') }}</li>
      </ol>
    </section>

    <!-- 认证 -->
    <section class="mt-12">
      <h2 class="text-xl font-semibold">{{ t('docs.authTitle') }}</h2>
      <p class="mt-2 text-muted">{{ t('docs.authBody') }}</p>
      <div class="card mt-3 p-4 font-mono text-xs">Authorization: Bearer sk-laiai-...</div>
    </section>

    <!-- curl 示例 -->
    <section class="mt-12">
      <h2 class="text-xl font-semibold">{{ t('docs.curlTitle') }}</h2>
      <div class="card mt-3 overflow-hidden">
        <div class="flex items-center justify-between border-b border-line px-4 py-2 text-xs text-muted">
          <span>curl · {{ t('docs.streaming') }}</span>
          <button class="hover:text-accent" @click="copy('curl', curlChat)">
            {{ copied === 'curl' ? t('common.copied') : t('common.copy') }}
          </button>
        </div>
        <pre class="overflow-x-auto p-4 text-xs leading-relaxed"><code>{{ curlChat }}</code></pre>
      </div>
    </section>

    <!-- SDK -->
    <section class="mt-12">
      <h2 class="text-xl font-semibold">{{ t('docs.sdkTitle') }}</h2>
      <p class="mt-2 text-muted">{{ t('docs.sdkBody') }}</p>
      <div class="card mt-3 overflow-hidden">
        <div class="flex items-center justify-between border-b border-line px-4 py-2 text-xs text-muted">
          <span>Python · openai</span>
          <button class="hover:text-accent" @click="copy('py', pySdk)">
            {{ copied === 'py' ? t('common.copied') : t('common.copy') }}
          </button>
        </div>
        <pre class="overflow-x-auto p-4 text-xs leading-relaxed"><code>{{ pySdk }}</code></pre>
      </div>
    </section>

    <!-- 端点 -->
    <section class="mt-12">
      <h2 class="text-xl font-semibold">{{ t('docs.endpoints') }}</h2>
      <div class="card mt-3 overflow-hidden">
        <table class="table w-full text-sm">
          <thead>
            <tr class="border-b border-line text-left text-muted">
              <th class="px-4 py-3 font-medium">{{ t('docs.endpoint') }}</th>
              <th class="px-4 py-3 font-medium">{{ t('docs.purpose') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="s in sections" :key="s.id">
              <td class="px-4 py-2.5 font-mono text-xs">
                <span class="badge-muted">{{ s.method }}</span> {{ s.path }}
              </td>
              <td class="px-4 py-2.5 text-muted">{{ t(`docs.ep_${s.id}`) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <p class="mt-12 text-sm text-muted">
      {{ t('docs.moreModels') }}
      <router-link class="text-accent hover:underline" to="/models">{{ t('nav.models') }} →</router-link>
    </p>
  </div>
</template>
