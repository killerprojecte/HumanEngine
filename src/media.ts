import sharp from 'sharp'

/** Validate bytes, not an untrusted suffix or Content-Type, and bound decoded pixels. */
export async function imageDataUrl(bytes: Buffer): Promise<string> {
  if (!bytes.length || bytes.length > 10 * 1024 * 1024) throw new Error('media_size_limit')
  try {
    const input = sharp(bytes, { pages: 1, limitInputPixels: 40_000_000, failOn: 'warning' })
    const metadata = await input.metadata()
    if (!['png', 'jpeg', 'gif', 'webp'].includes(metadata.format ?? ''))
      throw new Error('unsupported_image_format')
    const png = await input
      .resize({ width: 1536, height: 1536, fit: 'inside', withoutEnlargement: true })
      .png()
      .toBuffer()
    return `data:image/png;base64,${png.toString('base64')}`
  } catch {
    throw new Error('invalid_or_unsupported_image')
  }
}
