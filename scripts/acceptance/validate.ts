import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import ts from 'typescript'
import { sha256 } from './manifest.ts'
import type { SafetyManifest } from './contracts.ts'

/** Static binding only: never represents execution or semantic correctness. */
export function validateManifest(root: string, manifest: SafetyManifest): void {
  if (
    manifest.schemaVersion !== 1 ||
    manifest.minimumCases < 200 ||
    manifest.minimumMechanisms < 10 ||
    !Array.isArray(manifest.cases) ||
    manifest.cases.length < manifest.minimumCases
  )
    throw new Error('acceptance_manifest_contract')
  const ids = new Set<string>(),
    bindings = new Set<string>(),
    combinations = new Set<string>(),
    mechanisms = new Set<string>()
  const declarations = new Map<string, string[]>()
  for (const item of manifest.cases) {
    if (
      !/^safety-\d{3,}$/.test(item.id) ||
      !/^tests\/[a-z0-9-]+\.test\.ts$/.test(item.file) ||
      !item.testName ||
      !item.scope ||
      !item.authorityChange ||
      !item.expectedOutcome ||
      !item.mechanism ||
      item.evidenceClass !== 'synthetic_mechanism' ||
      item.semanticGold !== 'not_claimed'
    )
      throw new Error('acceptance_case_contract')
    const binding = `${item.file}\0${item.testName}`
    if (
      ids.has(item.id) ||
      bindings.has(binding) ||
      combinations.has(item.combinationKey) ||
      item.combinationKey !== sha256(binding)
    )
      throw new Error('acceptance_duplicate_case')
    ids.add(item.id)
    bindings.add(binding)
    combinations.add(item.combinationKey)
    mechanisms.add(item.mechanism)
    const source = readFileSync(join(root, item.file), 'utf8')
    if (sha256(source) !== item.fixtureHash)
      throw new Error(`acceptance_fixture_changed:${item.file}`)
    if (!declarations.has(item.file)) {
      const ast = ts.createSourceFile(item.file, source, ts.ScriptTarget.Latest, true)
      const names: string[] = []
      // Only top-level literal test(...) registrations, not comments/grep/nested dynamic cases.
      for (const statement of ast.statements) {
        if (!ts.isExpressionStatement(statement) || !ts.isCallExpression(statement.expression))
          continue
        const call = statement.expression
        if (!ts.isIdentifier(call.expression) || call.expression.text !== 'test') continue
        const name = call.arguments[0]
        if (name && ts.isStringLiteral(name)) names.push(name.text)
      }
      declarations.set(item.file, names)
    }
    if (declarations.get(item.file)!.filter((name) => name === item.testName).length !== 1)
      throw new Error(`acceptance_test_binding_missing_or_ambiguous:${item.id}`)
  }
  if (mechanisms.size < manifest.minimumMechanisms) throw new Error('acceptance_mechanism_coverage')
}
