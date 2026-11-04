import test from 'node:test'
import assert from 'node:assert/strict'
import { usageBucket, usageTimeZone } from '../src/usage-time.ts'
import { formatUsageTime, formatUsageBucket } from '../app/utils/usage-time.ts'

test('local hour buckets distinguish repeated DST hours and respect half-hour transitions', () => {
  assert.equal(
    usageBucket('2026-11-01T05:45:00Z', 'America/New_York', 'hour'),
    '2026-11-01T05:00:00.000Z',
  )
  assert.equal(
    usageBucket('2026-11-01T06:45:00Z', 'America/New_York', 'hour'),
    '2026-11-01T06:00:00.000Z',
  )
  assert.equal(
    usageBucket('2026-03-08T07:15:00Z', 'America/New_York', 'hour'),
    '2026-03-08T07:00:00.000Z',
  )
  assert.equal(
    usageBucket('2026-10-03T15:45:00Z', 'Australia/Lord_Howe', 'hour'),
    '2026-10-03T15:30:00.000Z',
  )
  assert.equal(
    usageBucket('2026-04-04T15:15:00Z', 'Australia/Lord_Howe', 'hour'),
    '2026-04-04T15:00:00.000Z',
  )
  assert.equal(usageTimeZone('Etc/UTC'), 'UTC')
  assert.equal(usageTimeZone('invalid/timezone'), undefined)
})
test('display uses browser timezone without reinterpreting local calendar-day labels', () => {
  assert.match(
    formatUsageTime('2026-11-03T18:45:00Z', 'en-US', 'Asia/Kathmandu'),
    /11\/04\/2026, 00:30 GMT\+5:45/,
  )
  assert.equal(formatUsageBucket('2026-11-04', 'en-US', 'America/Los_Angeles'), '2026-11-04')
  assert.notEqual(
    formatUsageTime('2026-11-01T05:30:00Z', 'en-US', 'America/New_York'),
    formatUsageTime('2026-11-01T06:30:00Z', 'en-US', 'America/New_York'),
  )
})

test('minute memo is bounded and avoids per-event formatting without merging DST segments', () => {
  const original = Intl.DateTimeFormat.prototype.formatToParts
  let calls = 0
  Intl.DateTimeFormat.prototype.formatToParts = function (date) {
    calls++
    return original.call(this, date)
  }
  try {
    const start = Date.parse('2027-10-02T15:45:00Z')
    const zone = 'Australia/Lord_Howe'
    const first = usageBucket(new Date(start).toISOString(), zone, 'hour')
    assert.equal(first, '2027-10-02T15:30:00.000Z')
    assert.ok(calls <= 8, 'uncached half-hour transition uses logarithmic search')
    const coldCalls = calls
    for (let i = 0; i < 1000; i++) {
      assert.equal(usageBucket(new Date(start + i * 59).toISOString(), zone, 'hour'), first)
    }
    assert.equal(calls, coldCalls, 'events within a UTC minute reuse the bucket')
    for (let i = 1; i <= 8192; i++) {
      usageBucket(new Date(start + i * 60000).toISOString(), zone, 'day')
    }
    const afterFill = calls
    usageBucket(new Date(start).toISOString(), zone, 'hour')
    assert.ok(calls > afterFill, 'oldest entry is evicted at the global capacity')
    assert.equal(usageBucket('2027-04-03T14:59:59.999Z', zone, 'hour'), '2027-04-03T14:00:00.000Z')
    assert.equal(usageBucket('2027-04-03T15:00:00.001Z', zone, 'hour'), '2027-04-03T15:00:00.000Z')
  } finally {
    Intl.DateTimeFormat.prototype.formatToParts = original
  }
})
