package moderation

import (
	"strings"
	"testing"
)

// TestPublicationCommentsMarkdown 验证评论仅作为已审纯文本引用，并明确标记导出上限。
func TestPublicationCommentsMarkdown(t *testing.T) {
	var draft strings.Builder
	writePublicationComments(&draft, []AdminPublicationText{{
		Body:   "首行\n<script>内容</script>\n![图片](https://example.com)",
		Author: "匿名同学",
		Notice: "未经证实",
	}}, true)
	out := draft.String()
	for _, want := range []string{
		"## 暖评\n\n匿名同学",
		"未经证实\n\n> 首行",
		"> &lt;script&gt;内容&lt;/script&gt;",
		`> \!\[图片\](https://example.com)`,
		"其余公开暖评请在后台查看。",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("草稿缺少 %q", want)
		}
	}
}
