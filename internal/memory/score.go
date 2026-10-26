package memory

import (
	"context"
	"math"
	"sort"
	"time"
)

// 打分权重。三分量公式对齐 Generative Agents 的
// recency × importance × relevance 记忆检索（deps/pdfs/generative-agents）：
// 相关度决定候选，新近性指数衰减，重要性线性加分。
const (
	// weightField 是 name/description/keywords 命中的权重。
	weightField = 3.0
	// weightBody 是正文命中的权重。
	weightBody = 1.0
	// recencyHalfLifeDays 是新近性半衰期（天）。
	recencyHalfLifeDays = 14.0
	// maxRecency 是新近性满分。
	maxRecency = 3.0
	// weightImportance 是重要性的线性系数（importance 1-10 → 0.15-1.5）。
	weightImportance = 0.15
	// projectBoost 是项目级记忆在匹配本项目会话中的加成。
	projectBoost = 1.2
)

// Scored 是带召回得分的记忆。
type Scored struct {
	Memory Memory
	Score  float64
}

// Recall 按确定性打分召回与 prompt 相关的记忆，最多 limit 条。
// limit <= 0 时使用默认值 5；没有词元重合的条目不返回。
// boost 是整体得分乘数（Hub 合并用户级与项目级时区分作用域）。
// 命中条目的 lastAccessedAt/accessCount 在超过 touchInterval 后回写落盘。
func (s *Store) Recall(ctx context.Context, prompt string, limit int, boost float64) ([]Scored, error) {
	if limit <= 0 {
		limit = defaultRecallLimit
	}
	if boost <= 0 {
		boost = 1
	}
	memories, err := s.list(ctx, false)
	if err != nil {
		return nil, err
	}
	tokens := tokenize(prompt)
	if len(tokens) == 0 || len(memories) == 0 {
		return nil, nil
	}
	now := time.Now()
	ranked := make([]Scored, 0, len(memories))
	for _, mem := range memories {
		score, relevant := scoreMemory(mem, tokens, now)
		if !relevant {
			continue
		}
		ranked = append(ranked, Scored{Memory: mem, Score: score * boost})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score > ranked[j].Score
		}
		return ranked[i].Memory.Name < ranked[j].Memory.Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	s.touch(ranked, now)
	return ranked, nil
}

// scoreMemory 计算单条记忆的召回得分。relevant 表示存在词元重合。
func scoreMemory(mem Memory, tokens map[string]struct{}, now time.Time) (score float64, relevant bool) {
	field := tokenize(mem.Name + " " + mem.Description + " " + joinKeywords(mem.Keywords))
	body := tokenize(mem.Body)
	for token := range tokens {
		if _, ok := field[token]; ok {
			score += weightField
			relevant = true
			continue
		}
		if _, ok := body[token]; ok {
			score += weightBody
			relevant = true
		}
	}
	if !relevant {
		return 0, false
	}
	base := mem.LastAccessedAt
	if base.IsZero() {
		base = mem.UpdatedAt
	}
	if !base.IsZero() && now.After(base) {
		days := now.Sub(base).Hours() / 24
		score += maxRecency * math.Exp(-days/recencyHalfLifeDays*math.Ln2)
	}
	if mem.Importance > 0 {
		score += weightImportance * float64(mem.Importance)
	}
	return score, true
}

// touch 回写召回命中条目的访问元数据；每条距上次记录超过 touchInterval
// 才落盘一次，避免高频改写。
func (s *Store) touch(ranked []Scored, now time.Time) {
	for _, item := range ranked {
		mem := item.Memory
		if !mem.LastAccessedAt.IsZero() && now.Sub(mem.LastAccessedAt) < touchInterval {
			continue
		}
		mem.LastAccessedAt = now
		mem.AccessCount++
		// 不重建索引：访问元数据不影响索引行内容。
		if err := s.writeEntry(mem); err != nil {
			continue
		}
	}
}

// joinKeywords 把关键词列表拼成打分用的文本。
func joinKeywords(keywords []string) string {
	if len(keywords) == 0 {
		return ""
	}
	out := ""
	for i, keyword := range keywords {
		if i > 0 {
			out += " "
		}
		out += keyword
	}
	return out
}
