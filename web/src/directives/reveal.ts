import type { Directive } from 'vue'

// v-reveal：元素进入视口时渐入（加 .reveal-in）。可选 v-reveal:delay="120" 做 stagger。
// IntersectionObserver 单次触发，触发后断开，避免反复动画。
const observer =
  typeof IntersectionObserver !== 'undefined'
    ? new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            if (entry.isIntersecting) {
              const el = entry.target as HTMLElement
              const delay = Number(el.dataset.revealDelay ?? 0)
              window.setTimeout(() => el.classList.add('reveal-in'), delay)
              observer!.unobserve(el)
            }
          }
        },
        { threshold: 0.12, rootMargin: '0px 0px -40px 0px' },
      )
    : null

export const reveal: Directive<HTMLElement, number | undefined> = {
  mounted(el, binding) {
    el.classList.add('reveal')
    if (binding.value) el.dataset.revealDelay = String(binding.value)
    if (observer) observer.observe(el)
    else el.classList.add('reveal-in') // 不支持 IO 时直接显示
  },
  unmounted(el) {
    observer?.unobserve(el)
  },
}
