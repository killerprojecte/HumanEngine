/** Reconstruct only the initial context, before assistant/tool continuation rounds.
 * Native context arrays span multiple real user-message blocks.
 */
export function fixtureContext(messages: readonly { role: string; content: string }[]) {
  const input: Record<string, any> = {}
  for (const message of messages) {
    if (message.role === 'assistant' || message.role === 'tool') break
    if (message.role !== 'user') continue
    const block = JSON.parse(message.content)
    for (const [key, value] of Object.entries(block)) {
      if ((key === 'messages' || key === 'evidence') && Array.isArray(value))
        input[key] = [...(input[key] ?? []), ...value]
      else input[key] = value
    }
  }
  return input
}
