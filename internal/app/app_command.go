package app

import (
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/command"
)

// commandsComponent 返回某个工作目录对应的命令加载器，并按工作目录缓存。
// workDir 为空时使用应用默认工作目录。
func (a *App) commandsComponent(workDir string) (*command.Loader, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = a.cfg.WorkDir
	}
	value, err := a.extensionStore("commands:"+workDir, func() (any, error) {
		return command.NewLoader(workDir)
	})
	if err != nil {
		return nil, fmt.Errorf("app: commands unavailable: %w", err)
	}
	loader, ok := value.(*command.Loader)
	if !ok {
		return nil, fmt.Errorf("app: commands component has unexpected type %T", value)
	}
	return loader, nil
}
