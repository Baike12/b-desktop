import avatarUrl from '../assets/mascot-avatar.png'

// MascotAvatar 使用只保留头部的裁切素材，避免品牌位露出肩膀。
export default function MascotAvatar({ className = '' }: { className?: string }) {
  return (
    <span className={`inline-block shrink-0 ${className}`} aria-hidden="true">
      <img src={avatarUrl} alt="" draggable={false} className="h-full w-full select-none" />
    </span>
  )
}
