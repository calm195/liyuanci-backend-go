// Package resourceplaza 提供认证资料目录、后台维护和四类反馈的事务边界。
package resourceplaza

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/calm195/liyuanci-backend-go/internal/command"
)

// MaxInteger 是各客户端均可精确表达的版本上限。
const MaxInteger int64 = 9007199254740991
const whitespace = "\t\n\v\f\r \u0085\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var integerPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)

// ID 校验完整UUID文本并规范为小写。
func ID(v string) (string, error) {
	if !uuidPattern.MatchString(v) {
		return "", command.ErrInvalid
	}
	return strings.ToLower(v), nil
}

// Fold 仅折叠ASCII，保持其他文字原样。
func Fold(v string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}, v)
}

// Text 依据共享规范先归一化再检查码点或字节长度。
func Text(kind, v string) (string, error) {
	if !utf8.ValidString(v) {
		return "", command.ErrInvalid
	}
	if kind == "description" {
		v = strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\n"), "\r", "\n")
	}
	v = strings.Trim(v, whitespace)
	if kind == "download_url" {
		if len(v) == 0 || len(v) > 2048 || strings.Contains(v, "\\") {
			return "", command.ErrInvalid
		}
		for _, r := range v {
			if strings.ContainsRune(whitespace, r) || r < 32 || (r >= 127 && r <= 159) {
				return "", command.ErrInvalid
			}
		}
		for i := 0; i < len(v); i++ {
			if v[i] == '%' {
				if i+2 >= len(v) {
					return "", command.ErrInvalid
				}
				if _, e := strconv.ParseUint(v[i+1:i+3], 16, 8); e != nil {
					return "", command.ErrInvalid
				}
				i += 2
			}
		}
		u, e := url.Parse(v)
		if e != nil || u.Hostname() == "" || u.User != nil || !slices.Contains([]string{"http", "https"}, Fold(u.Scheme)) || strings.HasSuffix(u.Host, ":") {
			return "", command.ErrInvalid
		}
		// 整串百分号已检查，url.Parse检查端口语法；显式数字端口另限范围。
		if p := u.Port(); p != "" {
			n, e := strconv.Atoi(p)
			if e != nil || n < 1 || n > 65535 {
				return "", command.ErrInvalid
			}
		}
		return v, nil
	}
	lim, ok := map[string][2]int{"title": {1, 100}, "name": {1, 30}, "description": {0, 2000}, "extraction_code": {0, 64}, "q": {0, 100}, "reason": {1, 500}}[kind]
	if !ok || utf8.RuneCountInString(v) < lim[0] || utf8.RuneCountInString(v) > lim[1] {
		return "", command.ErrInvalid
	}
	for _, r := range v {
		if (r < 32 || (r >= 127 && r <= 159) || r == 0x2028 || r == 0x2029) && !((kind == "description" || kind == "reason") && (r == '\n' || r == '\t')) {
			return "", command.ErrInvalid
		}
	}
	return v, nil
}

// Input 是固定路由白名单解析后的规范命令；未使用字段不参与客户端输入。
type Input struct {
	Title                   string  `json:"title"`
	CategoryID              string  `json:"category_id"`
	Description             string  `json:"description"`
	DownloadURL             string  `json:"download_url"`
	ExtractionCode          *string `json:"extraction_code"`
	Name                    string  `json:"name"`
	SortOrder               int     `json:"sort_order"`
	Enabled                 bool    `json:"enabled"`
	Reason                  string  `json:"reason"`
	Kind                    string  `json:"kind"`
	ExpectedVersion         int64   `json:"expected_version"`
	ExpectedContentVersion  int64   `json:"expected_content_version"`
	ExpectedFeedbackVersion int64   `json:"expected_feedback_version"`
}

// Query 是已规范化并绑定游标的筛选条件。
type Query struct {
	Q, CategoryID, Status, Enabled, Cursor string
	Limit                                  int
	ExpectedVersion                        int64
}

var requestFields = map[string][]string{"create": {"title", "category_id", "download_url", "description", "extraction_code"}, "update": {"title", "category_id", "download_url", "description", "extraction_code", "expected_version"}, "publish": {"expected_version"}, "offline": {"expected_version", "reason"}, "category_create": {"name", "sort_order"}, "category_update": {"name", "sort_order", "enabled", "expected_version"}, "feedback": {"kind", "expected_content_version", "expected_feedback_version"}}

// ParseInput 拒绝重复键、未知字段、非整数token及孤立UTF-16代理转义。
func ParseInput(op string, raw []byte) (out Input, err error) {
	if !utf8.Valid(raw) {
		return out, command.ErrInvalid
	}
	// encoding/json 会替换孤立代理；先独立检查转义，避免默默更改输入。
	if !validSurrogates(raw) {
		return out, command.ErrInvalid
	}

	allowed, ok := requestFields[op]
	if !ok {
		return out, command.ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return out, command.ErrInvalid
	}
	values := map[string]json.RawMessage{}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return out, command.ErrInvalid
		}
		k, ok := t.(string)
		if !ok || !slices.Contains(allowed, k) {
			return out, command.ErrInvalid
		}
		if _, ok = values[k]; ok {
			return out, command.ErrInvalid
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return out, command.ErrInvalid
		}
		values[k] = v
	}
	if _, e = d.Token(); e != nil {
		return out, command.ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return out, command.ErrInvalid
	}
	for _, k := range allowed {
		_, ok := values[k]
		optional := (op == "create" && (k == "description" || k == "extraction_code")) || (op == "category_create" && k == "sort_order")
		if !ok && !optional {
			return out, command.ErrInvalid
		}
	}
	for k, b := range values {
		if slices.Contains([]string{"expected_version", "expected_content_version", "expected_feedback_version", "sort_order"}, k) {
			n, e := Integer(string(b), k == "sort_order" || k == "expected_feedback_version")
			if e != nil || (k == "sort_order" && n > 9999) {
				return out, command.ErrInvalid
			}
			switch k {
			case "expected_version":
				out.ExpectedVersion = n
			case "expected_content_version":
				out.ExpectedContentVersion = n
			case "expected_feedback_version":
				out.ExpectedFeedbackVersion = n
			case "sort_order":
				out.SortOrder = int(n)
			}
			continue
		}
		if k == "enabled" {
			if string(b) != "true" && string(b) != "false" {
				return out, command.ErrInvalid
			}
			out.Enabled = string(b) == "true"
			continue
		}
		var v string
		if string(b) == "null" && (k == "description" || k == "extraction_code") {
			v = ""
		} else if json.Unmarshal(b, &v) != nil || string(b) == "null" {
			return out, command.ErrInvalid
		}
		if k == "category_id" {
			v, err = ID(v)
		} else if k == "kind" {
			if !slices.Contains([]string{"valid", "link_invalid", "outdated", "incomplete"}, v) {
				err = command.ErrInvalid
			}
		} else {
			v, err = Text(k, v)
		}
		if err != nil {
			return out, err
		}
		switch k {
		case "title":
			out.Title = v
		case "category_id":
			out.CategoryID = v
		case "description":
			out.Description = v
		case "download_url":
			out.DownloadURL = v
		case "extraction_code":
			if v != "" {
				out.ExtractionCode = &v
			}
		case "name":
			out.Name = v
		case "reason":
			out.Reason = v
		case "kind":
			out.Kind = v
		}
	}
	if op == "category_create" {
		out.Enabled = true
	}
	return
}

// Integer 只接受规范十进制安全整数。
func Integer(v string, zero bool) (int64, error) {
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || !integerPattern.MatchString(v) || n > MaxInteger || (!zero && n == 0) {
		return 0, command.ErrInvalid
	}
	return n, nil
}
func validSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			m, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || m < 0xDC00 || m > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}

// normalizeInput 对领域直接调用同样执行正式规则，防止绕过HTTP产生不可投影数据。
func normalizeInput(op string, in Input) (Input, error) {
	b, e := json.Marshal(in)
	if e != nil {
		return Input{}, command.ErrInvalid
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(b, &all) != nil {
		return Input{}, command.ErrInvalid
	}
	fields, ok := requestFields[op]
	if !ok {
		return Input{}, command.ErrInvalid
	}
	selected := map[string]json.RawMessage{}
	for _, k := range fields {
		selected[k] = all[k]
	}
	b, e = json.Marshal(selected)
	if e != nil {
		return Input{}, command.ErrInvalid
	}
	return ParseInput(op, b)
}
