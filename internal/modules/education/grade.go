// Package education 提供账号展示与报名快照共享的入学年级规则，不依赖持久化。
package education

import (
	"strconv"
	"strings"
	"time"
)

var beijing = time.FixedZone("Asia/Shanghai", 8*60*60)

// GradeCode 以九月一日为学年起点；旧资料缺少入学信息时保留原年级。
func GradeCode(year int, level, override, legacy string, at time.Time) string {
	if year == 0 || level == "" {
		return legacy
	}
	if override != "" {
		return override
	}
	current := at.In(beijing)
	academicYear := current.Year()
	if current.Month() < time.September {
		academicYear--
	}
	n := academicYear - year + 1
	if n < 1 {
		return ""
	}
	prefix := level
	if level == "doctor" {
		prefix = "doctoral"
	}
	return prefix + "_" + strconv.Itoa(n)
}

// Valid 校验成对的入学信息及同学历的手动年级，零值仅供旧资料兼容。
func Valid(year int, level, override string, at time.Time) bool {
	if year == 0 && level == "" {
		return override == ""
	}
	current := at.In(beijing)
	latest := current.Year()
	if current.Month() < time.September {
		latest--
	}
	if year < 1900 || year > latest || (level != "undergraduate" && level != "master" && level != "doctor") {
		return false
	}
	if override == "" {
		return true
	}
	prefix := level
	if level == "doctor" {
		prefix = "doctoral"
	}
	suffix, ok := strings.CutPrefix(override, prefix+"_")
	n, err := strconv.Atoi(suffix)
	return ok && err == nil && n >= 1 && n <= 100 && strconv.Itoa(n) == suffix
}

// ValidGradeCode 识别计算生成的年级；不改变历史年级代码。
func ValidGradeCode(code string) bool {
	for _, level := range []string{"undergraduate", "master", "doctoral"} {
		suffix, ok := strings.CutPrefix(code, level+"_")
		n, err := strconv.Atoi(suffix)
		if ok && err == nil && n >= 1 && n <= 200 && strconv.Itoa(n) == suffix {
			return true
		}
	}
	return false
}

// Label 为有效的自动年级提供常用中文名称，超出常见学制仍展示实际年级。
func Label(code string) string {
	parts := strings.Split(code, "_")
	if len(parts) != 2 || !ValidGradeCode(code) {
		return code
	}
	n, _ := strconv.Atoi(parts[1])
	number := parts[1]
	if n <= 10 {
		number = []string{"", "一", "二", "三", "四", "五", "六", "七", "八", "九", "十"}[n]
	}
	switch parts[0] {
	case "undergraduate":
		return "大" + number
	case "master":
		return "研" + number
	case "doctoral":
		return "博" + number
	}
	return code
}
