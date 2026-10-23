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

// groupSessionsByProject 把会话按 workDir 精确归属到已打开项目。
// 未命中任何项目的会话单独返回，避免它们从侧边栏消失。
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
    const bucket = byPath.get(normalizePath(session.workDir))
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
