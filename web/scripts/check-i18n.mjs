// 编译每条 i18n 文案，捕获 vue-i18n 运行时才暴露的消息语法错误：
//   @  → 链接消息操作符（如 user:pass@host 会被当成 @host 链接 → SyntaxError）
//   |  → 复数分隔符
//   {} → 命名/列表插值（未配对会报错）
// vue-tsc / `satisfies Messages` 只查类型与键，查不出消息语法——这类 bug 能过构建却在运行时把页面整块炸白。
// 本检查放进 build 前置，杜绝此类问题再次上线。
import { build } from 'esbuild'
import { baseCompile } from '@intlify/message-compiler'
import { pathToFileURL, fileURLToPath } from 'node:url'
import { mkdtempSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const i18nDir = fileURLToPath(new URL('../src/i18n/', import.meta.url))
const localeFiles = readdirSync(i18nDir).filter((f) => f.endsWith('.ts') && f !== 'index.ts')
const tmp = mkdtempSync(join(tmpdir(), 'i18n-check-'))
let errors = 0
let count = 0

function walk(obj, file, path) {
  for (const [k, v] of Object.entries(obj)) {
    const p = path ? `${path}.${k}` : k
    if (typeof v === 'string') {
      count++
      try {
        // 与 vue-i18n 同款编译器；语法非法即抛
        baseCompile(v, { onError(e) { throw e } })
      } catch (e) {
        errors++
        console.error(`✗ ${file}  ${p}\n    "${v}"\n    ${e.message}\n`)
      }
    } else if (v && typeof v === 'object') {
      walk(v, file, p)
    }
  }
}

try {
  for (const file of localeFiles) {
    const outfile = join(tmp, file.replace(/\.ts$/, '.mjs'))
    await build({
      entryPoints: [join(i18nDir, file)],
      bundle: true, format: 'esm', outfile, logLevel: 'silent', platform: 'node',
    })
    const mod = await import(pathToFileURL(outfile).href)
    if (!mod.default || typeof mod.default !== 'object') {
      console.error(`✗ ${file}: no default-exported message object`)
      errors++
      continue
    }
    walk(mod.default, file, '')
  }
} finally {
  rmSync(tmp, { recursive: true, force: true })
}

if (errors) {
  console.error(`\n${errors} i18n message(s) failed to compile — vue-i18n would crash at runtime. Fix before building.`)
  process.exit(1)
}
console.log(`✓ i18n: ${count} messages compile cleanly across ${localeFiles.length} message bundles`)
