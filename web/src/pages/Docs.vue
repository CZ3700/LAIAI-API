<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import DocExample from '@/components/docs/DocExample.vue'
import { docGroups, docPages, searchPages } from '@/docs/apiDocs'
const route = useRoute()
const router = useRouter()
const search = ref('')
const mobileOpen = ref(false)
const expanded = ref<Record<string, boolean>>({ text: true, image: false, video: true })
const article = ref<HTMLElement | null>(null)
const selected = computed(() => docPages.find(p => p.id === route.query.section) || docPages[0]!)
const matches = computed(() => searchPages(search.value))
const groups = computed(() => docGroups.map(g => ({ ...g, pages: matches.value.filter(p => p.group === g.id) })))
const index = computed(() => docPages.indexOf(selected.value))
const previous = computed(() => docPages[index.value - 1])
const next = computed(() => docPages[index.value + 1])
async function select(id: string) {
  await router.push({ path: '/docs', query: { section: id } })
  mobileOpen.value = false
  await nextTick()
  article.value?.focus({ preventScroll: true })
  article.value?.scrollIntoView({ block: 'start', behavior: 'instant' })
}
watch(selected, async (page, old) => {
  expanded.value[page.group] = true
  if (old && page.id !== old.id) {
    await nextTick()
    article.value?.focus({ preventScroll: true })
    article.value?.scrollIntoView({ block: 'start', behavior: 'instant' })
  }
}, { immediate: true })
</script>

<template>
  <div class="api-docs mx-auto w-full max-w-[1440px] px-4 pb-14 sm:px-6 lg:px-8" lang="zh-CN">
    <div class="flex flex-wrap items-end justify-between gap-4 border-b border-line py-8">
      <div><p class="text-xs font-semibold tracking-[0.16em] text-accent">LAIAI / API · 简体中文</p><h1 class="mt-2 text-3xl font-semibold">开发文档</h1><p class="mt-3 text-sm text-muted">从模型选择到请求、任务和账单，按实际接口接入。</p></div>
      <div class="flex flex-wrap gap-3 text-sm"><router-link to="/models" class="btn-ghost">模型目录</router-link><a href="/openapi.yaml" download="laiai-openapi.yaml" class="btn-ghost">下载 OpenAPI 3.1</a></div>
    </div>
    <div class="grid items-start gap-8 lg:grid-cols-[240px_minmax(0,1fr)] xl:grid-cols-[250px_minmax(0,1fr)]">
      <aside class="docs-sidebar border-b border-line py-5 lg:sticky lg:top-20 lg:max-h-[calc(100dvh-6rem)] lg:overflow-y-auto lg:border-b-0 lg:pr-3">
        <button class="mb-4 flex w-full items-center justify-between rounded-lg border border-line p-3 text-sm lg:hidden" :aria-expanded="mobileOpen" aria-controls="docs-navigation" @click="mobileOpen = !mobileOpen">文档目录 <span aria-hidden="true">{{ mobileOpen ? '−' : '+' }}</span></button>
        <div id="docs-navigation" :class="mobileOpen ? 'block' : 'hidden lg:block'">
          <label for="docs-search" class="sr-only">搜索文档、接口或字段</label>
          <input id="docs-search" v-model="search" type="search" placeholder="搜索文档、接口或字段…" class="input w-full text-sm" autocomplete="off" />
          <p v-if="search" class="mt-3 text-xs text-muted" role="status">找到 {{ matches.length }} 篇文档</p>
          <nav aria-label="模型文档" class="mt-5 space-y-4">
            <section v-for="group in groups" :key="group.id">
              <button class="flex w-full items-center justify-between py-2 text-left text-sm font-semibold" :aria-expanded="!!search || expanded[group.id]" :aria-controls="`docs-group-${group.id}`" @click="expanded[group.id] = !expanded[group.id]">
                <span>{{ group.title }}</span><span class="text-muted" aria-hidden="true">{{ search || expanded[group.id] ? '−' : '+' }}</span>
              </button>
              <ul v-show="search || expanded[group.id]" :id="`docs-group-${group.id}`" class="space-y-1 border-l border-line pl-3">
                <li v-for="page in group.pages" :key="page.id"><a :href="`/docs?section=${page.id}`" :aria-current="selected.id === page.id ? 'page' : undefined" class="block rounded-md px-3 py-2 text-sm leading-5 transition-colors hover:bg-panel" :class="selected.id === page.id ? 'bg-panel font-medium text-accent' : 'text-muted'" @click.prevent="select(page.id)">{{ page.title }}</a></li>
                <li v-if="!group.pages.length" class="px-3 py-2 text-xs text-muted">无匹配内容</li>
              </ul>
            </section>
          </nav>
        </div>
      </aside>
      <main ref="article" tabindex="-1" class="docs-article min-w-0 py-6 outline-none lg:py-9">
        <p class="text-xs font-medium text-accent">{{ docGroups.find(g => g.id === selected.group)?.title }}</p>
        <h2 class="mt-3 text-3xl font-semibold tracking-tight sm:text-4xl">{{ selected.title }}</h2>
        <p class="mt-4 max-w-4xl text-base leading-7 text-muted">{{ selected.summary }}</p>
        <div class="mt-6 rounded-lg border border-line bg-panel px-4 py-3 text-xs leading-6 text-muted">以下示例为说明性数据，不是真实生成记录、业务账单或价格承诺。Base URL、模型权限和能力以当前部署为准；示例只展示请求，不会在本页面发起生成。</div>
        <nav v-if="selected.sections.length > 1" aria-label="本页目录" class="mt-6 flex flex-wrap gap-x-5 gap-y-2 border-b border-line pb-5 text-sm"><a v-for="section in selected.sections" :key="section.id" :href="`#${section.id}`" class="text-muted hover:text-accent">{{ section.title }}</a></nav>
        <section v-for="section in selected.sections" :id="section.id" :key="`${selected.id}-${section.id}`" class="docs-section mt-9">
          <h3 class="text-xl font-semibold">{{ section.title }}</h3>
          <p v-for="(p, i) in section.paragraphs" :key="i" class="mt-4 text-sm leading-7 text-muted">{{ p }}</p>
          <ol v-if="section.steps" class="mt-5 list-decimal space-y-3 pl-5 text-sm leading-7 text-muted"><li v-for="step in section.steps" :key="step">{{ step }}</li></ol>
          <div v-if="section.table" class="mt-5 overflow-x-auto rounded-lg border border-line"><table class="w-full min-w-[540px] text-left text-sm"><thead class="bg-panel text-xs"><tr><th v-for="heading in section.table.headers" :key="heading" scope="col" class="px-4 py-3 font-semibold">{{ heading }}</th></tr></thead><tbody><tr v-for="(row, i) in section.table.rows" :key="i" class="border-t border-line"><td v-for="(cell, j) in row" :key="j" class="px-4 py-3 align-top leading-6" :class="j === 0 ? 'font-mono text-xs' : 'text-muted'">{{ cell }}</td></tr></tbody></table></div>
          <div v-if="section.note" class="mt-5 rounded-lg border-l-2 border-accent bg-panel px-4 py-3 text-sm leading-7">{{ section.note }}</div>
          <DocExample v-for="example in section.examples" :key="example.id" :example="example" class="mt-6" />
          <div v-if="section.links" class="mt-5 flex flex-wrap gap-4 text-sm"><router-link v-for="link in section.links" :key="link.to" :to="link.to" class="font-medium text-accent hover:underline">{{ link.label }} →</router-link></div>
        </section>
        <div class="mt-12 grid gap-3 border-t border-line pt-6 sm:grid-cols-2">
          <a v-if="previous" :href="`/docs?section=${previous.id}`" class="rounded-lg border border-line p-4 text-sm hover:bg-panel" @click.prevent="select(previous.id)"><span class="block text-xs text-muted">上一篇</span><span class="mt-2 block">← {{ previous.title }}</span></a><span v-else />
          <a v-if="next" :href="`/docs?section=${next.id}`" class="rounded-lg border border-line p-4 text-right text-sm hover:bg-panel" @click.prevent="select(next.id)"><span class="block text-xs text-muted">下一篇</span><span class="mt-2 block">{{ next.title }} →</span></a>
        </div>
      </main>
    </div>
  </div>
</template>

<style scoped>
.docs-article, .docs-section { scroll-margin-top: 6rem; }
.api-docs :deep(pre) { tab-size: 2; }
.docs-sidebar { scrollbar-width: thin; }
@media (prefers-reduced-motion: reduce) { .api-docs * { scroll-behavior: auto; transition: none; } }
</style>
