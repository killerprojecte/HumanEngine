import type { ModelInstance, Provider } from '../models.ts'

// Deliberately bounded to documented model IDs, rather than guessing from a gateway's
// display name or treating every OpenAI-compatible endpoint as the official API.
const officialModels = /^(?:gpt-5\.6|gpt-6\.1-sol|gpt-6\.1-terra)(?:-\d{4}-\d{2}-\d{2})?$/

export function supportsOpenAIPromptBreakpoints(provider: Provider, model: ModelInstance): boolean {
  if (!['openai-chat', 'openai-responses'].includes(provider.protocol)) return false
  if (model.promptCacheMode === 'off') return false
  // An explicit selection is a user assertion of the endpoint/model capability.
  if (model.promptCacheMode === 'openai') return true
  try {
    const url = new URL(provider.baseUrl)
    return (
      url.protocol === 'https:' &&
      url.hostname === 'api.openai.com' &&
      !url.port &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash &&
      /^\/v1\/?$/.test(url.pathname) &&
      officialModels.test(model.apiModelName)
    )
  } catch {
    return false
  }
}
