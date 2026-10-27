// Eligibility is deliberately narrower than all knowledge. Model review is not fact verification.
export const publicTechnicalDomains = [
  'http',
  'json',
  'unicode',
  'git',
  'sql',
  'javascript',
  'typescript',
  'python',
  'linux',
] as const
export type PublicTechnicalDomain = (typeof publicTechnicalDomains)[number]
export function publicTechnicalCandidate(content: string, domain: PublicTechnicalDomain) {
  if (content.length > 2000 || !content.toLowerCase().includes(domain)) return false
  return !/(https?:\/\/|[\w.+-]+@[\w.-]+|\b(?:\d{1,3}\.){3}\d{1,3}\b|\b\d{7,}\b|\b[a-f0-9]{24,}\b|\b(?:our|ours|my|mine|internal|private|secret|customer|company|project|account|password|credential|api[_ -]?key|access[_ -]?token)\b|\u6211\u4eec|\u6211\u5011|\u5185\u90e8|\u5167\u90e8|\u9879\u76ee|\u9805\u76ee|\u5ba2\u6237|\u5ba2\u6236|\u5bc6\u7801|\u5bc6\u78bc)/i.test(
    content,
  )
}

// This detects a bounded class of unsupported singular pronouns, not semantic
// entailment or a person's gender. Names and quoted third-party language are not
// permission to infer personal attributes.
const pronounSegments = new Intl.Segmenter('zh', { granularity: 'word' })
const nonPersonalChinese =
  /^(?:他们|他們|她们|她們|他人|他乡|他鄉|他日|他国|他國|他处|他處|他方|他律|他山)/u
const unquoted = (text: string) =>
  text.replace(
    /```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]*`|“[^”]*”|‘[^’]*’|「[^」]*」|『[^』]*』|"[^"\n]*"|(?<![\p{L}\p{N}])'[^'\n]*'(?![\p{L}\p{N}])/gu,
    ' ',
  )
function singularPronouns(text: string): Set<'feminine' | 'masculine'> {
  const result = new Set<'feminine' | 'masculine'>()
  const plain = unquoted(text).replace(/\b(he|she)['’](?:s|d|ll)\b/giu, '$1')
  for (const { segment } of pronounSegments.segment(plain)) {
    const word = segment.toLowerCase()
    if (
      /^(?:she|her|hers|herself)$/.test(word) ||
      (word.startsWith('她') && !nonPersonalChinese.test(word))
    )
      result.add('feminine')
    if (
      /^(?:he|him|his|himself)$/.test(word) ||
      (word.startsWith('他') && !nonPersonalChinese.test(word))
    )
      result.add('masculine')
  }
  return result
}
export function personalPronounsSupported(
  content: string,
  authorSourceTexts: string[],
  literalSourceTexts: string[] = authorSourceTexts,
): boolean {
  const required = singularPronouns(content)
  if (!required.size) return true
  // An unchanged complete source statement introduces no new pronoun/referent.
  // Do not match substrings: that could remove an "if" or a negation.
  const normalized = (text: string) => text.trim().replace(/\s+/gu, ' ')
  if (literalSourceTexts.some((text) => normalized(text) === normalized(content))) return true
  const supported = new Set<'feminine' | 'masculine'>()
  for (const text of authorSourceTexts) {
    const plain = unquoted(text)
    // Deliberately conservative: jokes and hypothetical declarations cannot
    // authorize a factual pronoun, even when followed by a separate clause.
    if (
      /如果|假如|假设|假設|可能|也许|也許|玩笑|才怪|\b(?:if|hypothetical|joking|joke|perhaps|maybe|suppose|assume)\b/iu.test(
        plain,
      )
    )
      continue
    for (const part of plain.split(/[。！？.!?\n，,；;]/u)) {
      const clause = part.trim()
      if (/不|别|別|未|并非|並非|\b(?:not|never|no|don't|doesn't|isn't)\b/iu.test(clause)) continue
      if (
        /^(?:我|本人)(?:是|为|為)(?:一个|一個|一位|一名)?(?:女性|女生|女人|女孩)$|^I am (?:a )?(?:woman|girl|female)$/iu.test(
          clause,
        )
      )
        supported.add('feminine')
      if (
        /^(?:我|本人)(?:是|为|為)(?:一个|一個|一位|一名)?(?:男性|男生|男人|男孩)$|^I am (?:a )?(?:man|boy|male)$/iu.test(
          clause,
        )
      )
        supported.add('masculine')
      const declaration = clause.match(
        /^(?:我的代词是|我的代詞是|我使用的代词是|我使用的代詞是|my pronouns are)\s*(.+)$/iu,
      )
      if (declaration) for (const family of singularPronouns(declaration[1]!)) supported.add(family)
    }
  }
  return [...required].every((family) => supported.has(family))
}

// A narrow trigger for one source-only review, never a fact extractor. Quotes,
// hypotheticals, denials and short acknowledgement-only statements do not qualify.
export function explicitDurableLearningSignal(text: string): boolean {
  const plain = unquoted(text).trim()
  if (
    plain.length < 12 ||
    /如果|假如|假設|假设|玩笑|尚未|未能|不能|不(?:是|要|需)?(?:确认|確認|更正)|\b(?:if|suppose|joking|not confirm|cannot confirm)\b/iu.test(
      plain,
    )
  )
    return false
  return /(?:正式更正|更正如下|正式修正|我(?:已|独立|獨立|亲自|親自)+(?:确认|確認|核实|核實))[^。！？!?\n]{4,}|\b(?:correction:|I (?:independently|personally) (?:confirm|verified|confirmed))\s+.{8,}/iu.test(
    plain,
  )
}
