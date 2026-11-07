/** Synthetic oracle executes the actual ReplyGrounding, not a rewritten policy. */
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { readFileSync, writeFileSync } from 'node:fs'
import ts from 'typescript'
import { ReplyGrounding } from '../../src/runtime.ts'
import en from '../../i18n/locales/en.json' with { type: 'json' }
import zhCN from '../../i18n/locales/zh-CN.json' with { type: 'json' }

assert.equal(process.version, 'v24.22.1')
const check = process.argv.includes('--check')
const cases = []
const textPart = (text) => ({ type: 'text', text })
const segments = (text, replyToId) => [
  { parts: [textPart(text)], ...(replyToId === undefined ? {} : { replyToId }) },
]
const source = (id = 'feedback', text = '这张图别用了') => ({
  op: 'source',
  id,
  parts: [textPart(text)],
})
const evidence = (value) => ({ op: 'evidence', value })
const tool = (name, value) => ({ op: 'tool', name, value })
const search = tool('memes.search', { items: [], coverage: 'partial' })
const receipt = (patch = {}) => ({
  ok: true,
  actionTaken: 'ranking_adjusted',
  scope: 'asset',
  assetId: 'image',
  sourceMessageId: 'feedback',
  positive: false,
  disabled: false,
  ...patch,
})
const feedback = (patch = {}) => tool('memes.feedback', receipt(patch))
const probe = (text = '那张我不再用啦', target = 'feedback') => ({
  op: 'probe',
  segments: segments(text, target),
  ids: ['', 'feedback', 'other', 'outside', '\ud800', '\ud801', '😀', '__proto__'],
})
const historyItem = (patch = {}) => ({
  id: 'feedback',
  sceneId: 'scene',
  senderId: 'person',
  sentAt: 'not-a-date',
  receivedAt: 'also-not-a-date',
  origin: 'history',
  status: 'available',
  parts: [textPart('旧图反馈')],
  ...patch,
})
const history = (item, mode = 'real') => ({
  op: 'history',
  value: { items: [item], coverage: 'partial' },
  sceneId: 'scene',
  mode,
})
function run(steps) {
  const guard = new ReplyGrounding(),
    output = []
  for (const step of steps) {
    switch (step.op) {
      case 'source':
        guard.addFeedbackSource(step.id, step.parts)
        break
      case 'evidence':
        guard.addEvidence(step.value)
        break
      case 'tool':
        guard.observeTool(step.name, step.value)
        break
      case 'history':
        guard.observeCoreHistory(step.value, step.sceneId, step.mode)
        break
      case 'probe': {
        const normalized = guard.normalizeFeedback(step.segments)
        output.push({
          has: guard.hasFeedbackReceipts(),
          targets: step.ids.map((id) => guard.isFeedbackTarget(id)),
          check: guard.check(step.segments) ?? null,
          normalized,
          normalizedCheck: guard.check(normalized) ?? null,
          original: step.segments,
        })
        break
      }
      default:
        throw new Error(step.op)
    }
  }
  return output
}
function add(name, steps) {
  // JSON.parse is also the Go boundary: undefined is not silently treated as null.
  const input = JSON.stringify(steps)
  cases.push({ name, input, expected: JSON.stringify(run(JSON.parse(input))) })
}
const texts = [
  '今天上午才刚有件好事，下午就挨这一下',
  '今天上午喝了茶',
  '面试没过确实不好受',
  '今天上午怎么样？',
  '今天下午记得休息。',
  'What happened this morning?',
  '今天上午是否喝茶',
  '如果今天上午喝茶',
  '假如今天早上出门',
  '今天下午可能下雨',
  '也许今天上午见过',
  '今天下午建议休息',
  '今天早上不确定',
  '今天下午不知道',
  'Perhaps this morning happened',
  'MAYBE this afternoon',
  'could this morning',
  'This morning might work',
  'remember to rest this afternoon',
  'This Morning tea',
  'this morning tea',
  'xif this morning tea',
  'éif this morning tea',
  '库里没有专门的“鞠躬道谢”图',
  '这边只有一张开心图',
  '手头没有别的表情',
  'The library has no thank-you image',
  '图库里只有刚才那张，别的开心图暂时没有存货',
  '改天再单独给你补一张庆祝的',
  '明天给你发一张图',
  "I'll send you another image tomorrow",
  '这张捧着小爱心、笑到眯眼的蓝团子最适合表达感谢了',
  '这次没找到合适的鞠躬图',
  'This search has no matching image',
  '如果你下次想庆祝，可以再找我选图',
  '祝你明天一切顺利',
  '你计划明天给大家发一张图',
  '这张适合表达感谢的心情',
  '这次没找到但库里肯定没有',
  '这次没找到，库里肯定没有',
  '这次库里肯定没有',
  'This search found nothing but the library has no other image',
  '如果顺利我明天给你发一张',
  '要是顺利明天给你补一张',
  "If everything goes well I'll send you an image tomorrow",
  '下次你再问我，我给你找一张',
  '如果你下次再问我，我给你找一张',
  '本次图库检索没有匹配结果',
  'This library search results list has no match',
  '没有其他图',
  '没有别的表情',
  'No other image',
  'this query has no other image',
  'These results have nothing',
  'inventory is empty',
  'inventory has nothing',
  'out of stock',
  'in stock only one',
  'on hand none',
  'This library QUERY results list has no match',
  '明天给你发一张图？',
  'Would you send you an image tomorrow',
  'if you ask I will send tomorrow',
  'when you request I will post later',
  '你打算明天给大家送礼物',
  '你说明天给你补礼物',
  '要不要明天给你送礼物',
  '祝大家明天给你送礼物',
  '希望明天给你送礼物',
  '这张holding',
  'holding',
  'this cat wearing',
  'this picture beside',
  'this meme on its head',
  'this image on their head',
  '图里叼着',
  '这只戴着',
  '画面头上',
  '这张脚下',
  '这张旁边有',
  '这张旁边是',
  'This image HOLDING',
  "I'll ſend you tomorrow",
  'ſend you tomorrow',
  'this image beſide',
  'tomorrow I will poſt',
  'tomorrow I WILL POST',
  '下午就',
  '今天早上',
  '\u0085今天上午喝了茶\u0085',
  '\ufeff今天上午喝了茶\ufeff',
  '\u180e今天上午喝了茶\u180e',
  '𠀀今天上午喝了茶',
  '\ud800今天上午喝了茶',
  '今天上午喝了茶\udfff',
  '库里没有图。今天上午喝了茶',
  '明天给你送礼物。库里没有图',
  '库里没有图。明天给你送礼物',
  '这张头上，明天给你送礼物',
  '明天给你送礼物。今天下午出门',
  '',
  '?',
  '？？',
]
for (let i = 0; i < texts.length; i++) {
  for (const searched of [false, true])
    add(`claims-${i}-${searched}`, [
      evidence('今天上午喝了茶。今天面试没过。this morning tea'),
      ...(searched ? [search] : []),
      { ...probe(), segments: segments(texts[i]) },
    ])
}
// Non-u dot widths and line terminators must match V8, not Go rune counts.
const widths = [0, 1, 3, 4, 6, 7, 8, 9, 12, 13, 15, 16, 20, 21, 30, 31]
const fillers = ['x', '😀', '\ud800', '\r', '\n', '\u2028', '\u2029', '\0']
for (const filler of fillers)
  for (const n of widths) {
    const gap = filler.repeat(n)
    const forms = [
      `库里${gap}检索没有`,
      `没有其他${gap}图`,
      `No other${gap}image`,
      `明天给你${gap}送礼物`,
      `明天送${gap}给你`,
      `tomorrow I will${gap}post`,
      `明天你${gap}问我给你送礼物`,
      `这张旁边${gap}有`,
    ]
    add(`width-${fillers.indexOf(filler)}-${n}`, [
      search,
      ...forms.map((text) => ({ ...probe(), segments: segments(text) })),
    ])
  }
for (const quote of [
  ['“', '”'],
  ['「', '」'],
  ['『', '』'],
  ['"', '"'],
]) {
  for (const line of ['', '\n', '\r', '\u2028', '\u2029'])
    add(`quote-${quote[0]}-${JSON.stringify(line)}`, [
      evidence(`例句：${quote[0]}今天上午${line}喝了茶${quote[1]}。`),
      { ...probe(), segments: segments(`今天上午${line}喝了茶`) },
    ])
}
add('nested-evidence-only-values-and-order', [
  evidence({
    今天早上出门: false,
    2: '今天下午喝茶',
    1: '今天上午喝茶',
    nested: [null, 42, { s: '下午就下雨' }],
  }),
  ...['今天早上出门', '今天下午喝茶', '今天上午喝茶', '下午就下雨'].map((s) => ({
    ...probe(),
    segments: segments(s),
  })),
])
for (const name of [
  'messages.get',
  'messages.around',
  'messages.byUser',
  'messages.foo',
  'messages.send',
  'memory.search',
  'memory.person',
  'memory.other',
  'memes.search',
  'memes.feedback',
])
  add(`evidence-tool-${name}`, [
    tool(name, { nested: [textPart('今天上午喝茶')] }),
    { ...probe(), segments: segments('今天上午喝茶') },
  ])
add('media-gates-visual-claim', [
  {
    ...probe(),
    segments: [
      { parts: [textPart('holding'), { type: 'media', assetId: 'a', mediaType: 'image' }] },
    ],
  },
  search,
  {
    ...probe(),
    segments: [
      { parts: [textPart('holding'), { type: 'media', assetId: 'a', mediaType: 'image' }] },
    ],
  },
])
add('text-joins-across-parts-and-segments', [
  evidence('今天上午喝茶'),
  search,
  { ...probe(), segments: [{ parts: [textPart('今天上午'), textPart('喝茶')] }] },
  {
    ...probe(),
    segments: [{ parts: [textPart('this')] }, { parts: [textPart(' image holding')] }],
  },
])
add('negative-feedback-state-and-targets', [
  source(),
  feedback(),
  probe(),
  probe('没有显式回复目标', undefined),
  { ...probe(), segments: segments('没有显式回复目标') },
  probe('bad', 'outside'),
  source('other', '讨论旧接口'),
  {
    ...probe(),
    segments: [...segments('bad', 'feedback'), ...segments('以后不再用旧接口', 'other')],
  },
  feedback(),
  probe(),
  feedback({ disabled: true }),
  probe(),
  feedback({ positive: true }),
  probe(),
])
add('single-positive-disabled-and-language-change', [
  source('feedback', 'Do not use this image'),
  feedback({ positive: true, disabled: true }),
  probe(),
  source('feedback', '反馈'),
  probe(),
  source('feedback', ''),
  probe(),
])
add('opposite-asset-polarities-aggregate', [
  source(),
  feedback(),
  feedback({ positive: true, disabled: true }),
  probe(),
])
add('multiple-assets-aggregate', [
  source(),
  feedback(),
  feedback({ assetId: 'other', disabled: true }),
  probe(),
])
add('unknown-before-source-is-not-replayed', [
  feedback(),
  probe(),
  source(),
  probe(),
  feedback(),
  probe(),
])
add('missing-target-dominates-unknown-and-claims', [
  source(),
  feedback(),
  search,
  { ...probe(), segments: [...segments('今天上午出门', 'outside'), ...segments('库里没有图')] },
  probe('今天上午出门', 'outside'),
  { ...probe(), segments: [] },
])
for (const key of [
  'ok',
  'actionTaken',
  'scope',
  'assetId',
  'sourceMessageId',
  'positive',
  'disabled',
]) {
  for (const value of [null, 1, 0, '', [], {}, 'false', 'wrong', false, true]) {
    add(`receipt-${key}-${JSON.stringify(value)}`, [source(), feedback({ [key]: value }), probe()])
  }
  const missing = receipt()
  delete missing[key]
  add(`receipt-missing-${key}`, [source(), tool('memes.feedback', missing), probe()])
}
for (const value of [null, [], true, 'receipt', { error: 'rejected' }, { preference: -0.2 }])
  add(`receipt-shape-${JSON.stringify(value)}`, [source(), tool('memes.feedback', value), probe()])
add('malformed-after-valid-does-not-erase', [
  source(),
  feedback(),
  tool('memes.feedback', { ok: false }),
  probe(),
])
for (const id of ['\ud800', '\ud801', '😀', '__proto__', 'constructor', '\0', ' ', 'a:false'])
  add(`opaque-id-${JSON.stringify(id)}`, [
    source(id),
    feedback({ sourceMessageId: id, assetId: id }),
    probe('promise', id),
  ])
add('locale-surrogate-pair-joins', [
  {
    op: 'source',
    id: 'feedback',
    parts: [textPart('\ud840'), { type: 'mention', userId: 'u' }, textPart('\udc00')],
  },
  feedback(),
  probe(),
])
add('locale-no-join-through-nontext-value', [
  {
    op: 'source',
    id: 'feedback',
    parts: [
      { type: 'unsupported', description: '中文' },
      { type: 'media', assetId: '中', mediaType: 'image' },
    ],
  },
  feedback(),
  probe(),
])
add('history-top-level-core-only', [
  history(historyItem({ id: 'outer', parts: [textPart(JSON.stringify(historyItem()))] })),
  feedback(),
  probe(),
  history(historyItem({ status: 'recalled' })),
  feedback(),
  probe(),
  history(historyItem({ status: 'unavailable' })),
  feedback(),
  probe(),
  history(historyItem({ sceneId: 'elsewhere' })),
  feedback(),
  probe(),
  history(historyItem(), 'simulation'),
  feedback(),
  probe(),
  tool('messages.get', { items: [historyItem()], coverage: 'partial' }),
  feedback(),
  probe(),
  history(historyItem()),
  feedback(),
  probe(),
])
add('history-alone-is-not-chronology-evidence', [
  history(historyItem({ parts: [textPart('今天上午喝茶')] })),
  { ...probe(), segments: segments('今天上午喝茶') },
  tool('messages.get', { items: [historyItem({ parts: [textPart('今天上午喝茶')] })] }),
  { ...probe(), segments: segments('今天上午喝茶') },
])
for (const key of [
  'id',
  'sceneId',
  'senderId',
  'sentAt',
  'receivedAt',
  'origin',
  'status',
  'parts',
]) {
  for (const value of [null, 1, false, '', [], {}, 'wrong'])
    add(`history-${key}-${JSON.stringify(value)}`, [
      history(historyItem({ [key]: value })),
      feedback(),
      probe(),
    ])
  const missing = historyItem()
  delete missing[key]
  add(`history-missing-${key}`, [history(missing), feedback(), probe()])
}
const partCases = [
  [],
  [textPart('')],
  [textPart('x')],
  [{ ...textPart('x'), extra: true }],
  [{ type: 'text', text: 1 }],
  [textPart('😀'.repeat(12000))],
  [textPart('😀'.repeat(12001))],
  [textPart('\ud800'.repeat(12000))],
  [textPart('\ud800'.repeat(12001))],
  [{ type: 'mention', userId: 'u' }],
  [{ type: 'mention', userId: '' }],
  [{ type: 'mention', userId: 'u', extra: true }],
  [{ type: 'media', assetId: 'a', mediaType: '' }],
  [{ type: 'media', assetId: '', mediaType: 'image' }],
  [{ type: 'media', assetId: 'a', mediaType: 'image', extra: true }],
  [{ type: 'media', assetId: 'a', mediaType: null }],
  [{ type: 'unsupported', description: '', extra: true }],
  [{ type: 'unsupported', description: '中文' }],
  [{ type: 'unsupported', description: 1 }],
  [{ type: 'unsupported' }],
  [{ type: 'unknown' }],
  [null],
  [1],
  [[]],
  [{}],
  [textPart('valid'), { type: 'text', text: false }],
]
for (const [i, parts] of partCases.entries())
  add(`history-parts-${i}`, [history(historyItem({ parts })), feedback(), probe()])
add('history-live-extra-top-level-and-unsupported', [
  history(
    historyItem({
      origin: 'live',
      arbitrary: true,
      parts: [{ type: 'unsupported', description: '', extra: 1 }, textPart('English')],
    }),
  ),
  feedback(),
  probe(),
])
add('history-replace-language', [
  history(historyItem()),
  feedback(),
  probe(),
  history(historyItem({ parts: [textPart('English')] })),
  probe(),
])

// Execute the source Runtime fallback expression itself, including its latest
// non-bot/text selection. No expected fallback policy is recreated here.
const runtime = readFileSync('src/runtime.ts', 'utf8')
const ast = ts.createSourceFile('runtime.ts', runtime, ts.ScriptTarget.Latest, true)
const expressions = []
function visit(node) {
  if (
    ts.isConditionalExpression(node) &&
    node.whenTrue.getText(ast) === 'zhCN.groundingFallback' &&
    node.whenFalse.getText(ast) === 'en.groundingFallback'
  )
    expressions.push(node.getText(ast))
  ts.forEachChild(node, visit)
}
visit(ast)
assert.equal(expressions.length, 1)
const fallbackSource = new Function('replyerRecent', 'zhCN', 'en', `return (${expressions[0]})`)
const fallbackTexts = [
  '',
  'English',
  '日本語',
  'ひらがな',
  '한글',
  '🙂',
  '\ud800',
  '\udfff',
  '𠀀',
  '\u2e80',
  '\u3005',
  '\u3007',
  '\ufe35',
  '\u{323b0}',
  '\u{33479}',
]
const fallbacks = fallbackTexts.map((text) => ({
  input: JSON.stringify(text),
  expected: fallbackSource([{ senderId: 'person', parts: [textPart(text)] }], zhCN, en),
}))

// Derive Han ranges by asking the actual source acknowledgement selector for
// every Unicode code point. This pins its Unicode version independently of Go.
const localeGuard = new ReplyGrounding()
localeGuard.addFeedbackSource('feedback', [textPart('English')])
localeGuard.observeTool('memes.feedback', receipt())
const request = segments('ignored', 'feedback'),
  ranges = []
let begin
for (let cp = 0; cp <= 0x10ffff; cp++) {
  localeGuard.addFeedbackSource('feedback', [textPart(String.fromCodePoint(cp))])
  const han =
    localeGuard.normalizeFeedback(request)[0].parts[0].text === zhCN.feedbackReceiptNegative
  if (han && begin === undefined) begin = cp
  if (!han && begin !== undefined) {
    ranges.push([begin, cp - 1])
    begin = undefined
  }
}
if (begin !== undefined) ranges.push([begin, 0x10ffff])
const locale = (name, data) =>
  `var ${name} = groundLocale{\n${[
    ['negative', 'feedbackReceiptNegative'],
    ['positive', 'feedbackReceiptPositive'],
    ['disabled', 'feedbackReceiptDisabled'],
    ['aggregate', 'feedbackReceiptAggregate'],
    ['fallback', 'groundingFallback'],
  ]
    .map(([field, key]) => `\t${field}: ${JSON.stringify(data[key])},`)
    .join('\n')}\n}\n`
const generated =
  '// Code generated by backend/tests/generate-reply-grounding-fixtures.mjs; DO NOT EDIT.\npackage botruntime\n\n' +
  locale('groundEnglish', en) +
  '\n' +
  locale('groundChinese', zhCN) +
  '\n' +
  '// Han membership observed from the actual source class with Node Unicode ' +
  process.versions.unicode +
  '.\n' +
  'var groundHanRanges = [][2]rune{\n' +
  ranges.map(([lo, hi]) => `\t{0x${lo.toString(16)}, 0x${hi.toString(16)}},`).join('\n') +
  '\n}\n'
const paths = [
  'src/runtime.ts',
  'i18n/locales/en.json',
  'i18n/locales/zh-CN.json',
  'tests/reply-grounding.test.ts',
  'package-lock.json',
]
const output = {
  node: process.version,
  unicode: process.versions.unicode,
  sources: paths.map((path) => ({
    path,
    sha256: createHash('sha256').update(readFileSync(path)).digest('hex'),
  })),
  hanRanges: ranges,
  fallbacks,
  cases,
}
for (const [path, data] of [
  ['backend/internal/botruntime/testdata/grounding.json', JSON.stringify(output, null, 2) + '\n'],
  [
    'backend/internal/botruntime/grounding_data_generated.go',
    execFileSync('gofmt', { input: generated, encoding: 'utf8' }),
  ],
]) {
  if (check) assert.equal(readFileSync(path, 'utf8'), data, path)
  else writeFileSync(path, data)
}
console.log(
  `${cases.length} actual-source grounding traces; ${fallbacks.length} source fallback cases; exhaustive Unicode Han classification (${ranges.length} ranges)`,
)
