// Display-only guard. Explicit native-money formatting and USD write contracts
// are allowed; API examples/CSV and price editors keep authoritative units.
import { readdir, readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { join, relative } from 'node:path'
const root = fileURLToPath(new URL('../src/', import.meta.url))
const exceptions = [
  /^docs\//, // API request/response examples retain *_usd and USD.
  /^components\/docs\//,
  /^pages\/(Docs|VideoPricing|LegacyVideoPricing)\.vue$/, // API docs and USD write editors.
  /^components\/pricing\//, // Pricing drafts / native CNY supplier inputs.
  /^components\/OfficialVideoPriceCatalog\.vue$/, // Native supplier CNY reference catalog.
]
let failures = 0, count = 0
async function scan(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const path = join(dir,entry.name), name = relative(root,path).replaceAll('\\','/')
    if (entry.isDirectory()) { await scan(path); continue }
    if (!name.endsWith('.vue')) continue
    const source = await readFile(path,'utf8'); count++
    const checks = [[/\b(?:fmtUsd|formatUsdForDisplay|convertUsdToDisplay)\([^\n;]*?(?:pay_amount|\w+_cny\b)/,'native money passed to USD converter']]
    if (!exceptions.some(pattern=>pattern.test(name))) checks.push(
      [/\bfunction\s+fmtMoney\b/, 'independent money formatter'],
      [/\$\{\{|\$\$\{|['"]\$['"]\s*\+/, 'hardcoded dollar display'],
      [/Number\([^\n)]*(?:amount|balance|cost|price)[^\n)]*\)\.toFixed\(/i,'floating point money formatting'],
      [/(?:USD\s*\{\{|\}\}\s*USD\b)/,'fixed USD label on a displayed value'],
    )
    for (const [pattern,reason] of checks) if (pattern.test(source)) { console.error(`${name}: ${reason}`);failures++ }
  }
}
await scan(root)
if (failures) process.exitCode=1
else console.log(`Money display checks passed (${count} Vue files; explicit USD input/native currency exceptions retained).`)
