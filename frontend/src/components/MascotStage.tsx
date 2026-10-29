import type { CSSProperties } from 'react'
import bodyUrl from '../assets/mascot-body.png'
import headUrl from '../assets/mascot-head.png'
import handUrl from '../assets/mascot-hand.png'
import cuffUrl from '../assets/mascot-cuff.png'
import lidLeftUrl from '../assets/mascot-lid-left.png'
import lidRightUrl from '../assets/mascot-lid-right.png'
import { characterName } from '../lib/character'
import { MASCOTS } from '../lib/mascots'
import { useSettingsStore } from '../stores/settings'
import AppMark from './AppMark'

type MascotStageProps = {
  className?: string
}

// 欢迎页吉祥物：噜噜沿用分层骨骼（手绕腕关节摆动），
// 奔奔拆成「补平手部的底图 + 手部图层」，复用同一套腕部挥手节奏。
// 默认主题显示原生应用图标。
export default function MascotStage({ className = '' }: MascotStageProps) {
  const character = useSettingsStore((state) => state.character)

  if (character === 'default') {
    return <AppMark className={`select-none ${className}`} />
  }

  if (character === 'benben') {
    const stage = MASCOTS.benben.stage
    if (stage) {
      return (
        <div
          className={`relative select-none ${className}`}
          role="img"
          aria-label={characterName(character)}
        >
          <div className="mascot-benben-puppet">
            <div className="mascot-benben-bob">
              <img
                src={stage.base}
                alt=""
                draggable={false}
                className="mascot-benben-layer mascot-benben-base"
              />
              <span
                className="mascot-benben-hand"
                style={
                  {
                    '--hand-x': `${stage.pivotX}%`,
                    '--hand-y': `${stage.pivotY}%`,
                  } as CSSProperties
                }
              >
                <img
                  src={stage.hand}
                  alt=""
                  draggable={false}
                  className="mascot-benben-layer"
                />
              </span>
            </div>
          </div>
        </div>
      )
    }
  }

  return (
    <div
      className={`relative select-none ${className}`}
      role="img"
      aria-label={characterName(character)}
    >
      <div className="mascot-puppet">
        <div className="mascot-frame">
          <img src={bodyUrl} alt="" draggable={false} className="mascot-layer" />
          <div className="mascot-head">
            <img src={headUrl} alt="" draggable={false} className="mascot-layer" />
            <img
              src={lidLeftUrl}
              alt=""
              draggable={false}
              className="mascot-layer mascot-lid mascot-lid-left"
            />
            <img
              src={lidRightUrl}
              alt=""
              draggable={false}
              className="mascot-layer mascot-lid mascot-lid-right"
            />
            <img src={cuffUrl} alt="" draggable={false} className="mascot-layer" />
            <div className="mascot-hand">
              <img src={handUrl} alt="" draggable={false} className="mascot-layer" />
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
