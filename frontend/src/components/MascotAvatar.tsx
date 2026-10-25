import { characterName } from '../lib/character'
import { MASCOTS } from '../lib/mascots'
import { useSettingsStore } from '../stores/settings'

// MascotAvatar 使用只保留头部的裁切素材，避免品牌位露出肩膀。
export default function MascotAvatar({ className = '' }: { className?: string }) {
  const character = useSettingsStore((state) => state.character)
  return (
    <span className={`inline-block shrink-0 ${className}`} aria-hidden="true">
      <img
        src={MASCOTS[character].avatar}
        alt=""
        title={characterName(character)}
        draggable={false}
        className="h-full w-full select-none"
      />
    </span>
  )
}
