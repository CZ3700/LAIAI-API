<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

const pillars = ['ledger', 'keys', 'observability', 'isolation'] as const
const controls = ['journal', 'idempotency', 'envelope', 'audit', 'retention', 'digest'] as const
const reviewPath = ['identity', 'money', 'logs', 'ops'] as const
const artifacts = ['status', 'invocation', 'fingerprint', 'ledger', 'audit', 'support'] as const
</script>

<template>
  <div class="trust-page">
    <section class="trust-hero">
      <div class="mx-auto grid max-w-7xl gap-10 px-4 py-14 sm:px-6 lg:grid-cols-[minmax(0,0.92fr)_minmax(26rem,0.76fr)] lg:items-center lg:px-8">
        <div>
          <p class="app-eyebrow">{{ t('surface.trustEyebrow') }}</p>
          <h1 class="mt-4 max-w-3xl text-4xl font-semibold leading-tight sm:text-5xl">{{ t('trust.title') }}</h1>
          <p class="mt-5 max-w-2xl text-base leading-7 text-muted">{{ t('trust.subtitle') }}</p>
          <div class="mt-8 flex flex-wrap gap-3">
            <router-link to="/status" class="btn-primary px-5 !py-2.5">{{ t('trust.statusCta') }}</router-link>
            <router-link to="/docs" class="btn-ghost px-5 !py-2.5">{{ t('trust.docsCta') }}</router-link>
          </div>
        </div>

        <div class="trust-map">
          <div class="trust-map-row">
            <span>{{ t('trust.edge') }}</span>
            <span>{{ t('trust.relay') }}</span>
            <span>{{ t('trust.core') }}</span>
          </div>
          <div class="trust-map-line">
            <i></i><i></i><i></i>
          </div>
          <div class="trust-map-row">
            <span>{{ t('trust.invocation') }}</span>
            <span>{{ t('trust.ledger') }}</span>
            <span>{{ t('trust.audit') }}</span>
          </div>
        </div>
      </div>
    </section>

    <section class="mx-auto max-w-7xl px-4 py-14 sm:px-6 lg:px-8">
      <div class="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <article v-for="p in pillars" :key="p" class="app-panel p-5">
          <p class="text-xs font-medium uppercase text-accent">{{ t(`trust.${p}Tag`) }}</p>
          <h2 class="mt-3 text-lg font-semibold">{{ t(`trust.${p}Title`) }}</h2>
          <p class="mt-3 text-sm leading-6 text-muted">{{ t(`trust.${p}Body`) }}</p>
        </article>
      </div>
    </section>

    <section class="mx-auto max-w-7xl px-4 pb-14 sm:px-6 lg:px-8">
      <div class="app-panel p-5 sm:p-6">
        <div class="mb-5 flex flex-wrap items-end justify-between gap-4">
          <div>
            <p class="app-eyebrow">{{ t('trust.reviewEyebrow') }}</p>
            <h2 class="mt-2 text-2xl font-semibold">{{ t('trust.reviewTitle') }}</h2>
          </div>
          <router-link to="/docs" class="btn-ghost !py-1.5 text-xs">{{ t('trust.docsCta') }}</router-link>
        </div>
        <div class="grid gap-px overflow-hidden rounded-lg border border-line bg-line md:grid-cols-4">
          <div v-for="r in reviewPath" :key="r" class="bg-panel p-4">
            <p class="text-xs font-medium uppercase text-accent">{{ t(`trust.review_${r}_tag`) }}</p>
            <h3 class="mt-2 font-semibold">{{ t(`trust.review_${r}_title`) }}</h3>
            <p class="mt-2 text-xs leading-5 text-muted">{{ t(`trust.review_${r}_body`) }}</p>
          </div>
        </div>
      </div>
    </section>

    <section class="border-y border-line bg-panel/38">
      <div class="mx-auto grid max-w-7xl gap-8 px-4 py-14 sm:px-6 lg:grid-cols-[0.42fr_0.58fr] lg:px-8">
        <div>
          <p class="app-eyebrow">{{ t('trust.controlsEyebrow') }}</p>
          <h2 class="mt-3 text-3xl font-semibold">{{ t('trust.controlsTitle') }}</h2>
          <p class="mt-4 text-sm leading-6 text-muted">{{ t('trust.controlsBody') }}</p>
        </div>
        <div class="grid gap-px overflow-hidden rounded-lg border border-line bg-line sm:grid-cols-2">
          <div v-for="c in controls" :key="c" class="bg-panel p-4">
            <p class="font-medium">{{ t(`trust.control_${c}`) }}</p>
            <p class="mt-2 text-xs leading-5 text-muted">{{ t(`trust.control_${c}_hint`) }}</p>
          </div>
        </div>
      </div>
    </section>

    <section class="mx-auto max-w-7xl px-4 py-14 sm:px-6 lg:px-8">
      <div class="grid gap-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <div class="app-panel p-5">
          <h2 class="font-semibold">{{ t('trust.artifacts') }}</h2>
          <div class="mt-5 overflow-hidden rounded-lg border border-line">
            <table class="table w-full text-sm">
              <tbody>
                <tr v-for="a in artifacts" :key="a">
                  <td class="px-4 py-3 text-muted">{{ t(`trust.artifact_${a}_label`) }}</td>
                  <td class="px-4 py-3 font-mono text-xs">{{ t(`trust.artifact_${a}_value`) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
        <aside class="app-panel p-5">
          <h2 class="font-semibold">{{ t('trust.procurementTitle') }}</h2>
          <p class="mt-3 text-sm leading-6 text-muted">{{ t('trust.procurementBody') }}</p>
          <router-link to="/about" class="btn-primary mt-5 w-full !py-2 text-sm">{{ t('trust.contactCta') }}</router-link>
        </aside>
      </div>
    </section>
  </div>
</template>
