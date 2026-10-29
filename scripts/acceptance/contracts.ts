export interface SafetyCase {
  id: string
  combinationKey: string
  file: string
  testName: string
  mechanism: string
  scope: string
  authorityChange: string
  expectedOutcome: string
  fixtureHash: string
  evidenceClass: 'synthetic_mechanism'
  semanticGold: 'not_claimed'
}
export interface SafetyManifest {
  schemaVersion: 1
  manifestId: string
  minimumCases: number
  minimumMechanisms: number
  cases: SafetyCase[]
}
export interface CaseResult {
  caseId: string
  file: string
  testName: string
  status: 'passed' | 'failed' | 'not_run'
  exitCode: number | null
  reason?: string
}
