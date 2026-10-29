import { useId } from 'react'

// AppMark 是默认主题的原生图标：蓝底圆角方形 + 白色「思考火花」。
// 与 build/appicon.svg 同源，纯几何图形，不绑定吉祥物角色；
// 渐变 id 用 useId 隔离，允许同页多实例。
export default function AppMark({ className = '' }: { className?: string }) {
  const gradientId = useId()
  return (
    <svg viewBox="0 0 512 512" className={className} role="img" aria-label="B Desktop">
      <defs>
        <linearGradient id={`${gradientId}-bg`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor="#2f8ceb" />
          <stop offset="1" stopColor="#0b5fc0" />
        </linearGradient>
      </defs>
      <rect x="24" y="24" width="464" height="464" rx="116" fill={`url(#${gradientId}-bg)`} />
      <path
        d="M232 96c14 78 44 108 122 122-78 14-108 44-122 122-14-78-44-108-122-122 78-14 108-44 122-122z"
        fill="#ffffff"
      />
      <path
        d="M356 300c8 44 24 60 68 68-44 8-60 24-68 68-8-44-24-60-68-68 44-8 60-24 68-68z"
        fill="#ffffff"
        fillOpacity="0.82"
      />
    </svg>
  )
}
