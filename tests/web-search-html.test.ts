import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildHtmlRequest, parseHtml } from '../src/plugins/builtin/web-search/providers/html.ts'

test('HTML requests use fixed endpoints and encode query as data', () => {
  for (const provider of ['bing', 'google', 'sogou', 'duckduckgo'] as const) {
    const request = buildHtmlRequest(provider, { query: 'x&url=http://127.0.0.1', limit: 3 })
    assert.equal(
      request.url.searchParams.get(provider === 'sogou' ? 'query' : 'q'),
      'x&url=http://127.0.0.1',
    )
    assert.equal(request.init.redirect, 'error')
    assert.equal(request.url.searchParams.has('url'), false)
  }
})
test('independent synthetic fixtures extract four HTML providers', () => {
  const fixtures = {
    bing: '<html><ol><li class="b_algo"><h2><a href="https://example.com/a">One &amp; Two</a></h2><div class="b_caption"><p>Summary</p></div></li></ol></html>',
    google:
      '<html><div class="g"><a href="/url?q=https%3A%2F%2Fexample.com%2Fa"><h3>One &amp; Two</h3></a><div class="VwiC3b">Summary</div></div></html>',
    sogou:
      '<html><div class="vrwrap"><h3><a href="https://example.com/a">One &amp; Two</a></h3><div class="fz-mid">Summary</div></div></html>',
    duckduckgo:
      '<html><div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa">One &amp; Two</a><a class="result__snippet">Summary</a></div></html>',
  }
  for (const provider of Object.keys(fixtures) as (keyof typeof fixtures)[]) {
    assert.deepEqual(parseHtml(provider, fixtures[provider]), [
      { provider, title: 'One & Two', url: 'https://example.com/a', snippet: 'Summary' },
    ])
  }
})
test('Bing tracking URLs decode without a network follow-up', () => {
  const encoded = Buffer.from('https://example.com/story').toString('base64url')
  const html = `<html><li class="b_algo"><h2><a href="https://www.bing.com/ck/a?u=a1${encoded}">Story</a></h2></li></html>`
  assert.equal(parseHtml('bing', html)[0]?.url, 'https://example.com/story')
})
test('fail closed on challenge, malformed, oversized and unsafe-only results', () => {
  for (const html of [
    '{"results": []}',
    '<html><title>Just a moment</title></html>',
    '<html><form action="/anomaly.js">captcha</form></html>',
    '<html><div class="g"><a href="javascript:alert(1)"><h3>Unsafe</h3></a></div></html>',
    '<html>' + 'x'.repeat(1_048_576),
    '<html><div>New unknown markup</div></html>',
  ]) {
    assert.throws(() => parseHtml('google', html), { message: 'invalid_response' })
  }
  assert.deepEqual(parseHtml('bing', '<html><body>No results found.</body></html>'), [])
})
test('scripts are not treated as title text and private URLs are rejected', () => {
  const html =
    '<html><li class="b_algo"><h2><a href="https://example.com/">Safe<script>steal()</script></a></h2></li><li class="b_algo"><h2><a href="http://127.0.0.1/">Private</a></h2></li></html>'
  assert.deepEqual(
    parseHtml('bing', html).map((item) => item.title),
    ['Safe'],
  )
})

test('malformed markup and long valueless attributes are processed in bounded time', () => {
  const started = performance.now()
  for (const body of [
    '<html>' + '<'.repeat(65536),
    '<html><div ' + 'a'.repeat(65536) + '></div></html>',
    '<html>' + '<!--'.repeat(16384),
    '<html>' + '<form '.repeat(16384),
  ])
    assert.throws(() => parseHtml('bing', body), { message: 'invalid_response' })
  // Loose CI allowance; the previous quadratic tokenizer took >9 seconds for one fixture.
  assert.ok(performance.now() - started < 1500)
})

test('many whitespace-only attribute tails are scanned monotonically', () => {
  const body = '<html>' + ('<div ' + ' '.repeat(7000) + '></div>').repeat(128) + '</html>'
  const started = performance.now()
  assert.throws(() => parseHtml('bing', body), { message: 'invalid_response' })
  assert.ok(performance.now() - started < 1500)
})
