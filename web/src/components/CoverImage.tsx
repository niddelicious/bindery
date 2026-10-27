import { useState, type ImgHTMLAttributes, type ReactNode } from 'react'

type CoverImageProps = Omit<ImgHTMLAttributes<HTMLImageElement>, 'src' | 'onError'> & {
  src?: string | null
  // What the slot shows without an image. Each list keeps its own
  // placeholder, so a cover that fails looks like a book with no cover.
  fallback?: ReactNode
}

// CoverImage is the <img> for a book or author picture. A cover that fails to
// load (an expired remote URL, a provider outage, a stored cover the server
// cannot find) shows the fallback instead of the browser's broken-image icon
// (#3146). The failure is remembered per URL, so a new src gets its own try.
export default function CoverImage({ src, fallback = null, ...img }: CoverImageProps) {
  const [failedSrc, setFailedSrc] = useState<string | null>(null)
  if (!src || failedSrc === src) return <>{fallback}</>
  return <img {...img} src={src} onError={() => setFailedSrc(src)} />
}
