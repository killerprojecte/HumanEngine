// Generated from src/plugins/config.ts; SHA256 d08f0a1f16f35d168acfac2014908bb71d857def88aebd5225cb5387a42391d6
export function validateConfig(value, schema) {
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
        'group'
    ]);
    for (const key of Object.keys(schema))if (!allowed.has(key) && key !== '$schema') throw new Error('unsupported_schema_keyword');
    if (schema.enum && !schema.enum.includes(value)) throw new Error('invalid_enum');
    if (schema.type === 'object') {
        if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('expected_object');
        const object = value, properties = schema.properties ?? {};
        for (const key of schema.required ?? [])if (!(key in object)) throw new Error('required_property');
        for (const [key, entry] of Object.entries(object)){
            if (!properties[key]) {
                if (schema.additionalProperties !== true) throw new Error('unknown_property');
            } else validateConfig(entry, properties[key]);
        }
    } else if (schema.type === 'string') {
        if (typeof value !== 'string' || typeof schema.pattern === 'string' && !new RegExp(schema.pattern).test(value) || typeof schema.minLength === 'number' && value.length < schema.minLength || typeof schema.maxLength === 'number' && value.length > schema.maxLength) throw new Error('expected_string');
    } else if (schema.type === 'boolean') {
        if (typeof value !== 'boolean') throw new Error('expected_boolean');
    } else if (schema.type === 'integer' || schema.type === 'number') {
        if (typeof value !== 'number' || !Number.isFinite(value) || schema.type === 'integer' && !Number.isInteger(value) || typeof schema.minimum === 'number' && value < schema.minimum || typeof schema.maximum === 'number' && value > schema.maximum) throw new Error('expected_number');
    } else if (schema.type === 'array') {
        if (!Array.isArray(value)) throw new Error('expected_array');
        for (const entry of value)validateConfig(entry, schema.items);
    } else throw new Error('unsupported_schema_type');
}
export function redactConfig(value, schema) {
    if (schema.secret === true) return undefined;
    if (schema.type === 'object' && value && typeof value === 'object' && !Array.isArray(value)) {
        const properties = schema.properties ?? {}, result = {};
        for (const [key, entry] of Object.entries(value)){
            const next = properties[key] ? redactConfig(entry, properties[key]) : entry;
            if (next !== undefined) result[key] = next;
        }
        return result;
    }
    if (schema.type === 'array' && Array.isArray(value)) return value.map((entry)=>redactConfig(entry, schema.items ?? {}) ?? null);
    return value;
}
export function preserveSecrets(value, previous, schema) {
    if (schema.secret === true) return value == null ? previous : value;
    if (schema.type === 'object' && value && typeof value === 'object' && !Array.isArray(value)) {
        const result = {
            ...value
        }, old = previous && typeof previous === 'object' ? previous : {}, properties = schema.properties ?? {};
        for (const [key, field] of Object.entries(properties)){
            if (!(key in result) && !(key in old)) continue;
            const next = preserveSecrets(result[key], old[key], field);
            if (next !== undefined) result[key] = next;
        }
        return result;
    }
    if (schema.type === 'array' && Array.isArray(value)) return value.map((entry, index)=>preserveSecrets(entry, Array.isArray(previous) ? previous[index] : undefined, schema.items ?? {}));
    return value;
}
export function publicSchema(schema) {
    const result = structuredClone(schema);
    if ('default' in result) {
        const safe = redactConfig(result.default, result);
        if (safe === undefined) delete result.default;
        else result.default = safe;
    }
    if (result.properties && typeof result.properties === 'object') result.properties = Object.fromEntries(Object.entries(result.properties).map(([key, field])=>[
            key,
            publicSchema(field)
        ]));
    if (result.items && typeof result.items === 'object') result.items = publicSchema(result.items);
    return result;
}
