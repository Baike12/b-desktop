package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/acp"
)

// 权限选项 ID，与 b-desktop 审批弹窗的四个标准选项对应。
const (
	permOptionAllowOnce   = "allow-once"
	permOptionAllowAlways = "allow-with-updates"
	permOptionReject      = "reject"
)

// 权限建议的目标位置：allow_always 的规则写入本地项目设置
// （.claude/settings.local.json），与 claude CLI 的临时授权语义一致。
const permDestinationLocal = "localSettings"

// canUseToolRequests 报告 CLI 的 permission_suggestions 是否包含可持久化的
// allow 规则；没有建议时 allow_always 与 allow_once 等价。
func allowSuggestions(request canUseToolRequest) []permissionUpdate {
	if len(request.PermissionSuggestions) == 0 {
		return nil
	}
	var suggestions []permissionUpdate
	if err := json.Unmarshal(request.PermissionSuggestions, &suggestions); err != nil {
		return nil
	}
	out := make([]permissionUpdate, 0, len(suggestions))
	for _, suggestion := range suggestions {
		switch suggestion.Type {
		case "addRules", "replaceRules", "setMode":
			if suggestion.Behavior == "deny" {
				continue
			}
			out = append(out, suggestion)
		}
	}
	return out
}

// permissionOptionsFor 构造呈现给用户的选项，对齐适配器的
// buildClaudePermissionOptions：总是提供「允许一次 / 总是允许 / 拒绝」；
// 没有 allow 建议时「总是允许」仍然出现（由本侧合成规则）。
func permissionOptionsFor(request canUseToolRequest) []acp.PermissionOption {
	options := []acp.PermissionOption{
		{OptionID: permOptionAllowOnce, Name: "允许一次", Kind: acp.AllowOnce},
		{OptionID: permOptionAllowAlways, Name: "总是允许", Kind: acp.AllowAlways},
		{OptionID: permOptionReject, Name: "拒绝", Kind: acp.RejectOnce},
	}
	if request.DecisionReason != "" && strings.Contains(strings.ToLower(request.DecisionReason), "default to no") {
		// CLI 标记为默认拒绝的请求把「拒绝」排在最前。
		return []acp.PermissionOption{options[2], options[0], options[1]}
	}
	return options
}

// denyAlwaysRules 为 deny_always 合成一条工具级 deny 规则。
func denyAlwaysRules(request canUseToolRequest) []permissionUpdate {
	if request.ToolName == "" {
		return nil
	}
	return []permissionUpdate{{
		Type:        "addRules",
		Destination: permDestinationLocal,
		Behavior:    "deny",
		Rules:       []permissionRule{{ToolName: request.ToolName}},
	}}
}

// decisionFor 把宿主的审批选项翻译成 can_use_tool 应答。
// 兼容 b-desktop 审批弹窗的四个标准选项（allow_once 等）与 ACP 原生 optionId。
func decisionFor(request canUseToolRequest, optionID string) (canUseToolDecision, error) {
	switch optionID {
	case permOptionAllowOnce, "allow_once":
		return canUseToolDecision{Behavior: "allow", UpdatedInput: request.Input}, nil
	case permOptionAllowAlways, "allow_always":
		updates := allowSuggestions(request)
		if len(updates) == 0 {
			// CLI 没给建议：退化为对整个工具的 allow 规则（写入本地设置）。
			updates = []permissionUpdate{{
				Type:        "addRules",
				Destination: permDestinationLocal,
				Behavior:    "allow",
				Rules:       []permissionRule{{ToolName: request.ToolName}},
			}}
		}
		return canUseToolDecision{Behavior: "allow", UpdatedInput: request.Input, UpdatedPermissions: updates}, nil
	case permOptionReject, "deny_once":
		return canUseToolDecision{Behavior: "deny", Message: "用户拒绝了本次工具调用"}, nil
	case "deny_always":
		return canUseToolDecision{Behavior: "deny", Message: "用户拒绝了本次工具调用", UpdatedPermissions: denyAlwaysRules(request)}, nil
	default:
		return canUseToolDecision{}, fmt.Errorf("claude: 未知审批选项 %q", optionID)
	}
}
