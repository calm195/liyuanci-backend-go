package resourceplaza

import (
	"strings"
	"testing"
)

// TestResourceFileValidation 覆盖文件名、大小与类型伪装，拒绝可执行文件与空文件。
func TestResourceFileValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  string
		valid bool
	}{
		{"课程.pdf", "%PDF-1.4\n测试", true}, {"课程.txt", "你好", true},
		{"课程.docx", "PK\x03\x04文档", true}, {"课程.exe", "MZ", false},
		{"假.pdf", "<html>", false}, {"../课程.txt", "内容", false},
		{"课程\n.txt", "内容", false}, {"空.txt", "", false},
		{"二进制.txt", "\xff\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateFile(tc.name, []byte(tc.data))
			if (err == nil) != tc.valid {
				t.Fatalf("文件校验不符：%v", err)
			}
		})
	}
	if _, err := validateFile("大.txt", []byte(strings.Repeat("a", FileMaxBytes+1))); err == nil {
		t.Fatal("未限制文件大小")
	}
}

// TestResourceFileInput 保持文件与外链互斥，并保留领域规范化的相同约束。
func TestResourceFileInput(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	in, err := ParseInput("create", []byte(`{"title":"资料","category_id":"`+id+`","file_id":"`+id+`"}`))
	if err != nil || in.FileID != id {
		t.Fatal("文件创建输入失败", err)
	}
	if _, err = normalizeInput("create", in); err != nil {
		t.Fatal("领域规范化失败", err)
	}
	for _, raw := range []string{
		`{"title":"资料","category_id":"` + id + `"}`,
		`{"title":"资料","category_id":"` + id + `","file_id":"` + id + `","download_url":"https://example.test/a"}`,
		`{"title":"资料","category_id":"` + id + `","file_id":"` + id + `","extraction_code":"123"}`,
	} {
		if _, err = ParseInput("create", []byte(raw)); err == nil {
			t.Fatal("非法输入未拒绝")
		}
	}
}
