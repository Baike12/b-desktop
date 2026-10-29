// Package claude 实现内置的 Claude Code ACP 后端：直接 spawn claude CLI 并
// 以它的 stream-json 双向协议通信，行为对齐 zed 的 claude-agent-acp 适配器
// （该适配器通过 Node 版 SDK 驱动同一协议），无需独立插件进程。
//
// 协议参考：
//   - claude-agent-sdk-python 的 subprocess_cli.py / query.py（spawn 参数、
//     control_request/control_response 帧、can_use_tool 应答）；
//   - claude-agent-acp 的 acp-agent.ts（SDK Options、权限选项、工具标题映射、
//     斜杠命令过滤）。
package claude

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BackendID 是 claude 后端在注册表中的 ID。
const BackendID = "claude"

// 环境变量与常见安装路径。
const (
	// EnvExecutable 优先级最高，与 zed 适配器和官方 SDK 的约定一致。
	EnvExecutable = "CLAUDE_CODE_EXECUTABLE"
	// EnvBDesktopBin 是 b-desktop 自己的覆盖入口。
	EnvBDesktopBin = "BDESKTOP_CLAUDE_BIN"
)

// ResolveCLI 定位 claude CLI 可执行文件：
// CLAUDE_CODE_EXECUTABLE → BDESKTOP_CLAUDE_BIN → PATH → 常见安装路径。
// 全部失败返回错误，错误消息说明已尝试的位置。
func ResolveCLI(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	for _, key := range []string{EnvExecutable, EnvBDesktopBin} {
		if path := strings.TrimSpace(getenv(key)); path != "" {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, nil
			}
			return "", fmt.Errorf("claude: %s=%s 不存在", key, path)
		}
	}
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, candidate := range []string{
			filepath.Join(home, ".local", "bin", "claude"),
			filepath.Join(home, ".claude", "local", "claude"),
			"/usr/local/bin/claude",
			"/opt/homebrew/bin/claude",
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", errors.New("claude: 未找到 claude CLI；请安装 Claude Code 或设置 CLAUDE_CODE_EXECUTABLE")
}
