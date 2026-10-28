import test from 'node:test'
import assert from 'node:assert/strict'
import { chatMediaUrl, chatImageTypes, historyReasonKey } from '../app/utils/chatMedia.ts'

test('chat images only fetch bound same-origin server resources, never user URLs or asset locators', () => {
  const path = '/api/scenes/ob%3As%3Asynthetic/messages/ob%3Am%3Asynthetic/media/0'
  assert.equal(chatMediaUrl(path), path)
  for (const bad of [
    undefined,
    '',
    '//evil.invalid/a',
    'https://evil.invalid/a',
    'data:image/png;base64,x',
    'blob:x',
    '/api/media?url=x',
    path + '?url=https://evil.invalid',
    path + '#fragment',
    '/api/scenes/a/messages/b/media/-1',
    '/api/scenes/a/messages/b/media/01',
    '/api/scenes/a/messages/b/media/0/extra',
  ])
    assert.equal(chatMediaUrl(bad), undefined)
  assert.equal(chatImageTypes.has('image/svg+xml'), false)
  assert.equal(chatImageTypes.has('text/html'), false)
  assert.equal(chatImageTypes.has('image/gif'), true)
})

test('history transport reasons are localized and unknown diagnostics never become captions', () => {
  assert.equal(historyReasonKey('platform_history_window'), 'cwHistoryWindow')
  assert.equal(historyReasonKey('history_exhausted_or_unavailable'), 'cwHistoryNoMore')
  assert.equal(historyReasonKey('unsupported'), 'cwHistoryUnsupported')
  assert.equal(historyReasonKey('internal_error_with_untrusted_payload'), 'cwHistoryLimited')
})
