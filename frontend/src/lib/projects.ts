import type { app } from '../../wailsjs/go/models'

export interface ProjectGroup {
  project: app.ProjectDTO
  sessions: app.SessionInfoDTO[]
}

export interface SessionGroups {
  groups: ProjectGroup[]
  orphans: app.SessionInfoDTO[]
}

// normalizePath 只做尾部斜杠归一；后端已把项目与会话的工作目录都解析成真实绝对路径。
function normalizePath(path: string): string {
  if (path.length > 1 && path.endsWith('/')) return path.replace(/\/+$/, '')
  return path
}

// groupSessionsByProject 把会话归到已打开项目。
// 普通会话按 workDir 精确匹配；工作树会话由后端标注 projectPath，
// 因此会归到主项目而不是落进“其他会话”。
export function groupSessionsByProject(
  projects: app.ProjectDTO[],
  sessions: app.SessionInfoDTO[],
): SessionGroups {
  const byPath = new Map<string, app.SessionInfoDTO[]>()
  for (const project of projects) {
    byPath.set(normalizePath(project.path), [])
  }

  const orphans: app.SessionInfoDTO[] = []
  for (const session of sessions) {
    const bucket = byPath.get(normalizePath(session.projectPath || session.workDir))
    if (bucket) bucket.push(session)
    else orphans.push(session)
  }

  return {
    groups: projects.map((project) => ({
      project,
      sessions: byPath.get(normalizePath(project.path)) ?? [],
    })),
    orphans,
  }
}
