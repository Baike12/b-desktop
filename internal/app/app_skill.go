package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Baike12/b-desktop/internal/skill"
)

// 技能绑定层的错误哨兵。
var (
	// ErrSkillNotFound 表示指定名称的技能不存在。
	ErrSkillNotFound = errors.New("app: skill not found")
	// ErrSkillsUnavailable 表示技能组件无法初始化。
	ErrSkillsUnavailable = errors.New("app: skills unavailable")
)

// SkillDTO 是技能清单项，不含正文。
type SkillDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

// SkillDetailDTO 是技能详情，Content 是去掉 frontmatter 的正文。
type SkillDetailDTO struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Path        string `json:"path"`
	Content     string `json:"content"`
}

// skillsComponent 返回某个工作目录对应的技能加载器，并按工作目录缓存。
// workDir 为空时使用应用默认工作目录。
func (a *App) skillsComponent(workDir string) (*skill.Loader, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = a.cfg.WorkDir
	}
	key := "skills:" + workDir
	value, err := a.extensionStore(key, func() (any, error) {
		return skill.NewLoader(workDir)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSkillsUnavailable, err)
	}
	loader, ok := value.(*skill.Loader)
	if !ok {
		return nil, fmt.Errorf("%w: unexpected component type %T", ErrSkillsUnavailable, value)
	}
	return loader, nil
}

// ListSkills 返回当前工作目录可用的技能清单（用户级 + 项目级）。
func (a *App) ListSkills() ([]SkillDTO, error) {
	loader, err := a.skillsComponent(a.cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	skills := loader.List()
	out := make([]SkillDTO, 0, len(skills))
	for _, s := range skills {
		out = append(out, SkillDTO{Name: s.Name, Description: s.Description, Source: s.Source})
	}
	return out, nil
}

// ReloadSkills 重新扫描技能目录，返回新的清单。
func (a *App) ReloadSkills() ([]SkillDTO, error) {
	loader, err := a.skillsComponent(a.cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	if err := loader.Reload(); err != nil {
		return nil, fmt.Errorf("app: reload skills: %w", err)
	}
	return a.ListSkills()
}

// GetSkillDetail 返回指定技能的详情与正文。
func (a *App) GetSkillDetail(name string) (*SkillDetailDTO, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("%w: empty name", ErrSkillNotFound)
	}
	loader, err := a.skillsComponent(a.cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	s, ok := loader.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSkillNotFound, name)
	}
	content, err := loader.Body(context.Background(), name)
	if err != nil {
		return nil, fmt.Errorf("app: read skill %q: %w", name, err)
	}
	return &SkillDetailDTO{
		Name:        s.Name,
		Description: s.Description,
		Source:      s.Source,
		Path:        s.Path,
		Content:     content,
	}, nil
}

// SkillInput 是新建或修改技能的入参。名称只用于新建与定位，修改时保持不变。
type SkillInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

// CreateSkill 在用户级技能目录新建技能，并返回可展示的详情。
func (a *App) CreateSkill(in SkillInput) (*SkillDetailDTO, error) {
	loader, err := a.skillsComponent(a.cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	created, err := loader.Create(name, in.Description, in.Content)
	if err != nil {
		return nil, fmt.Errorf("app: create skill %q: %w", name, err)
	}
	return a.skillDetail(loader, created.Name)
}

// UpdateSkill 覆盖已有技能的描述与正文。
func (a *App) UpdateSkill(in SkillInput) (*SkillDetailDTO, error) {
	loader, err := a.skillsComponent(a.cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if err := loader.Update(name, in.Description, in.Content); err != nil {
		return nil, fmt.Errorf("app: update skill %q: %w", name, err)
	}
	return a.skillDetail(loader, name)
}

// DeleteSkill 删除技能目录。
func (a *App) DeleteSkill(name string) error {
	loader, err := a.skillsComponent(a.cfg.WorkDir)
	if err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if err := loader.Delete(name); err != nil {
		return fmt.Errorf("app: delete skill %q: %w", name, err)
	}
	return nil
}

// skillDetail 读取技能详情，供写操作返回最新内容。
func (a *App) skillDetail(loader *skill.Loader, name string) (*SkillDetailDTO, error) {
	s, ok := loader.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrSkillNotFound, name)
	}
	content, err := loader.Body(context.Background(), name)
	if err != nil {
		return nil, fmt.Errorf("app: read skill %q: %w", name, err)
	}
	return &SkillDetailDTO{
		Name:        s.Name,
		Description: s.Description,
		Source:      s.Source,
		Path:        s.Path,
		Content:     content,
	}, nil
}
