import bodyUrl from '../assets/mascot-body.png'
import headUrl from '../assets/mascot-head.png'
import handUrl from '../assets/mascot-hand.png'
import wristUrl from '../assets/mascot-wrist.png'
import lidLeftUrl from '../assets/mascot-lid-left.png'
import lidRightUrl from '../assets/mascot-lid-right.png'

type MascotStageProps = {
  className?: string
}

// 定稿图分层骨骼动画：手绕腕关节摆动、眼睛定时眨、头部轻微点头，外观与定稿一致。
export default function MascotStage({ className = '' }: MascotStageProps) {
  return (
    <div className={`relative select-none ${className}`} role="img" aria-label="水豚噜噜">
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
          </div>
          <img src={wristUrl} alt="" draggable={false} className="mascot-layer" />
          <img src={handUrl} alt="" draggable={false} className="mascot-layer mascot-hand" />
        </div>
      </div>
    </div>
  )
}
