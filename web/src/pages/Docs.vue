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

const steps = ['step1', 'step2', 'step3']
</script>

<template>
  <div class="docs-page overflow-hidden">
    <section class="docs-hero border-b border-line">
      <div class="mx-auto grid max-w-7xl gap-10 px-4 py-16 sm:px-6 lg:grid-cols-[0.88fr_1.12fr] lg:items-center lg:px-8">
        <div>
          <p class="enter text-sm font-medium text-accent" style="animation-delay: 0ms">LAIAI API</p>
          <h1 class="enter docs-hero-title mt-5" style="animation-delay: 70ms">
            {{ t('docs.title') }}
          </h1>
          <p class="enter docs-hero-lead mt-5 max-w-2xl" style="animation-delay: 120ms">
            {{ t('docs.lead') }}
          </p>
          <p class="enter mt-6 max-w-xl text-sm leading-6 text-muted" style="animation-delay: 170ms">
            {{ t('docs.sdkBody') }}
          </p>
          <div class="enter mt-8 flex flex-wrap gap-3" style="animation-delay: 210ms">
            <router-link to="/register" class="btn-primary px-5 !py-2.5">{{ t('landing.ctaStart') }}</router-link>
            <router-link to="/models" class="btn-ghost px-5 !py-2.5">{{ t('nav.models') }}</router-link>
          </div>
        </div>

        <div class="enter docs-command" style="animation-delay: 160ms">
          <div class="flex items-center justify-between border-b border-white/10 px-4 py-3 text-xs">
            <span class="text-slate-500">{{ t('surface.docsBaseUrl') }}</span>
            <span class="text-cyan-300">/v1</span>
          </div>
          <pre class="overflow-x-auto p-5 text-xs leading-relaxed"><code>client = OpenAI(
  base_url="https://your-domain/v1",
  api_key=os.environ["LAIAI_KEY"],
)</code></pre>
          <div class="grid grid-cols-3 gap-px bg-white/10 text-xs">
            <div class="bg-[#05080d] p-4">
              <p class="text-slate-500">{{ t('surface.docsAuth') }}</p>
              <p class="mt-1 text-slate-100">Bearer</p>
            </div>
            <div class="bg-[#05080d] p-4">
              <p class="text-slate-500">{{ t('surface.docsStream') }}</p>
              <p class="mt-1 text-slate-100">SSE</p>
            </div>
            <div class="bg-[#05080d] p-4">
              <p class="text-slate-500">{{ t('surface.docsFormat') }}</p>
              <p class="mt-1 text-slate-100">OpenAI</p>
            </div>
          </div>
        </div>
      </div>
    </section>

    <section class="mx-auto grid max-w-7xl gap-8 px-4 py-16 sm:px-6 lg:grid-cols-[0.46fr_0.54fr] lg:px-8">
      <div class="docs-rail" v-reveal>
        <p class="text-sm font-medium text-accent">{{ t('docs.quickstart') }}</p>
        <ol class="mt-6 space-y-5">
          <li v-for="(step, i) in steps" :key="step" class="docs-step">
            <span>{{ i + 1 }}</span>
            <p>{{ t(`docs.${step}`) }}</p>
          </li>
        </ol>
        <div class="mt-8 border-t border-line pt-6">
          <p class="text-sm font-medium">{{ t('docs.authTitle') }}</p>
          <p class="mt-2 text-sm leading-6 text-muted">{{ t('docs.authBody') }}</p>
          <div class="mt-4 overflow-x-auto rounded-lg border border-line bg-panel p-4 font-mono text-xs">
            Authorization: Bearer sk-laiai-...
          </div>
        </div>
      </div>

      <div class="space-y-6">
        <section v-reveal="80" class="docs-code-block">
          <div class="flex items-center justify-between border-b border-line px-4 py-3 text-xs text-muted">
            <span>curl · {{ t('docs.streaming') }}</span>
            <button class="font-medium hover:text-accent" @click="copy('curl', curlChat)">
              {{ copied === 'curl' ? t('common.copied') : t('common.copy') }}
            </button>
          </div>
          <pre class="overflow-x-auto p-4 text-xs leading-relaxed"><code>{{ curlChat }}</code></pre>
        </section>

        <section v-reveal="160" class="docs-code-block">
          <div class="flex items-center justify-between border-b border-line px-4 py-3 text-xs text-muted">
            <span>Python · openai</span>
            <button class="font-medium hover:text-accent" @click="copy('py', pySdk)">
              {{ copied === 'py' ? t('common.copied') : t('common.copy') }}
            </button>
          </div>
          <pre class="overflow-x-auto p-4 text-xs leading-relaxed"><code>{{ pySdk }}</code></pre>
        </section>
      </div>
    </section>

    <section class="docs-endpoints border-t border-line">
      <div class="mx-auto max-w-7xl px-4 py-14 sm:px-6 lg:px-8">
        <div class="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
          <div>
            <p class="text-sm font-medium text-accent">{{ t('docs.endpoints') }}</p>
            <h2 class="mt-2 text-3xl font-semibold">{{ t('docs.endpoint') }}</h2>
          </div>
          <p class="text-sm text-muted">
            {{ t('docs.moreModels') }}
            <router-link class="text-accent hover:underline" to="/models">{{ t('nav.models') }}</router-link>
          </p>
        </div>
        <div class="mt-8 grid gap-px overflow-hidden rounded-lg border border-line bg-line md:grid-cols-2">
          <div v-for="s in sections" :key="s.id" v-reveal class="docs-endpoint bg-panel p-5">
            <div class="flex items-center justify-between gap-4">
              <span class="badge-muted">{{ s.method }}</span>
              <span class="font-mono text-xs text-muted">{{ s.path }}</span>
            </div>
            <p class="mt-6 text-sm leading-6 text-muted">{{ t(`docs.ep_${s.id}`) }}</p>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>
