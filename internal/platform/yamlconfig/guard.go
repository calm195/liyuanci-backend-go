package yamlconfig

import (
	"bytes"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// checkedRegistry 复用Viper内置YAML解码器，在其键名折叠前检查每层输入。
// 不直接引入YAML解析库，不把Viper原始错误（可能带秘密值）传给外层。
type checkedRegistry struct {
	viper.DecoderRegistry
	target reflect.Type
}

func (r checkedRegistry) Decoder(format string) (viper.Decoder, error) {
	decoder, err := r.DecoderRegistry.Decoder(format)
	if err != nil {
		return nil, ErrInvalid
	}
	return checkedDecoder{decoder, r.target}, nil
}

type checkedDecoder struct {
	viper.Decoder
	target reflect.Type
}

func (d checkedDecoder) Decode(raw []byte, values map[string]any) error {
	if !explicitYAML(raw) || d.Decoder.Decode(raw, values) != nil || validKeys(values, 0) != nil {
		return ErrInvalid
	}
	// Viper的AllSettings会省略空映射；先校验原始映射，防止unknown: {}绕过未知字段检查。
	options := &mapstructure.DecoderConfig{Result: reflect.New(d.target).Interface()}
	decoderOptions(options)
	decoder, err := mapstructure.NewDecoder(options)
	if err != nil || decoder.Decode(values) != nil {
		return ErrInvalid
	}
	return nil
}

func validKeys(value any, depth int) error {
	if value == nil || depth > 64 {
		return ErrInvalid
	}
	switch data := value.(type) {
	case map[string]any:
		seen := map[string]bool{}
		for key, item := range data {
			name := strings.ToLower(key)
			if seen[name] || strings.ContainsRune(key, '\x00') {
				return ErrInvalid
			}
			seen[name] = true
			if validKeys(item, depth+1) != nil {
				return ErrInvalid
			}
		}
	case map[any]any:
		return ErrInvalid // 配置键仅允许字符串。
	case []any:
		for _, item := range data {
			if validKeys(item, depth+1) != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}

// explicitYAML 限制配置约定中的文档标记、锚点/别名及合并键；真正的语法解析仍由Viper完成。
// 引号内和缩进块中的口令、PEM及说明按字面保留，不能把字符串内容误认成语法。
func explicitYAML(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(raw) > limit || len(trimmed) == 0 {
		return false
	}
	var quote byte
	blockIndent := -1
	root, header := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		indent := len(line) - len(strings.TrimLeft(line, " "))
		text := strings.TrimSpace(line)
		if blockIndent >= 0 {
			if text == "" || indent > blockIndent {
				continue
			}
			blockIndent = -1
		}
		if quote == 0 && (text == "" || strings.HasPrefix(text, "#")) {
			continue
		}
		if quote == 0 && indent == 0 {
			fields := strings.Fields(text)
			if fields[0] == "---" || fields[0] == "..." {
				if root || header || fields[0] != "---" {
					return false
				}
				header = true
				continue
			}
		}
		if !root {
			if text[0] == '{' || text[0] == '[' || text[0] == '%' {
				return false
			}
			root = true
		}
		start, quotedEnd := true, false
		var syntax strings.Builder
		for i := 0; i < len(line); i++ {
			c := line[i]
			if quote != 0 {
				if quote == '"' && c == '\\' {
					i++
					continue
				}
				if c == quote {
					if quote == '\'' && i+1 < len(line) && line[i+1] == '\'' {
						i++
						continue
					}
					quote = 0
					quotedEnd = true
				}
				continue
			}
			if c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
				break
			}
			if (c == '\'' || c == '"') && start {
				quote = c
				start = false
				syntax.WriteByte('x')
				continue
			}
			if start && (c == '&' || c == '*' || c == '!') {
				return false
			}
			if start && c == '<' && strings.HasPrefix(line[i:], "<<") && strings.HasPrefix(strings.TrimLeft(line[i+2:], " \t"), ":") {
				return false
			}
			syntax.WriteByte(c)
			switch c {
			case ' ', '\t':
			case '[', '{', ',':
				start = true
			case ':':
				start = quotedEnd || i+1 == len(line) || line[i+1] == ' ' || line[i+1] == '\t'
			case '-':
				start = start && (i+1 == len(line) || line[i+1] == ' ' || line[i+1] == '\t')
			default:
				start = false
			}
			if c != ' ' && c != '\t' {
				quotedEnd = false
			}
		}
		fields := strings.Fields(syntax.String())
		if quote == 0 && len(fields) > 0 {
			last := fields[len(fields)-1]
			if (last[0] == '|' || last[0] == '>') && strings.Trim(last[1:], "+-123456789") == "" {
				blockIndent = indent
			}
		}
	}
	return root && quote == 0
}
