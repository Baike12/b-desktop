package task

import (
	"testing"
	"time"
)

func TestParseCronValid(t *testing.T) {
	cases := []struct {
		expr    string
		minute  []int
		hour    []int
		dow     []int
		checkFn func(t *testing.T, c Cron)
	}{
		{
			expr: "*/5 * * * *",
			checkFn: func(t *testing.T, c Cron) {
				if len(c.Minute) != 12 || c.Minute[0] != 0 || c.Minute[11] != 55 {
					t.Fatalf("minute = %v", c.Minute)
				}
				if len(c.Hour) != 24 || len(c.DayOfMonth) != 31 || len(c.Month) != 12 || len(c.DayOfWeek) != 7 {
					t.Fatalf("wildcards not expanded: %+v", c)
				}
			},
		},
		{
			expr: "0 9 * * 1-5",
			checkFn: func(t *testing.T, c Cron) {
				if len(c.Minute) != 1 || c.Minute[0] != 0 {
					t.Fatalf("minute = %v", c.Minute)
				}
				if len(c.Hour) != 1 || c.Hour[0] != 9 {
					t.Fatalf("hour = %v", c.Hour)
				}
				want := []int{1, 2, 3, 4, 5}
				if len(c.DayOfWeek) != len(want) {
					t.Fatalf("dow = %v", c.DayOfWeek)
				}
				for i := range want {
					if c.DayOfWeek[i] != want[i] {
						t.Fatalf("dow = %v, want %v", c.DayOfWeek, want)
					}
				}
			},
		},
		{
			expr: "0,30 8-10/2 1,15 6 *",
			checkFn: func(t *testing.T, c Cron) {
				if len(c.Minute) != 2 || c.Minute[1] != 30 {
					t.Fatalf("minute = %v", c.Minute)
				}
				if len(c.Hour) != 2 || c.Hour[0] != 8 || c.Hour[1] != 10 {
					t.Fatalf("hour = %v", c.Hour)
				}
				if len(c.DayOfMonth) != 2 || c.DayOfMonth[0] != 1 || c.DayOfMonth[1] != 15 {
					t.Fatalf("dom = %v", c.DayOfMonth)
				}
				if len(c.Month) != 1 || c.Month[0] != 6 {
					t.Fatalf("month = %v", c.Month)
				}
			},
		},
		{
			expr: "0 0 * * 7",
			checkFn: func(t *testing.T, c Cron) {
				if len(c.DayOfWeek) != 1 || c.DayOfWeek[0] != 0 {
					t.Fatalf("dow = %v, want [0] for 7", c.DayOfWeek)
				}
			},
		},
		{
			expr: "0 0 * * 5-7",
			checkFn: func(t *testing.T, c Cron) {
				want := []int{0, 5, 6}
				if len(c.DayOfWeek) != 3 {
					t.Fatalf("dow = %v", c.DayOfWeek)
				}
				for i := range want {
					if c.DayOfWeek[i] != want[i] {
						t.Fatalf("dow = %v, want %v", c.DayOfWeek, want)
					}
				}
			},
		},
	}
	for _, c := range cases {
		got, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("ParseCron(%q): %v", c.expr, err)
		}
		c.checkFn(t, got)
		if got.Expr != c.expr {
			t.Fatalf("Expr = %q, want %q", got.Expr, c.expr)
		}
	}
}

func TestParseCronInvalid(t *testing.T) {
	cases := []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 13 *",
		"* * * * 8",
		"5-1 * * * *",
		"*/0 * * * *",
		"*/x * * * *",
		"1- * * * *",
		"MON * * * *",
		"1,,2 * * * *",
		"1.5 * * * *",
	}
	for _, expr := range cases {
		if _, err := ParseCron(expr); err == nil {
			t.Fatalf("ParseCron(%q) succeeded, want error", expr)
		}
	}
}

// localTime 构造固定时区的时间，避免测试受运行机器时区影响。
func localTime(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.Local)
}

func TestNextBasic(t *testing.T) {
	c, err := ParseCron("30 14 * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	from := localTime(2026, time.January, 1, 9, 0)
	next, ok := c.Next(from)
	if !ok {
		t.Fatal("Next returned false")
	}
	want := localTime(2026, time.January, 1, 14, 30)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestNextIsStrictlyAfter(t *testing.T) {
	c, err := ParseCron("15 * * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	from := localTime(2026, time.January, 1, 9, 15)
	next, ok := c.Next(from)
	if !ok {
		t.Fatal("Next returned false")
	}
	want := localTime(2026, time.January, 1, 10, 15)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestNextStepMinutes(t *testing.T) {
	c, err := ParseCron("*/15 * * * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	from := time.Date(2026, time.January, 1, 9, 0, 45, 0, time.Local)
	next, ok := c.Next(from)
	if !ok {
		t.Fatal("Next returned false")
	}
	want := localTime(2026, time.January, 1, 9, 15)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestNextDayOfMonthOrWeekday(t *testing.T) {
	// 2026-01-01 是周四；日期 13 号与周五同时受限时，任一满足即可。
	c, err := ParseCron("0 0 13 * 5")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	next, ok := c.Next(localTime(2026, time.January, 1, 0, 0))
	if !ok {
		t.Fatal("Next returned false")
	}
	want := localTime(2026, time.January, 2, 0, 0)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s (first Friday)", next, want)
	}

	domOnly, err := ParseCron("0 0 13 * *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	next, ok = domOnly.Next(localTime(2026, time.January, 1, 0, 0))
	if !ok {
		t.Fatal("Next returned false for dom rule")
	}
	want = localTime(2026, time.January, 13, 0, 0)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestNextMonthRollover(t *testing.T) {
	c, err := ParseCron("0 0 1 3 *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	next, ok := c.Next(localTime(2026, time.January, 15, 12, 0))
	if !ok {
		t.Fatal("Next returned false")
	}
	want := localTime(2026, time.March, 1, 0, 0)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestNextImpossibleDate(t *testing.T) {
	c, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	if _, ok := c.Next(localTime(2026, time.January, 1, 0, 0)); ok {
		t.Fatal("Next found Feb 30")
	}
}

func TestNextLeapDay(t *testing.T) {
	c, err := ParseCron("0 0 29 2 *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	// 从 2027-03-01 起算，2028-02-29 在 366 天窗口内。
	next, ok := c.Next(localTime(2027, time.March, 1, 0, 0))
	if !ok {
		t.Fatal("Next returned false")
	}
	want := localTime(2028, time.February, 29, 0, 0)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}
