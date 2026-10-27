import type { AdapterFactory } from './adapters.ts'
import type { ModelProtocol } from '../model-protocols.ts'
import type { SendHook, TurnContext, RuntimeEvent } from '../runtime.ts'
import type { ToolDefinition } from '../models.ts'
import type { SendResult } from '../onebot.ts'
export interface PluginToolContext extends TurnContext {
  send(input: unknown): Promise<SendResult[]>
}

export interface PluginTool extends ToolDefinition {
  effect: 'read' | 'learningCandidate' | 'send'
  outputSchema?: Record<string, unknown>
  execute(input: Record<string, unknown>, context: PluginToolContext): Promise<unknown>
}
export interface AdminPage {
  id: string
  title: string
  entry: string
}
export interface PluginManifest {
  id: string
  name: string
  version: string
  description: string
  configSchema: Record<string, unknown>
  capabilities: string[]
}
export interface PluginContext {
  requestId: string
  config: Record<string, unknown>
  signal: AbortSignal
  registerTool(tool: PluginTool): void
  registerModelProtocol(name: string, label: string, protocol: ModelProtocol): void
  registerAdapter(name: string, factory: AdapterFactory): void
  beforeSend(hook: Omit<SendHook, 'id'>): void
  registerAdminPage(page: AdminPage): void
  registerAdminAction(name: string, handler: (input: unknown) => Promise<unknown>): void
  on(
    type: RuntimeEvent['type'],
    listener: (event: Readonly<RuntimeEvent>) => void | Promise<void>,
  ): void
  onDispose(cleanup: () => void | Promise<void>): void
  log(code: string): void
}
export interface LocalPlugin {
  manifest: PluginManifest
  prepareConfiguration?(config: Record<string, unknown>): Record<string, unknown>
  configuredSecrets?(): Record<string, boolean>
  validateConfiguration?(config: Record<string, unknown>): void
  setup(context: PluginContext): void | Promise<void>
}
