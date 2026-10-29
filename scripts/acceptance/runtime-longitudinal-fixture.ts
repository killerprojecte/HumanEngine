import { createHash } from 'node:crypto'

/** Synthetic event dates are declarations, not elapsed execution or billing time. */
export const longitudinalFixtureVersion = 'runtime-longitudinal-fixture-v1'
export const longitudinalPeople = { alice: 'alice', bob: 'bob' } as const
export const longitudinalVariants = [
  { id: 'no-memory', memory: false, qualified: false },
  { id: 'ordinary', memory: true, qualified: false },
  { id: 'qualified-feature', memory: true, qualified: true },
] as const
export type LongitudinalVariant = (typeof longitudinalVariants)[number]
export const longitudinalFacts = {
  coffee: 'Alice explicitly prefers coffee; no date of preference onset was stated.',
  tea: 'Alice explicitly corrects the preference to tea, not coffee; onset remains unknown.',
  bob: 'Bob explicitly prefers water; this is not Alice despite the same displayed nickname.',
} as const
export const longitudinalEvents = [
  {
    id: 'alice-original',
    person: 'alice',
    eventDate: '2026-10-01T18:00:00Z',
    kind: 'coffee',
    text: 'I prefer coffee.',
  },
  {
    id: 'alice-independent',
    person: 'alice',
    eventDate: '2026-10-02T18:00:00Z',
    kind: 'coffee',
    text: 'I independently confirm that I prefer coffee.',
  },
  {
    id: 'bob-independent',
    person: 'bob',
    eventDate: '2026-10-02T19:00:00Z',
    kind: 'bob',
    text: 'I prefer water.',
  },
  {
    id: 'alice-correction',
    person: 'alice',
    eventDate: '2026-10-03T18:00:00Z',
    kind: 'tea',
    text: 'Correction: my preference is tea, not coffee.',
  },
  {
    id: 'plan-18',
    person: 'alice',
    eventDate: '2026-10-04T10:00:00Z',
    kind: 'propose',
    text: 'I propose that Alice and Bob meet at 18:00; date and timezone are not specified.',
  },
  {
    id: 'confirm-18',
    person: 'bob',
    eventDate: '2026-10-04T11:00:00Z',
    kind: 'confirm',
    text: 'I explicitly agree to the proposed 18:00 arrangement.',
  },
  {
    id: 'amend-19',
    person: 'alice',
    eventDate: '2026-10-05T10:00:00Z',
    kind: 'amend',
    text: 'I propose changing the arrangement to 19:00; the date and timezone remain unknown.',
  },
  {
    id: 'confirm-19',
    person: 'bob',
    eventDate: '2026-10-05T11:00:00Z',
    kind: 'confirm',
    text: 'I explicitly agree to the amended 19:00 arrangement.',
  },
] as const
export type LongitudinalEvent = (typeof longitudinalEvents)[number]
export const longitudinalFixtureHash = createHash('sha256')
  .update(
    JSON.stringify({
      version: longitudinalFixtureVersion,
      people: longitudinalPeople,
      variants: longitudinalVariants,
      events: longitudinalEvents,
      facts: longitudinalFacts,
    }),
  )
  .digest('hex')
