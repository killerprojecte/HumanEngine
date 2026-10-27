// A deliberately small JSON Schema form vocabulary, rejected rather than silently ignored when unsupported.
export function validateConfig(value: unknown, schema: Record<string, unknown>): void {
  const allowed = new Set([
    'type',
    'properties',
    'required',
    'additionalProperties',
    'description',
    'title',
    'default',
    'enum',
    'minimum',
    'maximum',
    'pattern',
    'minLength',
    'maxLength',
    'items',
    'secret',
    'group',
  ])
  for (const key of Object.keys(schema))
    if (!allowed.has(key) && key !== '$schema') throw new Error('unsupported_schema_keyword')
  if (schema.enum && !(schema.enum as unknown[]).includes(value)) throw new Error('invalid_enum')
  if (schema.type === 'object') {
    if (!value || typeof value !== 'object' || Array.isArray(value))
      throw new Error('expected_object')
    const object = value as Record<string, unknown>,
      properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>
    for (const key of (schema.required ?? []) as string[])
      if (!(key in object)) throw new Error('required_property')
    for (const [key, entry] of Object.entries(object)) {
      if (!properties[key]) {
        if (schema.additionalProperties !== true) throw new Error('unknown_property')
      } else validateConfig(entry, properties[key])
    }
  } else if (schema.type === 'string') {
    if (
      typeof value !== 'string' ||
      (typeof schema.pattern === 'string' && !new RegExp(schema.pattern).test(value)) ||
      (typeof schema.minLength === 'number' && value.length < schema.minLength) ||
      (typeof schema.maxLength === 'number' && value.length > schema.maxLength)
    )
      throw new Error('expected_string')
  } else if (schema.type === 'boolean') {
    if (typeof value !== 'boolean') throw new Error('expected_boolean')
  } else if (schema.type === 'integer' || schema.type === 'number') {
    if (
      typeof value !== 'number' ||
      !Number.isFinite(value) ||
      (schema.type === 'integer' && !Number.isInteger(value)) ||
      (typeof schema.minimum === 'number' && value < schema.minimum) ||
      (typeof schema.maximum === 'number' && value > schema.maximum)
    )
      throw new Error('expected_number')
  } else if (schema.type === 'array') {
    if (!Array.isArray(value)) throw new Error('expected_array')
    for (const entry of value) validateConfig(entry, schema.items as Record<string, unknown>)
  } else throw new Error('unsupported_schema_type')
}
export function redactConfig(value: unknown, schema: Record<string, unknown>): unknown {
  if (schema.secret === true) return undefined
  if (schema.type === 'object' && value && typeof value === 'object' && !Array.isArray(value)) {
    const properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>,
      result: Record<string, unknown> = {}
    for (const [key, entry] of Object.entries(value)) {
      const next = properties[key] ? redactConfig(entry, properties[key]) : entry
      if (next !== undefined) result[key] = next
    }
    return result
  }
  if (schema.type === 'array' && Array.isArray(value))
    return value.map(
      (entry) => redactConfig(entry, (schema.items ?? {}) as Record<string, unknown>) ?? null,
    )
  return value
}
export function preserveSecrets(
  value: unknown,
  previous: unknown,
  schema: Record<string, unknown>,
): unknown {
  if (schema.secret === true) return value == null ? previous : value
  if (schema.type === 'object' && value && typeof value === 'object' && !Array.isArray(value)) {
    const result = { ...value } as Record<string, unknown>,
      old = previous && typeof previous === 'object' ? (previous as Record<string, unknown>) : {},
      properties = (schema.properties ?? {}) as Record<string, Record<string, unknown>>
    for (const [key, field] of Object.entries(properties)) {
      if (!(key in result) && !(key in old)) continue
      const next = preserveSecrets(result[key], old[key], field)
      if (next !== undefined) result[key] = next
    }
    return result
  }
  if (schema.type === 'array' && Array.isArray(value))
    return value.map((entry, index) =>
      preserveSecrets(
        entry,
        Array.isArray(previous) ? previous[index] : undefined,
        (schema.items ?? {}) as Record<string, unknown>,
      ),
    )
  return value
}
export function publicSchema(schema: Record<string, unknown>): Record<string, unknown> {
  const result = structuredClone(schema)
  if ('default' in result) {
    const safe = redactConfig(result.default, result)
    if (safe === undefined) delete result.default
    else result.default = safe
  }
  if (result.properties && typeof result.properties === 'object')
    result.properties = Object.fromEntries(
      Object.entries(result.properties as Record<string, Record<string, unknown>>).map(
        ([key, field]) => [key, publicSchema(field)],
      ),
    )
  if (result.items && typeof result.items === 'object')
    result.items = publicSchema(result.items as Record<string, unknown>)
  return result
}
