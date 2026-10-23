// Package task 实现定时任务：5 字段 cron 解析、磁盘持久化、按分钟调度的
// Scheduler 以及供模型调用的 ScheduleCron 工具。
package task

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron 是一个解析后的 5 字段 cron 表达式，字段语义与标准 cron 一致：
// 分 时 日 月 周，周字段 0 表示周日、7 也可表示周日。
// 所有时间按传入时间的时区解释；本地时区由调用方决定。
type Cron struct {
	// Minute 是分钟集合，取值 0-59。
	Minute []int
	// Hour 是小时集合，取值 0-23。
	Hour []int
	// DayOfMonth 是日期集合，取值 1-31。
	DayOfMonth []int
	// Month 是月份集合，取值 1-12。
	Month []int
	// DayOfWeek 是星期集合，取值 0-6，0 为周日。
	DayOfWeek []int
	// Expr 是原始表达式（已去除首尾空白）。
	Expr string
}

// fieldRange 描述一个 cron 字段的取值区间。
type fieldRange struct {
	min int
	max int
	// sundayAlias 为 true 时允许用 7 表示周日。
	sundayAlias bool
}

// cronRanges 按 分/时/日/月/周 的顺序定义字段区间。
var cronRanges = [5]fieldRange{
	{min: 0, max: 59},
	{min: 0, max: 23},
	{min: 1, max: 31},
	{min: 1, max: 12},
	{min: 0, max: 6, sundayAlias: true},
}

// ParseCron 解析 5 字段 cron 表达式。
// 支持通配符 *、单值 N、区间 N-M、步长 */N 与 N-M/S、以及逗号列表；
// 字段数量不为 5、取值越界或出现其他语法时返回错误。
func ParseCron(expr string) (Cron, error) {
	normalized := strings.TrimSpace(expr)
	parts := strings.Fields(normalized)
	if len(parts) != 5 {
		return Cron{}, fmt.Errorf("task: cron %q: expected 5 fields, got %d", expr, len(parts))
	}
	expanded := make([][]int, 5)
	for i, part := range parts {
		values, err := expandField(part, cronRanges[i])
		if err != nil {
			return Cron{}, fmt.Errorf("task: cron %q: field %d: %w", expr, i+1, err)
		}
		expanded[i] = values
	}
	return Cron{
		Minute:     expanded[0],
		Hour:       expanded[1],
		DayOfMonth: expanded[2],
		Month:      expanded[3],
		DayOfWeek:  expanded[4],
		Expr:       normalized,
	}, nil
}

// expandField 把一个字段展开成升序、去重的取值集合。
func expandField(field string, spec fieldRange) ([]int, error) {
	if strings.TrimSpace(field) == "" {
		return nil, fmt.Errorf("empty value")
	}
	seen := make(map[int]struct{})
	add := func(v int) {
		seen[v] = struct{}{}
	}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("empty list element")
		}
		switch {
		case part == "*":
			for v := spec.min; v <= spec.max; v++ {
				add(v)
			}
		case strings.HasPrefix(part, "*/"):
			step, err := parseStep(strings.TrimPrefix(part, "*/"))
			if err != nil {
				return nil, err
			}
			for v := spec.min; v <= spec.max; v += step {
				add(v)
			}
		case strings.Contains(part, "-"):
			lo, hi, step, err := parseRange(part, spec)
			if err != nil {
				return nil, err
			}
			for v := lo; v <= hi; v += step {
				if v == 7 && spec.sundayAlias {
					add(0)
					continue
				}
				add(v)
			}
		default:
			v, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid value %q", part)
			}
			if v == 7 && spec.sundayAlias {
				add(0)
				continue
			}
			if v < spec.min || v > spec.max {
				return nil, fmt.Errorf("value %d out of range %d-%d", v, spec.min, spec.max)
			}
			add(v)
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("no values")
	}
	out := make([]int, 0, len(seen))
	for v := spec.min; v <= spec.max; v++ {
		if _, ok := seen[v]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// parseStep 解析 */N 或 N-M/S 中的步长。
func parseStep(raw string) (int, error) {
	step, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid step %q", raw)
	}
	if step < 1 {
		return 0, fmt.Errorf("step must be positive, got %d", step)
	}
	return step, nil
}

// parseRange 解析 N-M 或 N-M/S 形式的区间，并校验上下界。
func parseRange(part string, spec fieldRange) (int, int, int, error) {
	rangePart := part
	step := 1
	if idx := strings.Index(part, "/"); idx >= 0 {
		rangePart = part[:idx]
		var err error
		step, err = parseStep(part[idx+1:])
		if err != nil {
			return 0, 0, 0, err
		}
	}
	bounds := strings.Split(rangePart, "-")
	if len(bounds) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid range %q", part)
	}
	lo, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid range start %q", bounds[0])
	}
	hi, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid range end %q", bounds[1])
	}
	maxValue := spec.max
	if spec.sundayAlias {
		maxValue = 7
	}
	if lo < spec.min || hi > maxValue {
		return 0, 0, 0, fmt.Errorf("range %d-%d out of bounds %d-%d", lo, hi, spec.min, maxValue)
	}
	if lo > hi {
		return 0, 0, 0, fmt.Errorf("range start %d greater than end %d", lo, hi)
	}
	return lo, hi, step, nil
}

// Next 返回严格晚于 from 的下一个触发时间。
// 标准 cron 语义：日期与星期同时受限时，任一满足即可；只有一个受限时按该字段匹配。
// 一年内没有匹配（例如 2 月 30 日）时返回零值与 false。
func (c Cron) Next(from time.Time) (time.Time, bool) {
	if len(c.Minute) == 0 || len(c.Hour) == 0 || len(c.DayOfMonth) == 0 ||
		len(c.Month) == 0 || len(c.DayOfWeek) == 0 {
		return time.Time{}, false
	}
	minutes := toSet(c.Minute)
	hours := toSet(c.Hour)
	days := toSet(c.DayOfMonth)
	months := toSet(c.Month)
	weekdays := toSet(c.DayOfWeek)
	domWild := len(c.DayOfMonth) == 31
	dowWild := len(c.DayOfWeek) == 7

	loc := from.Location()
	// 秒与纳秒归零后前进一分钟，保证结果严格晚于 from。
	t := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), from.Minute(), 0, 0, loc).Add(time.Minute)

	const maxIterations = 366 * 24 * 60
	for i := 0; i < maxIterations; i++ {
		if _, ok := months[int(t.Month())]; !ok {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		dayMatches := true
		switch {
		case domWild && dowWild:
			dayMatches = true
		case domWild:
			_, dayMatches = weekdays[int(t.Weekday())]
		case dowWild:
			_, dayMatches = days[t.Day()]
		default:
			_, domOK := days[t.Day()]
			_, dowOK := weekdays[int(t.Weekday())]
			dayMatches = domOK || dowOK
		}
		if !dayMatches {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if _, ok := hours[t.Hour()]; !ok {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if _, ok := minutes[t.Minute()]; !ok {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// toSet 把取值切片转成集合。
func toSet(values []int) map[int]struct{} {
	out := make(map[int]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}
