import { createHash } from 'node:crypto'
import { readFileSync, readdirSync } from 'node:fs'
import { join, relative } from 'node:path'
import type { SafetyManifest } from './contracts.ts'

export const sha256 = (value: string | Buffer): string =>
  createHash('sha256').update(value).digest('hex')
export function readManifest(root: string): SafetyManifest {
  return JSON.parse(readFileSync(join(root, 'docs/acceptance/safety-manifest.json'), 'utf8'))
}
/** Exact checked-in code/fixture snapshot; no database, credentials, raw chats or build output. */
export function acceptanceSourceHash(root: string): string {
  const files: string[] = []
  const walk = (directory: string) => {
    for (const entry of readdirSync(join(root, directory), { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isSymbolicLink()) throw new Error('acceptance_source_symlink')
      if (entry.isDirectory()) walk(path)
      else if (entry.isFile()) files.push(path)
    }
  }
  for (const directory of ['src', 'tests', 'scripts']) walk(directory)
  files.push('package.json', 'package-lock.json', 'docs/acceptance/safety-manifest.json')
  files.sort()
  return sha256(
    files
      .map(
        (file) =>
          `${relative(root, join(root, file)).replaceAll('\\', '/')}\0${sha256(readFileSync(join(root, file)))}\n`,
      )
      .join(''),
  )
}
