const plainDataObject = (value: unknown): value is Record<string, unknown> => {
  if (!value || typeof value !== 'object') return false
  const prototype = Object.getPrototypeOf(value)
  return (
    (prototype === Object.prototype || prototype === null) &&
    !('toJSON' in value) &&
    Object.values(Object.getOwnPropertyDescriptors(value)).every(
      (descriptor) => 'value' in descriptor,
    )
  )
}

/** Some OpenAI-compatible endpoints require an explicit object root even for object-only unions.
 * A direct object type on every alternative proves this additional constraint redundant.
 * Do not infer object type from properties, references, or mixed/unknown alternatives.
 */
export function nativeToolSchema(schema: Record<string, unknown>): Record<string, unknown> {
  if (!plainDataObject(schema) || Object.hasOwn(schema, 'type')) return schema
  const unions = ['oneOf', 'anyOf'].filter((key) => Object.hasOwn(schema, key))
  if (!unions.length) return schema
  if (
    !unions.every((key) => {
      const descriptor = Object.getOwnPropertyDescriptor(schema, key)
      const branches =
        descriptor && 'value' in descriptor && descriptor.enumerable ? descriptor.value : undefined
      return (
        Array.isArray(branches) &&
        branches.length > 0 &&
        Object.getPrototypeOf(branches) === Array.prototype &&
        !('toJSON' in branches) &&
        !Object.hasOwn(branches, Symbol.iterator) &&
        Object.values(Object.getOwnPropertyDescriptors(branches)).every(
          (descriptor) => 'value' in descriptor,
        ) &&
        Array.from(branches).every(
          (branch) =>
            plainDataObject(branch) &&
            (() => {
              const type = Object.getOwnPropertyDescriptor(branch, 'type')
              return type?.enumerable === true && 'value' in type && type.value === 'object'
            })(),
        )
      )
    })
  )
    return schema
  return { ...schema, type: 'object' }
}
