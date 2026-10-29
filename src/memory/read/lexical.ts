// Chinese questions need word boundaries even when no embedding model is configured.
// Keep technical tokens intact; never turn individual Han characters into broad matches.
const hanWords = new Intl.Segmenter('zh', { granularity: 'word' })
const genericHanTerms = new Set([
  '什么',
  '怎么',
  '如何',
  '是否',
  '能否',
  '请问',
  '一下',
  '一个',
  '这个',
  '那个',
  '哪些',
  '这样',
  '那样',
  '之前',
  '现在',
  '以后',
  '曾经',
  '记得',
  '记住',
  '知道',
  '事情',
  '东西',
  '内容',
  '问题',
  '信息',
  '有关',
  '相关',
  '说过',
  '过的',
  '应该',
  '可以',
  '需要',
  '不是',
  '没有',
  '不要',
  '还有',
  '以及',
  '因为',
  '所以',
  '但是',
  '然后',
  '情况',
  '时候',
  '用户',
  '我们',
  '你们',
  '他们',
  '她们',
  '大家',
])
export function memorySearchTerms(query: string): string[] {
  const terms = new Set<string>()
  const add = (term: string) => {
    if (terms.size >= 64 || genericHanTerms.has(term)) return
    if (/^\p{Script=Han}+$/u.test(term) && [...term].length < 2) return
    terms.add(term)
  }
  for (const token of query
    .toLowerCase()
    .slice(0, 2000)
    .match(/[\p{L}\p{N}_:.+-]+/gu) ?? []) {
    add(token)
    if (/\p{Script=Han}/u.test(token)) {
      for (const word of hanWords.segment(token)) {
        if (word.isWordLike && /\p{Script=Han}/u.test(word.segment)) add(word.segment)
      }
    }
  }
  return [...terms]
}
