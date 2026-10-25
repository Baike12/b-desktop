import type { CSSProperties } from 'react'
import { MASCOTS, type MascotPoseName } from '../lib/mascots'
import { useSettingsStore } from '../stores/settings'

// 全身姿态沿用欢迎页的分层骨骼思路：呼吸起伏 + 定时眨眼，眨眼帧按姿态错开相位。
export default function MascotPose({
  pose,
  className = '',
}: {
  pose: MascotPoseName
  className?: string
}) {
  const character = useSettingsStore((state) => state.character)
  const art = MASCOTS[character].poses[pose]
  return (
    <span
      className={`mascot-pose ${className}`}
      style={
        {
          aspectRatio: art.aspect,
          '--mascot-delay': `${art.delay}s`,
        } as CSSProperties
      }
      data-mascot-pose={pose}
      data-mascot-character={character}
      aria-hidden="true"
    >
      <span className="mascot-pose-bob">
        <img src={art.open} alt="" draggable={false} className="mascot-pose-frame" />
        {art.blink && (
          <img
            src={art.blink}
            alt=""
            draggable={false}
            className="mascot-pose-frame mascot-pose-blink"
          />
        )}
      </span>
    </span>
  )
}
