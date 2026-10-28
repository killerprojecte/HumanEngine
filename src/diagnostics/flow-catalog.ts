import presentationCN from './catalogs/presentation-zh-CN.json' with { type: 'json' }
import presentationTW from './catalogs/presentation-zh-TW.json' with { type: 'json' }
import presentationEN from './catalogs/presentation-en.json' with { type: 'json' }
import zhCN from './catalogs/zh-CN.json' with { type: 'json' }
import zhTW from './catalogs/zh-TW.json' with { type: 'json' }
import en from './catalogs/en.json' with { type: 'json' }
import errorsCN from './catalogs/errors-zh-CN.json' with { type: 'json' }
import errorsTW from './catalogs/errors-zh-TW.json' with { type: 'json' }
import errorsEN from './catalogs/errors-en.json' with { type: 'json' }

export type DiagnosticLocale = 'zh-CN' | 'zh-TW' | 'en'
export interface FlowCatalog {
  stageLabels: Record<string, string>
  statusLabels: Record<string, string>
  reasonLabels: Record<string, string>
  nextLabels: Record<string, string>
  countLabels: Record<string, string>
}
export function diagnosticLocale(locale = 'zh-CN'): DiagnosticLocale {
  if (/^zh-(?:TW|HK|Hant)/i.test(locale)) return 'zh-TW'
  if (/^en(?:-|$)/i.test(locale)) return 'en'
  return 'zh-CN'
}
export function flowCatalog(locale?: string): FlowCatalog {
  return { 'zh-CN': zhCN, 'zh-TW': zhTW, en }[diagnosticLocale(locale)]
}
export function presentationCatalog(locale?: string) {
  return { 'zh-CN': presentationCN, 'zh-TW': presentationTW, en: presentationEN }[
    diagnosticLocale(locale)
  ]
}
/** Compatibility exports use the same Simplified Chinese default as stdout. */
export const { stageLabels, statusLabels, reasonLabels, nextLabels, countLabels } =
  zhCN as FlowCatalog
export function localizedError(
  code: string | undefined,
  locale?: string,
): { summary: string; advice: string } {
  const language = diagnosticLocale(locale)
  const catalog: Record<string, { summary: string; advice: string }> = {
    'zh-CN': errorsCN,
    'zh-TW': errorsTW,
    en: errorsEN,
  }[language]
  const key = code && /^provider_http_5\d\d$/.test(code) ? 'provider_http_5xx' : code
  if (key && catalog[key]) return catalog[key]!
  if (code && /^provider_http_[1-5]\d\d$/.test(code)) {
    const status = code.slice(-3)
    const words = presentationCatalog(language).words
    return { summary: words.httpSummary.replace('{status}', status), advice: words.httpAdvice }
  }
  const reason = code ? flowCatalog(language).reasonLabels[code] : undefined
  const fallback = catalog.unknown_error!
  return { summary: reason ?? fallback.summary, advice: fallback.advice }
}
