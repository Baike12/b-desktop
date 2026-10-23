package memory

import (
	"strings"
	"unicode"
)

// stopwords 是召回打分时忽略的高频词，避免「的」「了」这类词造成虚假重合。
var stopwords = map[string]struct{}{
	"the": {}, "and": {}, "for": {}, "with": {}, "this": {}, "that": {},
	"from": {}, "into": {}, "are": {}, "was": {}, "you": {}, "our": {},
	"的": {}, "了": {}, "是": {}, "我": {}, "你": {}, "他": {}, "她": {},
	"它": {}, "在": {}, "和": {}, "与": {}, "就": {}, "都": {}, "也": {},
	"很": {}, "把": {}, "被": {}, "给": {}, "对": {}, "让": {}, "这": {},
	"那": {}, "有": {}, "没": {}, "不": {}, "吗": {}, "呢": {},
}

// tokenize 把文本切成用于重合度打分的词元集合。
//
// 拉丁字母与数字按连续串切分（长度 >= 2）；中日韩文字没有空格，按单字
// 与相邻双字同时产出，双字承担主要信号，单字补偿短查询。
func tokenize(text string) map[string]struct{} {
	out := make(map[string]struct{})
	var word strings.Builder
	flush := func() {
		token := strings.ToLower(word.String())
		word.Reset()
		if len([]rune(token)) < 2 {
			return
		}
		if _, stop := stopwords[token]; stop {
			return
		}
		out[token] = struct{}{}
	}
	var previousCJK rune
	for _, r := range text {
		if isCJK(r) {
			flush()
			token := string(r)
			if _, stop := stopwords[token]; !stop {
				out[token] = struct{}{}
			}
			if previousCJK != 0 {
				bigram := string([]rune{previousCJK, r})
				if _, stop := stopwords[bigram]; !stop {
					out[bigram] = struct{}{}
				}
			}
			previousCJK = r
			continue
		}
		previousCJK = 0
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// isCJK 报告 r 是否属于中日韩表意文字或假名。
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
		return true
	case r >= 0x3400 && r <= 0x4DBF: // 扩展 A
		return true
	case r >= 0x3040 && r <= 0x30FF: // 平假名、片假名
		return true
	case r >= 0xAC00 && r <= 0xD7AF: // 韩文音节
		return true
	default:
		return false
	}
}
