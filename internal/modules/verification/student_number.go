package verification

// validStudentNumber 接受四位非零开头年份和八位数字，不限制入学年份区间。
// 学号保持字符串，不能丢失尾部八位中的前导零，也不自动改写用户输入。
func validStudentNumber(value string) bool {
	if len(value) != 12 || value[0] < '1' || value[0] > '9' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
