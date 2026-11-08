# Experience view projection

`Project` and `Verify` implement the pure source experience projection and
included-reference verification. Ordered jsjson objects retain source field
order and absent/null distinctions. Output is recursively cloned, not aliased
with supplied snapshots. Caller-owned output may be mutated; verify rejects
changed included bodies against one fresh owner-controlled resolution batch.

Source policies remain 40 supplied candidates and 12 returned entries. Filters,
duplicates, coverage, and assertion-before-plan ordering match the source.
Verification compares keyed complete items independently of page ordering and
coverage metadata. Empty pages require no resolution.

These functions do not authorize snapshots, synthesize episodes, establish
observed events, register exposure, or invoke models. Workspace composition,
identity capture and runtime delivery remain separate owners.
