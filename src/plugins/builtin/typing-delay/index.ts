import type { LocalPlugin } from '../../types.ts'
import type { Runtime } from '../../../runtime.ts'
import { TypingDelayConfigSchema, typingDelayConfigSchema } from './config.ts'
export function typingDelayPlugin(runtime: Runtime): LocalPlugin {
  return {
    manifest: {
      id: 'typing-delay',
      name: 'Typing delay',
      version: '0.1.0',
      description:
        'Optional human typing delay for each complete outgoing message segment, not each sentence.',
      capabilities: ['send-delay', 'schema-form'],
      configSchema: typingDelayConfigSchema,
    },
    validateConfiguration: (value) => {
      TypingDelayConfigSchema.parse(value)
    },
    setup(context) {
      context.onDispose(
        runtime.installTypingDelay(TypingDelayConfigSchema.parse(context.config), context.signal),
      )
    },
  }
}
