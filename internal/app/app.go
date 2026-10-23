// Package app 承载 Wails 绑定给前端的应用服务。
// 只有这个包中的导出方法会生成 TypeScript 绑定，前端无法直接访问内部实现。
package app

import (
	"context"
	"fmt"
)

// App 是 Wails 的主绑定对象。
// 这里只保存应用生命周期 context，请求级 context 一律通过方法参数传入。
type App struct {
	ctx context.Context
}

// New 创建应用实例。
func New() *App {
	return &App{}
}

// Startup 在窗口加载前由 Wails 调用，保存生命周期 context。
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}

// Shutdown 在应用退出时由 Wails 调用。
func (a *App) Shutdown(context.Context) {}

// Greet 是脚手架示例方法，用于验证前后端绑定是否打通。
func (a *App) Greet(name string) string {
	return fmt.Sprintf("Hello %s, ANC DSK is running.", name)
}
