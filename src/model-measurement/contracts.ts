/** Text-token counts are not calibrated provider input tokens or a spending cap. */
export interface TokenizerTarget {
  providerId: string
  protocol: string
  apiModelName: string
  codecId: string
}
export interface TrustedTargetTokenizer {
  target: Readonly<TokenizerTarget>
  id: string
  revision: string
  count(serializedRequest: string): number
}
export interface RequestMeasurement {
  bodyBytes?: number
  textTokens?: number
  tokenizerContract?: string
  reason:
    | 'unregistered_tokenizer'
    | 'registered_text_counter'
    | 'counter_failed'
    | 'invalid_counter'
    | 'custom_wire_unknown'
  providerTokenCount: 'unknown'
  providerFraming: 'uncalibrated'
  hardTokenCap: false
}
export interface RequestMeasurementSummary {
  attempts: number
  measuredBodyAttempts: number
  bodyUtf8Bytes: number | null
  bytesCoverage: 'complete' | 'partial' | 'unknown'
  textCountedAttempts: number
  serializedTextTokens: number | null
  textTokenCoverage: 'complete' | 'partial' | 'unknown'
  tokenizerContracts: string[]
  contractCoverage: 'complete' | 'limited'
  providerTokenCount: 'unknown'
  providerFraming: 'uncalibrated'
  hardTokenCap: false
}
