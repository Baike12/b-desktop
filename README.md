# B Desktop

桌面 AI 工作台，基于 Go + Wails v2 + 系统 WebView，目标是低性能电脑上也能流畅运行。

- 技术栈：Go 1.26 · Wails v2 · React 19 · TypeScript · Vite · Tailwind CSS · Zustand
- 开发约定与架构说明见 [AGENTS.md](./AGENTS.md)

```bash
wails dev     # 开发模式
wails build   # 打包
go test ./...
```
