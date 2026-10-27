import {
  SearchError,
  type ProviderId,
  type ProviderRequest,
  type SearchRequest,
  type SearchResult,
} from '../types.ts'
import { result, uniqueResults } from './results.ts'

/** Experimental public SERP parsing. No authentication, challenge solving, or linked-page fetching. */
export function buildHtmlRequest(provider: ProviderId, input: SearchRequest): ProviderRequest {
  const endpoints: Partial<Record<ProviderId, string>> = {
    bing: 'https://www.bing.com/search',
    google: 'https://www.google.com/search',
    sogou: 'https://www.sogou.com/web',
    duckduckgo: 'https://html.duckduckgo.com/html/',
  }
  const endpoint = endpoints[provider]
  if (!endpoint) throw new SearchError('provider_disabled')
  const url = new URL(endpoint)
  url.searchParams.set(provider === 'sogou' ? 'query' : 'q', input.query)
  return {
    url,
    format: 'html',
    init: {
      method: 'GET',
      redirect: 'error',
      headers: { Accept: 'text/html', 'User-Agent': 'HumanEngine-WebSearch/1.0' },
    },
  }
}

type Element = {
  tag: string
  attrs: Record<string, string>
  children: (Element | string)[]
  parent?: Element
}
const voidTags = new Set([
  'area',
  'base',
  'br',
  'col',
  'embed',
  'hr',
  'img',
  'input',
  'link',
  'meta',
  'param',
  'source',
  'track',
  'wbr',
])
function decode(text: string): string {
  const named: Record<string, string> = {
    amp: '&',
    lt: '<',
    gt: '>',
    quot: '"',
    apos: "'",
    nbsp: ' ',
  }
  return text.replace(
    /&(#x[\da-f]{1,8}|#\d{1,10}|amp|lt|gt|quot|apos|nbsp);/gi,
    (whole, code: string) => {
      if (!code.startsWith('#')) return named[code.toLowerCase()] ?? whole
      const point =
        code[1]?.toLowerCase() === 'x' ? parseInt(code.slice(2), 16) : parseInt(code.slice(1), 10)
      return point > 0 && point <= 0x10ffff && !(point >= 0xd800 && point <= 0xdfff)
        ? String.fromCodePoint(point)
        : ''
    },
  )
}
/** Monotonic scanning prevents malformed '<' runs from causing regex rescans. */
function* tokens(body: string): Generator<string> {
  let offset = 0
  while (offset < body.length) {
    if (body[offset] !== '<') {
      const next = body.indexOf('<', offset)
      const end = next < 0 ? body.length : next
      yield body.slice(offset, end)
      offset = end
      continue
    }
    if (body.startsWith('<!--', offset)) {
      const end = body.indexOf('-->', offset + 4)
      if (end < 0) throw new SearchError('invalid_response')
      offset = end + 3
      continue
    }
    const end = body.indexOf('>', offset + 1)
    // Overlong or unterminated tags are unsupported, not retried at each '<'.
    if (end < 0 || end - offset > 8192) throw new SearchError('invalid_response')
    yield body.slice(offset, end + 1)
    offset = end + 1
  }
}
function parse(body: string): Element[] {
  if (body.length > 1_048_576 || !/<(?:html|body|div|ol)\b/i.test(body))
    throw new SearchError('invalid_response')
  const root: Element = { tag: 'root', attrs: {}, children: [] }
  const stack = [root]
  const elements: Element[] = []
  for (const value of tokens(body)) {
    if (value.startsWith('<!--') || value.startsWith('<!')) continue
    const end = /^<\/\s*([\w:-]+)/.exec(value)
    const current = stack.at(-1)!
    if (
      ['script', 'style', 'noscript', 'svg'].includes(current.tag) &&
      end?.[1]?.toLowerCase() !== current.tag
    )
      continue
    if (end) {
      const name = end[1]!.toLowerCase()
      for (let i = stack.length - 1; i > 0; i--)
        if (stack[i]!.tag === name) {
          stack.length = i
          break
        }
      continue
    }
    const start = /^<\s*([\w:-]+)/.exec(value)
    if (!start) {
      stack.at(-1)!.children.push(decode(value))
      continue
    }
    if (elements.length >= 30_000 || stack.length >= 128) throw new SearchError('invalid_response')
    const attrs: Record<string, string> = Object.create(null)
    // Sticky matching never restarts a failed attribute match at every suffix.
    const attributes = /([^\s=<>/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/y
    let position = start[0].length
    while (position < value.length) {
      while (position < value.length && /\s/.test(value[position]!)) position++
      if (position >= value.length) break
      attributes.lastIndex = position
      const attr = attributes.exec(value)
      if (!attr) {
        position++
        continue
      }
      position = attributes.lastIndex
      attrs[attr[1]!.toLowerCase()] = decode(attr[2] ?? attr[3] ?? attr[4] ?? '')
    }
    const node: Element = {
      tag: start[1]!.toLowerCase(),
      attrs,
      children: [],
      parent: stack.at(-1)!,
    }
    node.parent!.children.push(node)
    elements.push(node)
    if (!voidTags.has(node.tag) && !value.endsWith('/>')) stack.push(node)
  }
  return elements
}
function plain(node: Element): string {
  if (['script', 'style', 'noscript', 'svg'].includes(node.tag)) return ''
  return node.children
    .map((child) => (typeof child === 'string' ? child : plain(child)))
    .join(' ')
    .replace(/\s+/g, ' ')
    .trim()
}
function hasClass(node: Element, name: string): boolean {
  return (node.attrs.class ?? '').split(/\s+/).includes(name)
}
function descendants(node: Element): Element[] {
  const found: Element[] = []
  for (const child of node.children)
    if (typeof child !== 'string') {
      found.push(child, ...descendants(child))
    }
  return found
}
function ancestor(node: Element, predicate: (node: Element) => boolean): Element | undefined {
  for (let parent = node.parent; parent; parent = parent.parent)
    if (predicate(parent)) return parent
}
function target(provider: ProviderId, raw: string): string {
  try {
    const base =
      provider === 'google'
        ? 'https://www.google.com'
        : provider === 'duckduckgo'
          ? 'https://duckduckgo.com'
          : provider === 'sogou'
            ? 'https://www.sogou.com'
            : 'https://www.bing.com'
    const url = new URL(raw, base)
    if (provider === 'google' && url.hostname === 'www.google.com' && url.pathname === '/url')
      return url.searchParams.get('q') ?? url.searchParams.get('url') ?? ''
    if (
      provider === 'duckduckgo' &&
      ['duckduckgo.com', 'html.duckduckgo.com'].includes(url.hostname)
    )
      return url.searchParams.get('uddg') ?? ''
    if (
      provider === 'bing' &&
      ['www.bing.com', 'cn.bing.com'].includes(url.hostname) &&
      url.pathname === '/ck/a'
    ) {
      const encoded = url.searchParams.get('u') ?? ''
      return encoded.startsWith('a1')
        ? Buffer.from(encoded.slice(2), 'base64url').toString('utf8')
        : ''
    }
    // Navigation and unresolved redirect URLs are not evidence sources.
    if (
      url.hostname === new URL(base).hostname ||
      /(?:^|\.)(?:google\.com|bing\.com|duckduckgo\.com)$/.test(url.hostname)
    )
      return ''
    return url.href
  } catch {
    return ''
  }
}
export function parseHtml(provider: ProviderId, body: string): SearchResult[] {
  if (!['bing', 'google', 'sogou', 'duckduckgo'].includes(provider))
    throw new SearchError('provider_disabled')
  const nodes = parse(body)
  if (
    nodes.some(
      (node) =>
        (['form', 'iframe'].includes(node.tag) &&
          /captcha|challenge|anomaly/i.test(Object.values(node.attrs).join(' '))) ||
        /g-recaptcha|h-captcha|cf-challenge|anomaly-modal/i.test(
          `${node.attrs.id ?? ''} ${node.attrs.class ?? ''}`,
        ) ||
        (node.tag === 'title' && /captcha|access denied|just a moment/i.test(plain(node))),
    )
  )
    throw new SearchError('invalid_response')
  const output: SearchResult[] = []
  const add = (anchor: Element | undefined, title: Element | undefined, snippet?: Element) => {
    if (!anchor || !title) return
    const item = result(
      provider,
      plain(title),
      target(provider, anchor.attrs.href ?? ''),
      snippet ? plain(snippet) : '',
    )
    if (item) output.push(item)
  }
  if (provider === 'bing' || provider === 'sogou') {
    const cards = nodes.filter((node) =>
      provider === 'bing'
        ? node.tag === 'li' && hasClass(node, 'b_algo')
        : hasClass(node, 'vrwrap') || hasClass(node, 'rb'),
    )
    for (const card of cards.slice(0, 100)) {
      const inside = descendants(card)
      const heading = inside.find((node) => node.tag === (provider === 'bing' ? 'h2' : 'h3'))
      const anchor = heading && descendants(heading).find((node) => node.tag === 'a')
      const snippet =
        inside.find((node) => hasClass(node, provider === 'bing' ? 'b_caption' : 'fz-mid')) ??
        inside.find((node) => node.tag === 'p')
      add(anchor, heading, snippet)
    }
  } else if (provider === 'duckduckgo') {
    for (const anchor of nodes
      .filter((node) => node.tag === 'a' && hasClass(node, 'result__a'))
      .slice(0, 100)) {
      const card = ancestor(anchor, (node) => hasClass(node, 'result'))
      add(
        anchor,
        anchor,
        card && descendants(card).find((node) => hasClass(node, 'result__snippet')),
      )
    }
  } else {
    for (const heading of nodes.filter((node) => node.tag === 'h3').slice(0, 100)) {
      const anchor = ancestor(heading, (node) => node.tag === 'a')
      const card = ancestor(heading, (node) => hasClass(node, 'g') || hasClass(node, 'MjjYud'))
      add(
        anchor,
        heading,
        card &&
          descendants(card).find((node) => hasClass(node, 'VwiC3b') || hasClass(node, 'IsZvec')),
      )
    }
  }
  if (
    !output.length &&
    !/(?:no results|did not match any documents|no more results|没有找到|未找到|找不到相关)/i.test(
      plain({
        tag: 'root',
        attrs: {},
        children: nodes.filter((node) => node.parent?.tag === 'root'),
      }),
    )
  )
    throw new SearchError('invalid_response')
  return uniqueResults(output, 100)
}
