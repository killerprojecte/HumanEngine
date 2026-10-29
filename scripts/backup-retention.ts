import { resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { BackupRetention } from '../src/backup-retention/service.ts'
export function retentionCommand(args: readonly string[]) {
  const [command, registry, ...rest] = args
  if (!registry) throw new Error('retention_usage')
  if (command === 'init' && rest.length === 1) {
    const service = BackupRetention.registerRoot(rest[0]!, registry)
    try {
      return service.inventory()
    } finally {
      service.close()
    }
  }
  const service = new BackupRetention(registry)
  try {
    if (command === 'register' && rest.length === 2) {
      service.registerBackup(rest[0]!, rest[1]!)
      return service.inventory()
    }
    if (command === 'inventory' && rest.length === 0) return service.inventory()
    if (
      command === 'expire' &&
      (rest.length === 0 || (rest.length === 1 && rest[0] === '--execute'))
    )
      return service.expire({ execute: rest[0] === '--execute' })
    throw new Error('retention_usage')
  } finally {
    service.close()
  }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    console.log(JSON.stringify(retentionCommand(process.argv.slice(2)), null, 2))
  } catch {
    console.error(
      'Retention refused. Usage: init REGISTRY ROOT | register REGISTRY BACKUP_NAME EXPIRY_ISO | inventory REGISTRY | expire REGISTRY [--execute]. Use only an exclusively managed local root; preserve its independent recovery authority.',
    )
    process.exitCode = 1
  }
}
