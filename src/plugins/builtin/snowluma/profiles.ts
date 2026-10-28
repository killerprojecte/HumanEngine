import type { Message, Obj, SceneRef } from './protocol.ts'

export const displayName = (value: unknown): string | undefined => {
  if (typeof value !== 'string') return
  const name = value
    .replace(/[\u0000-\u001f\u007f\u202a-\u202e\u2066-\u2069]/g, '')
    .trim()
    .slice(0, 128)
  return name || undefined
}
/** Only canonical numeric QQ identifiers can form these platform-owned avatar URLs. */
export function qqAvatar(id: string, group = false): string | undefined {
  if (!/^[1-9][0-9]{0,19}$/.test(id)) return
  return group
    ? `https://p.qlogo.cn/gh/${id}/${id}/100`
    : `https://q1.qlogo.cn/g?b=qq&nk=${id}&s=100`
}
/** Do not map another adapter's numeric identifiers to QQ identities. */
export function isOneBotScene(scene: SceneRef): boolean {
  return (
    scene.id ===
    `ob:s:${Buffer.from(JSON.stringify([scene.adapterId, scene.accountId, scene.kind, scene.platformSceneId])).toString('base64url')}`
  )
}
export interface ProfileUpdate {
  scene: SceneRef
  sender?: { id: string; platformId: string; displayName?: string; avatarUrl?: string }
}

/** Opportunistic, bounded lookups; never hold ingress or query on a UI read. */
export class OneBotProfiles {
  private readonly cache = new Map<string, { until: number; name?: string }>()
  private readonly pending = new Set<string>()
  private readonly controller = new AbortController()
  constructor(
    private readonly action: (name: string, params: Obj, signal?: AbortSignal) => Promise<unknown>,
    private readonly update: (profile: ProfileUpdate) => void,
    private readonly failure: () => void,
    private readonly now = Date.now,
  ) {}
  dispose() {
    this.controller.abort()
    this.cache.clear()
  }
  observe(message: Message, scene: SceneRef): void {
    if (this.controller.signal.aborted || message.status !== 'available') return
    const senderId = message.senderPlatformId
    const sceneKey = `scene:${scene.id}`
    const senderKey = `sender:${scene.id}:${message.senderId}`
    const needsSceneLookup = !scene.displayName
    const needsSenderLookup = !message.senderDisplayName
    if (scene.displayName) this.remember(sceneKey, scene.displayName)
    else scene.displayName = this.cache.get(sceneKey)?.name
    if (message.senderDisplayName) this.remember(senderKey, message.senderDisplayName)
    else message.senderDisplayName = this.cache.get(senderKey)?.name
    if (needsSceneLookup)
      this.lookup(
        sceneKey,
        scene.kind === 'group' ? 'get_group_info' : 'get_stranger_info',
        { [scene.kind === 'group' ? 'group_id' : 'user_id']: scene.platformSceneId },
        (data) => {
          const returnedId = String(data[scene.kind === 'group' ? 'group_id' : 'user_id'] ?? '')
          return returnedId === scene.platformSceneId
            ? displayName(data[scene.kind === 'group' ? 'group_name' : 'nickname'])
            : undefined
        },
        (name) => this.update({ scene: { ...scene, displayName: name } }),
      )
    if (senderId && needsSenderLookup)
      this.lookup(
        senderKey,
        'get_stranger_info',
        { user_id: senderId },
        (data) =>
          String(data.user_id ?? '') === senderId ? displayName(data.nickname) : undefined,
        (name) =>
          this.update({
            scene,
            sender: {
              id: message.senderId,
              platformId: senderId,
              displayName: name,
              avatarUrl: qqAvatar(senderId),
            },
          }),
      )
  }
  private remember(key: string, name?: string, ttl = 30 * 60_000) {
    this.cache.delete(key)
    this.cache.set(key, { name, until: this.now() + ttl })
    while (this.cache.size > 500) this.cache.delete(this.cache.keys().next().value!)
  }
  private lookup(
    key: string,
    action: string,
    params: Obj,
    extract: (data: Obj) => string | undefined,
    done: (name: string) => void,
  ) {
    if (
      (this.cache.get(key)?.until ?? 0) > this.now() ||
      this.pending.has(key) ||
      this.pending.size >= 4
    )
      return
    const previous = this.cache.get(key)
    this.pending.add(key)
    void Promise.resolve().then(async () => {
      try {
        const value = await this.action(action, params, this.controller.signal)
        if (this.controller.signal.aborted || this.cache.get(key) !== previous) return
        const name =
          value && typeof value === 'object' && !Array.isArray(value)
            ? extract(value as Obj)
            : undefined
        this.remember(key, name, name ? 30 * 60_000 : 5 * 60_000)
        if (name) done(name)
      } catch {
        if (!this.controller.signal.aborted && this.cache.get(key) === previous) {
          this.remember(key, undefined, 5 * 60_000)
          this.failure()
        }
      } finally {
        this.pending.delete(key)
      }
    })
  }
}
