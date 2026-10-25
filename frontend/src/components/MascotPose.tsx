import type { CSSProperties } from 'react'
import bashUrl from '../assets/mascot-pose-bash.png'
import bashBlinkUrl from '../assets/mascot-pose-bash-blink.png'
import questionUrl from '../assets/mascot-pose-question.png'
import questionBlinkUrl from '../assets/mascot-pose-question-blink.png'
import readingUrl from '../assets/mascot-pose-reading.png'
import readingBlinkUrl from '../assets/mascot-pose-reading-blink.png'
import restingUrl from '../assets/mascot-pose-resting.png'
import thinkingUrl from '../assets/mascot-pose-thinking.png'
import thinkingBlinkUrl from '../assets/mascot-pose-thinking-blink.png'
import workingUrl from '../assets/mascot-pose-working.png'
import workingBlinkUrl from '../assets/mascot-pose-working-blink.png'
import writingUrl from '../assets/mascot-pose-writing.png'
import writingBlinkUrl from '../assets/mascot-pose-writing-blink.png'

export type MascotPoseName =
  | 'reading'
  | 'writing'
  | 'thinking'
  | 'question'
  | 'working'
  | 'resting'
  | 'bash'

type PoseArt = {
  open: string
  blink?: string
  delay: number
  aspect?: string
}

const POSES: Record<MascotPoseName, PoseArt> = {
  reading: { open: readingUrl, blink: readingBlinkUrl, delay: 0 },
  writing: { open: writingUrl, blink: writingBlinkUrl, delay: 1.1 },
  thinking: { open: thinkingUrl, blink: thinkingBlinkUrl, delay: 2.3 },
  question: { open: questionUrl, blink: questionBlinkUrl, delay: 0.6 },
  working: { open: workingUrl, blink: workingBlinkUrl, delay: 1.7 },
  bash: { open: bashUrl, blink: bashBlinkUrl, delay: 2.9 },
  // 躺平形象闭眼睡觉，用更慢的呼吸动画代替眨眼帧。
  resting: { open: restingUrl, delay: 0.4, aspect: '3 / 2' },
}

// 工具名 → 姿态：先排除任务/团队类工具，避免 TodoWrite、TaskList 被误判成写文件或读文件。
export function mascotPoseForTool(toolName: string): MascotPoseName {
  const name = toolName.toLowerCase()
  if (/(askuserquestion|question)/.test(name)) return 'question'
  if (/(bash|shell|terminal|powershell)/.test(name)) return 'bash'
  if (/(todo|task|agent|team)/.test(name)) return 'working'
  if (/(write|edit|notebookedit|multiedit|patch|replace|append|create)/.test(name)) return 'writing'
  if (/(read|glob|grep|search|fetch|list|view|inspect|tree)/.test(name)) return 'reading'
  return 'working'
}

// 全身姿态沿用欢迎页的分层骨骼思路：呼吸起伏 + 定时眨眼，眨眼帧按姿态错开相位。
export default function MascotPose({
  pose,
  className = '',
}: {
  pose: MascotPoseName
  className?: string
}) {
  const art = POSES[pose]
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
