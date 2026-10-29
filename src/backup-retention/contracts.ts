import { z } from 'zod'
const hash = z.string().regex(/^[a-f0-9]{64}$/)
export const BackupManifestSchema = z
  .object({
    version: z.literal(1),
    createdAt: z.iso.datetime({ offset: true }),
    node: z.string().min(1).max(80),
    files: z
      .array(
        z
          .object({
            path: z
              .string()
              .regex(
                /^(humanengine\.sqlite|\.secrets\.key|media\/[a-f0-9]{64}\.(jpeg|png|gif|webp))$/,
              ),
            sha256: hash,
            bytes: z.number().int().nonnegative().safe(),
          })
          .strict(),
      )
      .min(1)
      .max(100000),
  })
  .strict()
  .superRefine((m, ctx) => {
    if (
      new Set(m.files.map((f) => f.path)).size !== m.files.length ||
      !m.files.some((f) => f.path === 'humanengine.sqlite')
    )
      ctx.addIssue({ code: 'custom', message: 'retention_manifest_invalid' })
  })
export const FileIdentitySchema = z
  .object({
    path: z
      .string()
      .regex(
        /^(manifest\.json|humanengine\.sqlite|\.secrets\.key|media\/[a-f0-9]{64}\.(jpeg|png|gif|webp))$/,
      ),
    sha256: hash,
    bytes: z.number().int().nonnegative().safe(),
    device: z.string(),
    inode: z.string(),
  })
  .strict()
export const RegistrationSchema = z
  .object({
    id: z.string().uuid(),
    name: z.string().regex(/^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$/),
    createdAt: z.iso.datetime({ offset: true }),
    expiresAt: z.iso.datetime({ offset: true }),
    manifestHash: hash,
    directory: z.object({ device: z.string(), inode: z.string() }).strict(),
    files: z.array(FileIdentitySchema).min(2).max(100001),
    state: z.enum(['retained', 'deletion_pending', 'verified_absent', 'unverifiable']),
    error: z.enum(['identity_changed', 'unsafe_entry', 'removal_incomplete']).nullable(),
    verifiedAt: z.iso.datetime({ offset: true }).nullable(),
  })
  .strict()
export type Registration = z.infer<typeof RegistrationSchema>
export const RetentionInventorySchema = z
  .object({
    version: z.literal(1),
    rootId: z.string().uuid(),
    coverage: z.literal('registered_only'),
    dryRun: z.boolean(),
    items: z
      .array(
        z
          .object({
            id: z.string().uuid(),
            name: z.string(),
            createdAt: z.string(),
            expiresAt: z.string(),
            expired: z.boolean(),
            state: RegistrationSchema.shape.state,
            error: RegistrationSchema.shape.error,
            verifiedAt: z.string().nullable(),
          })
          .strict(),
      )
      .max(10000),
  })
  .strict()
export type RetentionInventory = z.infer<typeof RetentionInventorySchema>
