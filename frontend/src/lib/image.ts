// 图片附件的前端预处理：缩放、重编码与 base64 转换。
// 后端会再做 magic bytes 校验与 5MB base64 硬限制（internal/llm/image.go），
// 这里在 WebView 侧提前压到限额以内，避免大图白白穿过 Wails 桥。

// 长边上限与后端 Read 工具一致（供应商建议的 1568px）。
const MAX_EDGE = 1568
// 原始字节预算：base64 后约 4MB，低于 5MB 的 API 硬限制留余量。
const MAX_RAW_BYTES = 3 << 20
// 体积超预算时的逐轮缩小系数与最大轮数（与 zed 一致）。
const SCALE_STEP = 0.85
const MAX_PASSES = 8

// PreparedImage 是一张可直接随消息发送的图片。
export interface PreparedImage {
  mediaType: string
  data: string
  name: string
}

// toDataURL 把 base64 拼成可直接给 <img> 的 URL。
export function imageDataUrl(mediaType: string, data: string): string {
  return `data:${mediaType || 'image/png'};base64,${data}`
}

// blobToBase64 把 Blob 转成不带 data URL 前缀的裸 base64。
function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('读取图片失败'))
    reader.onload = () => {
      const result = String(reader.result ?? '')
      const comma = result.indexOf(',')
      resolve(comma >= 0 ? result.slice(comma + 1) : result)
    }
    reader.readAsDataURL(blob)
  })
}

// canvasToBlob 兼容 toBlob 与旧 WebView 的 toDataURL。
function canvasToBlob(canvas: HTMLCanvasElement, mediaType: string, quality?: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    if (canvas.toBlob) {
      canvas.toBlob(
        (blob) => (blob ? resolve(blob) : reject(new Error('图片编码失败'))),
        mediaType,
        quality,
      )
      return
    }
    try {
      const url = canvas.toDataURL(mediaType, quality)
      void fetch(url)
        .then((res) => res.blob())
        .then(resolve)
        .catch(() => reject(new Error('图片编码失败')))
    } catch {
      reject(new Error('图片编码失败'))
    }
  })
}

// loadBitmap 解码图片；createImageBitmap 不可用时回退 <img>。
async function loadBitmap(file: Blob): Promise<ImageBitmap | HTMLImageElement> {
  if (typeof createImageBitmap === 'function') {
    return createImageBitmap(file)
  }
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file)
    const img = new Image()
    img.onload = () => {
      URL.revokeObjectURL(url)
      resolve(img)
    }
    img.onerror = () => {
      URL.revokeObjectURL(url)
      reject(new Error('图片解码失败'))
    }
    img.src = url
  })
}

function bitmapSize(bmp: ImageBitmap | HTMLImageElement): { width: number; height: number } {
  return { width: bmp.width, height: bmp.height }
}

// drawScaled 把位图画到按 scale 缩放的画布上。
function drawScaled(bmp: ImageBitmap | HTMLImageElement, scale: number): HTMLCanvasElement {
  const { width, height } = bitmapSize(bmp)
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(width * scale))
  canvas.height = Math.max(1, Math.round(height * scale))
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('无法创建画布')
  ctx.drawImage(bmp, 0, 0, canvas.width, canvas.height)
  return canvas
}

// prepareImageFile 把用户提供的图片压到后端限额以内。
// 尺寸与体积都达标时原样透传（保留 png/jpeg/gif/webp 原格式）；
// 否则解码缩放到 1568px 内重编码，仍超限按 0.85 逐轮缩小。
export async function prepareImageFile(file: Blob, name = ''): Promise<PreparedImage> {
  if (file.size > 0 && file.size <= MAX_RAW_BYTES) {
    // 未缩放前先用 <img>/bitmap 验证可解码，避免把坏图发给后端。
    const bmp = await loadBitmap(file)
    const { width, height } = bitmapSize(bmp)
    if (Math.max(width, height) <= MAX_EDGE) {
      const mediaType = file.type && file.type.startsWith('image/') ? file.type : 'image/png'
      return { mediaType, data: await blobToBase64(file), name }
    }
  }

  const bmp = await loadBitmap(file)
  const { width, height } = bitmapSize(bmp)
  let scale = Math.min(1, MAX_EDGE / Math.max(width, height))
  const jpegSource = (file.type === 'image/jpeg' || file.type === 'image/jpg') && scale < 1

  for (let pass = 0; ; pass++) {
    const canvas = drawScaled(bmp, scale)
    const mediaType = jpegSource ? 'image/jpeg' : 'image/png'
    const blob = await canvasToBlob(canvas, mediaType, jpegSource ? 0.85 : undefined)
    if (blob.size <= MAX_RAW_BYTES || pass === MAX_PASSES - 1) {
      return { mediaType, data: await blobToBase64(blob), name }
    }
    scale *= SCALE_STEP
  }
}
