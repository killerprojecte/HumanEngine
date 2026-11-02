import { compressImage, DEFAULT_IMAGE_UPLOAD_LIMITS } from './image-upload.ts'

/** Vision deliberately sees only the first frame; callers disclose this in the vision prompt. */
export async function imageDataUrl(bytes: Buffer, signal?: AbortSignal): Promise<string> {
  return compressImage(bytes, DEFAULT_IMAGE_UPLOAD_LIMITS, true, signal)
}
