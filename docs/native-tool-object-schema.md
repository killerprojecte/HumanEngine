# Native tool object schemas

The native OpenAI Chat and Responses serializers add an explicit root
`type: "object"` to a tool parameter schema only when it has no own `type`
property and every alternative in each present, nonempty root `oneOf` or
`anyOf` explicitly has its own enumerable data property `type: "object"`.
The inspected nodes must be plain data objects; accessors, custom serialization
and unknown prototypes are not used as proof.

This is a redundant constraint, not a schema simplification. The alternatives,
required fields, bounds, descriptions and additional-property rules remain
unchanged. The input schema is not mutated. Existing root types, mixed or
unknown alternatives, references and empty unions are left alone. Nested
schemas and custom provider protocols are not rewritten. Runtime argument
validation remains authoritative and unchanged.

## Evidence and limits

A Gitee native learning request returned HTTP 400 with a diagnostic requiring
tool parameters to be an object JSON Schema. The learning tool's root already
had that type; the habit tool used an object-only root union without an
explicit root type. Offline reconstruction of the same Engine tool schemas
matched a separately successful schema probe byte for byte. Thus the observed
failure does not prove a universal Gitee rejection of unions or of
`strict: false`: both minimal strict variants and a separate complete-schema
probe were accepted.

The serializer change makes the object contract explicit without discarding
constraints. Offline native-wire and Engine tests verify this behavior; a
successful real-provider end-to-end run must be recorded separately. This does
not claim broad provider compatibility or semantic quality.
