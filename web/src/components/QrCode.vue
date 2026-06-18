<script setup lang="ts">
// 客户端生成二维码（零外部依赖、零网络）：用 qrcode-generator 把内容编码成 SVG 自渲染。
// 避免依赖 api.qrserver.com 等外部服务（国内常被墙/超时，导致二维码不显示）。
import { computed } from 'vue'
import qrcode from 'qrcode-generator'

const props = withDefaults(defineProps<{ value: string; size?: number }>(), { size: 180 })

const svg = computed(() => {
  if (!props.value) return ''
  const qr = qrcode(0, 'M') // typeNumber=0 自适应；纠错级别 M
  qr.addData(props.value)
  qr.make()
  const count = qr.getModuleCount()
  const cell = props.size / count
  let rects = ''
  for (let r = 0; r < count; r++) {
    for (let c = 0; c < count; c++) {
      if (qr.isDark(r, c)) {
        rects += `<rect x="${(c * cell).toFixed(3)}" y="${(r * cell).toFixed(3)}" width="${cell.toFixed(3)}" height="${cell.toFixed(3)}"/>`
      }
    }
  }
  // value 只进 QR 模块（布尔点阵），绝不拼进 HTML —— 无注入面。
  return `<svg xmlns="http://www.w3.org/2000/svg" width="${props.size}" height="${props.size}" viewBox="0 0 ${props.size} ${props.size}" shape-rendering="crispEdges"><rect width="${props.size}" height="${props.size}" fill="#ffffff"/><g fill="#000000">${rects}</g></svg>`
})
</script>

<template>
  <!-- eslint-disable-next-line vue/no-v-html -->
  <div class="inline-block rounded-lg border border-line bg-white p-2 leading-none" role="img" aria-label="QR" v-html="svg"></div>
</template>
