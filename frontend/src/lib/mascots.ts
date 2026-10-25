import luluAvatarUrl from '../assets/mascot-avatar.png'
import luluBashUrl from '../assets/mascot-pose-bash.png'
import luluBashBlinkUrl from '../assets/mascot-pose-bash-blink.png'
import luluQuestionUrl from '../assets/mascot-pose-question.png'
import luluQuestionBlinkUrl from '../assets/mascot-pose-question-blink.png'
import luluReadingUrl from '../assets/mascot-pose-reading.png'
import luluReadingBlinkUrl from '../assets/mascot-pose-reading-blink.png'
import luluRestingUrl from '../assets/mascot-pose-resting.png'
import luluThinkingUrl from '../assets/mascot-pose-thinking.png'
import luluThinkingBlinkUrl from '../assets/mascot-pose-thinking-blink.png'
import luluWorkingUrl from '../assets/mascot-pose-working.png'
import luluWorkingBlinkUrl from '../assets/mascot-pose-working-blink.png'
import luluWritingUrl from '../assets/mascot-pose-writing.png'
import luluWritingBlinkUrl from '../assets/mascot-pose-writing-blink.png'
import benbenAvatarUrl from '../assets/benben/avatar.png'
import benbenBashUrl from '../assets/benben/pose-bash.png'
import benbenBashBlinkUrl from '../assets/benben/pose-bash-blink.png'
import benbenQuestionUrl from '../assets/benben/pose-question.png'
import benbenQuestionBlinkUrl from '../assets/benben/pose-question-blink.png'
import benbenReadingUrl from '../assets/benben/pose-reading.png'
import benbenReadingBlinkUrl from '../assets/benben/pose-reading-blink.png'
import benbenRestingUrl from '../assets/benben/pose-resting.png'
import benbenStageBaseUrl from '../assets/benben/stage-base.png'
import benbenStageHandUrl from '../assets/benben/stage-hand.png'
import benbenThinkingUrl from '../assets/benben/pose-thinking.png'
import benbenThinkingBlinkUrl from '../assets/benben/pose-thinking-blink.png'
import benbenWorkingUrl from '../assets/benben/pose-working.png'
import benbenWorkingBlinkUrl from '../assets/benben/pose-working-blink.png'
import benbenWritingUrl from '../assets/benben/pose-writing.png'
import benbenWritingBlinkUrl from '../assets/benben/pose-writing-blink.png'
import type { CharacterId } from './character'

export type MascotPoseName =
  | 'reading'
  | 'writing'
  | 'thinking'
  | 'question'
  | 'working'
  | 'resting'
  | 'bash'

type MascotPoseArt = {
  open: string
  blink?: string
  delay: number
  aspect?: string
}

type MascotStageArt = {
  base: string
  hand: string
  pivotX: number
  pivotY: number
}

type MascotArt = {
  avatar: string
  stage?: MascotStageArt
  poses: Record<MascotPoseName, MascotPoseArt>
}

export const MASCOTS: Record<CharacterId, MascotArt> = {
  lulu: {
    avatar: luluAvatarUrl,
    poses: {
      reading: { open: luluReadingUrl, blink: luluReadingBlinkUrl, delay: 0 },
      writing: { open: luluWritingUrl, blink: luluWritingBlinkUrl, delay: 1.1 },
      thinking: { open: luluThinkingUrl, blink: luluThinkingBlinkUrl, delay: 2.3 },
      question: { open: luluQuestionUrl, blink: luluQuestionBlinkUrl, delay: 0.6 },
      working: { open: luluWorkingUrl, blink: luluWorkingBlinkUrl, delay: 1.7 },
      bash: { open: luluBashUrl, blink: luluBashBlinkUrl, delay: 2.9 },
      // 躺平形象闭眼睡觉，用更慢的呼吸动画代替眨眼帧。
      resting: { open: luluRestingUrl, delay: 0.4, aspect: '3 / 2' },
    },
  },
  benben: {
    avatar: benbenAvatarUrl,
    // 底图已把手部补平，手部图层绕腕部圆点旋转，和噜噜共用挥手节奏。
    stage: {
      base: benbenStageBaseUrl,
      hand: benbenStageHandUrl,
      pivotX: 75.83,
      pivotY: 59.09,
    },
    poses: {
      reading: { open: benbenReadingUrl, blink: benbenReadingBlinkUrl, delay: 0 },
      writing: { open: benbenWritingUrl, blink: benbenWritingBlinkUrl, delay: 1.1 },
      thinking: { open: benbenThinkingUrl, blink: benbenThinkingBlinkUrl, delay: 2.3 },
      question: { open: benbenQuestionUrl, blink: benbenQuestionBlinkUrl, delay: 0.6 },
      working: { open: benbenWorkingUrl, blink: benbenWorkingBlinkUrl, delay: 1.7 },
      bash: { open: benbenBashUrl, blink: benbenBashBlinkUrl, delay: 2.9 },
      resting: { open: benbenRestingUrl, delay: 0.4, aspect: '3 / 2' },
    },
  },
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
