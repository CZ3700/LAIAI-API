<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { copyText } from '@/lib/api'

const { t } = useI18n()
const copied = ref('')

const endpoints = [
  { name: 'OpenAI', base: 'https://your-domain/v1', client: 'OpenAI SDK / LangChain / Agent frameworks' },
  { name: 'Anthropic', base: 'https://your-domain/anthropic', client: 'Anthropic SDK / Claude Code' },
]
const headers = ['x-trace-id', 'x-user-id', 'x-agent-name']
const checklist = ['key', 'model', 'stream', 'budget', 'logs', 'status'] as const
const tools = ['Cursor', 'Claude Code', 'Codex', 'TRAE', 'Hermes Agent']
const paths = [
  { key: 'key', to: '/app/keys', stat: 'sk-laiai-*' },
  { key: 'provider', to: '/docs', stat: 'BYOK' },
  { key: 'observe', to: '/app/logs', stat: 'trace' },
] as const
const supportPacket = ['trace', 'user', 'agent', 'request', 'status'] as const

const curl = `curl https://your-domain/v1/chat/completions \\
  -H "Authorization: Bearer $LAIAI_KEY" \\
  -H "Content-Type: application/json" \\
  -H "x-trace-id: checkout-smoke-001" \\
  -d '{
    "model": "deepseek-v4-pro",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true,
    "stream_options": {"include_usage": true}
  }'`

const env = `LAIAI_BASE_URL=https://your-domain/v1
LAIAI_KEY=sk-laiai-...
LAIAI_MODEL=deepseek-v4-pro`

async function copy(id: string, value: string) {
  if (await copyText(value)) {
    copied.value = id
    window.setTimeout(() => (copied.value = ''), 1200)
  }
}
</script>

<template>
  <div class="app-page devex-page">
    <section class="console-hero p-5 sm:p-6">
      <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_28rem] xl:items-end">
        <div>
          <p class="app-eyebrow">{{ t('surface.devexEyebrow') }}</p>
          <h1 class="app-page-title">{{ t('devex.title') }}</h1>
          <p class="app-page-subtitle">{{ t('devex.subtitle') }}</p>
        </div>
        <div class="grid gap-2 sm:grid-cols-3 xl:grid-cols-1">
          <router-link to="/app/keys" class="devex-step-link">
            <span>01</span>{{ t('devex.createKey') }}
          </router-link>
          <router-link to="/app/models" class="devex-step-link">
            <span>02</span>{{ t('devex.chooseModel') }}
          </router-link>
          <router-link to="/app/logs" class="devex-step-link">
            <span>03</span>{{ t('devex.verifyLogs') }}
          </router-link>
        </div>
      </div>
    </section>

    <section class="grid gap-4 md:grid-cols-3">
      <router-link v-for="p in paths" :key="p.key" :to="p.to" class="devex-path-card">
        <span>{{ t(`devex.path_${p.key}_tag`) }}</span>
        <strong>{{ t(`devex.path_${p.key}_title`) }}</strong>
        <small>{{ t(`devex.path_${p.key}_body`) }}</small>
        <code>{{ p.stat }}</code>
      </router-link>
    </section>

    <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_24rem]">
      <div class="space-y-4">
        <section class="app-panel p-5">
          <div class="mb-4 flex items-center justify-between gap-3">
            <div>
              <h2 class="font-semibold">{{ t('devex.apiEntry') }}</h2>
              <p class="mt-1 text-xs text-muted">{{ t('devex.apiEntryHint') }}</p>
            </div>
            <router-link to="/docs" class="btn-ghost !py-1.5 text-xs">{{ t('nav.docs') }}</router-link>
          </div>
          <div class="overflow-hidden rounded-lg border border-line">
            <table class="table w-full text-sm">
              <thead>
                <tr class="bg-elev/55 text-left text-muted">
                  <th class="px-4 py-2.5">{{ t('devex.api') }}</th>
                  <th class="px-4 py-2.5">{{ t('devex.baseUrl') }}</th>
                  <th class="px-4 py-2.5">{{ t('devex.clients') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="e in endpoints" :key="e.name">
                  <td class="px-4 py-3 font-medium">{{ e.name }}</td>
                  <td class="px-4 py-3"><code class="font-mono text-xs">{{ e.base }}</code></td>
                  <td class="px-4 py-3 text-muted">{{ e.client }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section class="grid gap-4 lg:grid-cols-2">
          <div class="docs-code-block">
            <div class="flex items-center justify-between border-b border-line px-4 py-3 text-xs text-muted">
              <span>curl · streaming</span>
              <button class="font-medium hover:text-accent" @click="copy('curl', curl)">
                {{ copied === 'curl' ? t('common.copied') : t('common.copy') }}
              </button>
            </div>
            <pre class="overflow-x-auto p-4 text-xs leading-relaxed"><code>{{ curl }}</code></pre>
          </div>
          <div class="docs-code-block">
            <div class="flex items-center justify-between border-b border-line px-4 py-3 text-xs text-muted">
              <span>.env</span>
              <button class="font-medium hover:text-accent" @click="copy('env', env)">
                {{ copied === 'env' ? t('common.copied') : t('common.copy') }}
              </button>
            </div>
            <pre class="overflow-x-auto p-4 text-xs leading-relaxed"><code>{{ env }}</code></pre>
          </div>
        </section>

        <section class="app-panel p-5">
          <h2 class="font-semibold">{{ t('devex.requestHeaders') }}</h2>
          <p class="mt-1 text-xs text-muted">{{ t('devex.requestHeadersHint') }}</p>
          <div class="mt-4 grid gap-3 md:grid-cols-3">
            <div v-for="h in headers" :key="h" class="soft-panel p-3">
              <code class="font-mono text-xs text-accent">{{ h }}</code>
              <p class="mt-2 text-xs leading-5 text-muted">{{ t(`devex.header_${h.replaceAll('-', '_')}`) }}</p>
            </div>
          </div>
        </section>
      </div>

      <aside class="space-y-4">
        <section class="app-panel p-5">
          <h2 class="font-semibold">{{ t('devex.launchChecklist') }}</h2>
          <div class="mt-4 space-y-3">
            <label v-for="item in checklist" :key="item" class="flex items-start gap-3 rounded-lg border border-line/70 p-3 text-sm">
              <input type="checkbox" class="mt-0.5 accent-accent" />
              <span>
                <span class="block font-medium">{{ t(`devex.check_${item}`) }}</span>
                <span class="mt-0.5 block text-xs leading-5 text-muted">{{ t(`devex.check_${item}_hint`) }}</span>
              </span>
            </label>
          </div>
        </section>

        <section class="app-panel p-5">
          <h2 class="font-semibold">{{ t('devex.tooling') }}</h2>
          <p class="mt-1 text-xs text-muted">{{ t('devex.toolingHint') }}</p>
          <div class="mt-4 flex flex-wrap gap-2">
            <span v-for="tool in tools" :key="tool" class="badge-muted">{{ tool }}</span>
          </div>
        </section>

        <section class="app-panel p-5">
          <h2 class="font-semibold">{{ t('devex.supportReady') }}</h2>
          <p class="mt-2 text-sm leading-6 text-muted">{{ t('devex.supportReadyHint') }}</p>
          <div class="mt-4 space-y-2">
            <div v-for="item in supportPacket" :key="item" class="flex items-center justify-between gap-3 rounded-lg border border-line/70 px-3 py-2 text-xs">
              <span class="text-muted">{{ t(`devex.packet_${item}`) }}</span>
              <span class="font-medium text-ink">{{ t(`devex.packet_${item}_value`) }}</span>
            </div>
          </div>
          <router-link to="/app/logs" class="btn-primary mt-4 w-full !py-2 text-sm">{{ t('devex.openLogs') }}</router-link>
        </section>
      </aside>
    </div>
  </div>
</template>
