package context

import (
	stdctx "context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"anc-dsk/internal/tool"
)

// 系统提示的默认参数。
const (
	defaultMaxInstructionBytes = 32 * 1024
	defaultGitTimeout          = 2 * time.Second
	gitStatusLimit             = 2000
	// defaultConfigDirName 是用户级配置目录，用户级 AGENTS.md 位于其中。
	defaultConfigDirName = ".anc_dsk"
)

// 系统提示中各段的包裹标签，导出供调用方与测试断言。
const (
	SectionEnv     = "env"                  // SectionEnv 是环境信息段。
	SectionProject = "project-instructions" // SectionProject 是项目指令段。
	SectionGit     = "git"                  // SectionGit 是 git 状态段。
	SectionTools   = "tools"                // SectionTools 是工具清单段。
)

// identityText 是系统提示第一段：身份与行为约定（固定文案，中文）。
const identityText = "你是 anc-dsk 桌面工作台里的编码 Agent。你可以在工作目录内读写文件、执行命令并调用工具完成任务。\n" +
	"要求：先读再改；工具失败时阅读错误信息并调整；回答简洁，用中文。"

// Options 是 Builder 的构造参数。
type Options struct {
	WorkDir             string
	Model               string
	ContextWindow       int // 仅记录；窗口容量由 Budget 使用
	MaxOutputTokens     int
	MaxInstructionBytes int           // 默认 32KB
	GitTimeout          time.Duration // 默认 2s
	Now                 func() time.Time
	// ReadFile 读取指令文件；nil 时用 os.ReadFile。测试注入用。
	ReadFile func(path string) ([]byte, error)
	// RunGit 执行 git 查询；nil 时用 exec.CommandContext（受 GitTimeout 约束）。
	// 签名固定为 (ctx, dir, args...) → stdout。
	RunGit func(ctx stdctx.Context, dir string, args ...string) ([]byte, error)
	// SystemExtra 追加在系统提示末尾的会话级附加文本（对应 agent.Config.SystemExtra）。
	SystemExtra string
}

// Builder 组装系统提示。所有方法并发安全。
// 缓存分两层：
//   - 静态层（身份、环境、项目指令、工具清单、附加文本）：按
//     (cwd, 指令文件签名, 工具集, 日期, 附加文本) 缓存；
//   - 动态层（git 状态）：每次 System 重新获取，但同一 turn 内复用，
//     由 BeginTurn 开启新 turn。
type Builder struct {
	opts       Options
	customRead bool // ReadFile 由调用方注入：跳过 os.Stat，改用内容签名

	mu        sync.RWMutex
	static    string
	staticKey string
	instrSig  string // 指令文件签名：默认读盘时为 stat 签名，注入读函数时为内容哈希
	gitValid  bool
	gitText   string
}

// NewBuilder 创建 Builder。零值字段填入默认值。
func NewBuilder(opts Options) *Builder {
	customRead := opts.ReadFile != nil
	if opts.MaxInstructionBytes <= 0 {
		opts.MaxInstructionBytes = defaultMaxInstructionBytes
	}
	if opts.GitTimeout <= 0 {
		opts.GitTimeout = defaultGitTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ReadFile == nil {
		opts.ReadFile = os.ReadFile
	}
	if opts.RunGit == nil {
		opts.RunGit = runGitCommand
	}
	return &Builder{opts: opts, customRead: customRead}
}

// BeginTurn 标记新 turn 开始：只让动态层（git）失效，静态层保持不变。
func (b *Builder) BeginTurn() {
	b.mu.Lock()
	b.gitValid = false
	b.mu.Unlock()
}

// Invalidate 清空全部缓存；仅在切换工作目录、模型或检测到指令文件变化时调用。
func (b *Builder) Invalidate() {
	b.mu.Lock()
	b.static = ""
	b.staticKey = ""
	b.instrSig = ""
	b.gitValid = false
	b.gitText = ""
	b.mu.Unlock()
}

// System 返回完整系统提示，按设计文档的顺序拼接：
// 身份 → env → project-instructions → git → tools → SystemExtra。
// git 不可用或超时时省略该段，不返回错误。
func (b *Builder) System(ctx stdctx.Context, tools []tool.Spec) (string, error) {
	parts := []string{
		b.staticSection(),
		b.gitSection(ctx),
		toolsSection(tools),
	}
	if extra := strings.TrimSpace(b.opts.SystemExtra); extra != "" {
		parts = append(parts, extra)
	}
	return strings.Join(nonEmpty(parts), "\n\n"), nil
}

// staticSection 返回静态层文本（身份、环境、项目指令）；缓存命中时不重读指令文件。
func (b *Builder) staticSection() string {
	b.mu.RLock()
	cachedKey, cached, instrSig := b.staticKey, b.static, b.instrSig
	b.mu.RUnlock()

	sig := b.instructionSignature(instrSig)
	key := b.staticCacheKey(sig)
	if cached != "" && key == cachedKey {
		return cached
	}

	instructionText, contentSig := b.instructionSection()
	if b.customRead && contentSig != "" {
		sig = contentSig
		key = b.staticCacheKey(sig)
	}

	out := strings.Join(nonEmpty([]string{
		identityText,
		b.envSection(),
		instructionText,
	}), "\n\n")

	b.mu.Lock()
	b.static = out
	b.staticKey = key
	b.instrSig = sig
	b.mu.Unlock()
	return out
}

func (b *Builder) staticCacheKey(instrSig string) string {
	return strings.Join([]string{
		b.absWorkDir(),
		b.opts.Model,
		b.opts.Now().Format("2006-01-02"),
		instrSig,
		b.opts.SystemExtra,
	}, "\x00")
}

func (b *Builder) absWorkDir() string {
	dir := b.opts.WorkDir
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

func (b *Builder) envSection() string {
	var sb strings.Builder
	sb.WriteString("<" + SectionEnv + ">\n")
	sb.WriteString("cwd: " + b.absWorkDir() + "\n")
	sb.WriteString("os: " + runtime.GOOS + "/" + runtime.GOARCH + "\n")
	sb.WriteString("date: " + b.opts.Now().Format("2006-01-02") + "\n")
	if b.opts.Model != "" {
		sb.WriteString("model: " + b.opts.Model + "\n")
	}
	sb.WriteString("</" + SectionEnv + ">")
	return sb.String()
}

// toolsSection 生成工具清单：每行 "- Name: Description"；完整 schema 走 API 的 tools 字段。
func toolsSection(tools []tool.Spec) string {
	if len(tools) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<" + SectionTools + ">\n")
	for _, t := range tools {
		desc := strings.Join(strings.Fields(t.Description), " ")
		if desc == "" {
			sb.WriteString("- " + t.Name + "\n")
			continue
		}
		sb.WriteString("- " + t.Name + ": " + desc + "\n")
	}
	sb.WriteString("</" + SectionTools + ">")
	return sb.String()
}

// instructionFile 是已读取的指令文件。
type instructionFile struct {
	Path    string
	Content string
}

// instructionSection 生成 project-instructions 段：用户级 AGENTS.md 与沿途各级项目 AGENTS.md。
// 只有静态层缓存未命中时才会被调用。
func (b *Builder) instructionSection() (text, contentSig string) {
	files := b.loadInstructionFiles()
	if len(files) == 0 {
		return "", ""
	}
	var sb strings.Builder
	sb.WriteString("<" + SectionProject + ">\n")
	sb.WriteString("以下内容来自 AGENTS.md，按优先级从低到高排列：越靠后的文件离工作目录越近，优先级越高。\n")
	for _, f := range files {
		sb.WriteString("--- " + f.Path + " ---\n")
		content := truncateBytes(f.Content, b.opts.MaxInstructionBytes)
		sb.WriteString(strings.TrimRight(content, "\n"))
		sb.WriteString("\n")
	}
	sb.WriteString("</" + SectionProject + ">")

	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write([]byte(f.Content))
		h.Write([]byte{0})
	}
	return sb.String(), hex.EncodeToString(h.Sum(nil))
}

// instructionFileNames 是每个目录下依次检查的指令文件名。
// `.anc_dsk/AGENTS.md` 对应 cc 的 `.claude/CLAUDE.md`：项目内可提交的补充指令。
var instructionFileNames = []string{"AGENTS.md", filepath.Join(".anc_dsk", "AGENTS.md")}

// instructionSignature 返回指令文件签名。
// 默认读盘时用 (path, mtime, size) 组成签名，任一文件变化即触发静态层重建；
// 注入 ReadFile 时沿用上次构建写入的内容哈希，避免每次 System 都重新读取。
func (b *Builder) instructionSignature(cached string) string {
	if b.customRead {
		return cached
	}
	var sb strings.Builder
	for _, path := range b.instructionPaths() {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "%s|%d|%d;", path, info.ModTime().UnixNano(), info.Size())
	}
	return sb.String()
}

// loadInstructionFiles 读取用户级与项目级 AGENTS.md。
// 顺序与 cc 一致：用户级最先，随后从文件系统根目录（不含根）向下直到工作目录，
// 更近的文件后加载，优先级更高。
func (b *Builder) loadInstructionFiles() []instructionFile {
	var files []instructionFile
	for _, path := range b.instructionPaths() {
		data, err := b.opts.ReadFile(path)
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		files = append(files, instructionFile{Path: path, Content: string(data)})
	}
	return files
}

// instructionPaths 返回候选指令路径，已去重并保持加载顺序。
func (b *Builder) instructionPaths() []string {
	seen := make(map[string]struct{})
	paths := make([]string, 0, 8)
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	// 用户级：~/.anc_dsk/AGENTS.md（对应 cc 的 ~/.claude/CLAUDE.md）。
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, defaultConfigDirName, "AGENTS.md"))
	}

	// 项目级：根目录 → 工作目录。upwardDirs 由近及远，需反向遍历。
	dirs := b.upwardDirs()
	for i := len(dirs) - 1; i >= 0; i-- {
		for _, name := range instructionFileNames {
			add(filepath.Join(dirs[i], name))
		}
	}
	return paths
}

// upwardDirs 返回从工作目录到文件系统根目录（不含根）的目录列表。
func (b *Builder) upwardDirs() []string {
	dir := b.absWorkDir()
	parent := filepath.Dir(dir)
	var dirs []string
	for dir != parent {
		dirs = append(dirs, dir)
		dir = parent
		parent = filepath.Dir(dir)
	}
	if len(dirs) == 0 {
		dirs = append(dirs, dir)
	}
	return dirs
}

// gitSection 返回 git 段；git 不可用时返回空串。
// 同一 turn 内复用，BeginTurn 后重新获取。
func (b *Builder) gitSection(ctx stdctx.Context) string {
	b.mu.RLock()
	if b.gitValid {
		text := b.gitText
		b.mu.RUnlock()
		return text
	}
	b.mu.RUnlock()

	text := b.buildGitSection(ctx)

	b.mu.Lock()
	b.gitText = text
	b.gitValid = true
	b.mu.Unlock()
	return text
}

func (b *Builder) buildGitSection(ctx stdctx.Context) string {
	gitCtx, cancel := stdctx.WithTimeout(ctx, b.opts.GitTimeout)
	defer cancel()

	dir := b.absWorkDir()
	branch, err := b.runGit(gitCtx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<" + SectionGit + ">\n")
	sb.WriteString("branch: " + branch + "\n")

	if main, err := b.mainBranch(gitCtx, dir); err == nil && main != "" {
		sb.WriteString("main branch: " + main + "\n")
	}

	// status 的首列空格有意义（worktree 与 index 的差异），不能 TrimSpace。
	if status, err := b.runGitRaw(gitCtx, dir, "status", "--porcelain=v1"); err == nil {
		status = strings.TrimRight(status, "\n")
		if strings.TrimSpace(status) == "" {
			status = "(clean)"
		}
		sb.WriteString("status:\n" + truncateRunes(status, gitStatusLimit) + "\n")
	}

	if commits, err := b.runGit(gitCtx, dir, "log", "-5", "--pretty=format:%h %s"); err == nil && strings.TrimSpace(commits) != "" {
		sb.WriteString("recent commits:\n" + truncateRunes(commits, gitStatusLimit) + "\n")
	}
	sb.WriteString("</" + SectionGit + ">")
	return sb.String()
}

func (b *Builder) mainBranch(ctx stdctx.Context, dir string) (string, error) {
	out, err := b.runGit(ctx, dir, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(out, "origin/")
	if name == "" || name == "origin" || name == "HEAD" {
		return "", errors.New("context: main branch unknown")
	}
	return name, nil
}

// runGitRaw 返回 git 的原始输出（仅去掉结尾换行），用于 status 这类前列空白有意义的场景。
func (b *Builder) runGitRaw(ctx stdctx.Context, dir string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := b.opts.RunGit(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (b *Builder) runGit(ctx stdctx.Context, dir string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := b.opts.RunGit(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runGitCommand 是默认的 git 执行器。
func runGitCommand(ctx stdctx.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("context: git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// nonEmpty 过滤空字符串，保留顺序。
func nonEmpty(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

// truncateBytes 按字节上限截断，并保证不切断 UTF-8 字符。
func truncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.ToValidUTF8(s[:cut], "")
}

// truncateRunes 按字符数截断，超出时追加省略标记。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n...(truncated)"
}
