// Package yamlconfig 使用Viper读取、合并和映射YAML配置，不绑定环境变量或全局配置实例。
package yamlconfig

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// ErrInvalid 不携带配置正文、路径、密钥或解析器原始错误。
var ErrInvalid = errors.New("YAML配置无效，请核对字段、类型、时长单位和文件格式")

const limit = 1 << 20

func input(path string) ([]byte, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".yaml" && ext != ".yml" {
		return nil, ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalid
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		clear(raw)
		return nil, ErrInvalid
	}
	return raw, nil
}

// Read 只接受显式YAML路径；不搜索其他文件，不展开变量，也不回退旧格式。
func Read(path string, target any) error {
	raw, err := input(path)
	if err != nil {
		return err
	}
	defer clear(raw)
	return Unmarshal(raw, target)
}

func instance(target any) (*viper.Viper, error) {
	t := reflect.TypeOf(target)
	if t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(target).IsNil() {
		return nil, ErrInvalid
	}
	// 使用独立实例和不会与字段名冲突的分隔符；不启用AutomaticEnv/BindEnv或文件监视。
	v := viper.NewWithOptions(viper.KeyDelimiter("\x00"), viper.WithDecoderRegistry(checkedRegistry{viper.NewCodecRegistry(), t.Elem()}))
	v.SetConfigType("yaml")
	return v, nil
}

func decoderOptions(c *mapstructure.DecoderConfig) {
	c.TagName = "json"
	c.WeaklyTypedInput = false
	c.DecodeHook = strictTypes
	c.ErrorUnused = true
}

var bytesType = reflect.TypeOf([]byte(nil))

// strictTypes 仅允许显式带单位时长和Base64密钥，避免弱类型转换悄悄改变口令或数值。
func strictTypes(from, to reflect.Type, value any) (any, error) {
	if to == durationType {
		if from.Kind() != reflect.String {
			return nil, ErrInvalid
		}
		return time.ParseDuration(value.(string))
	}
	if to == bytesType {
		if from.Kind() != reflect.String {
			return nil, ErrInvalid
		}
		return base64.StdEncoding.DecodeString(value.(string))
	}
	if from.Kind() == reflect.Float32 || from.Kind() == reflect.Float64 {
		switch to.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return nil, ErrInvalid
		}
	}
	return value, nil
}

// Unmarshal 通过Viper读取并严格映射；原始错误统一脱敏，不向调用方暴露配置值。
func Unmarshal(raw []byte, target any) error {
	v, err := instance(target)
	if err != nil {
		return err
	}
	if v.ReadConfig(bytes.NewReader(raw)) != nil || v.UnmarshalExact(target, decoderOptions) != nil {
		return ErrInvalid
	}
	return nil
}

// ReadLayers 由Viper依次读取公共配置并合并环境覆盖；每层独立校验，数组整体替换。
func ReadLayers(paths []string, target any) error {
	v, err := instance(target)
	if err != nil || len(paths) == 0 {
		return ErrInvalid
	}
	for i, path := range paths {
		raw, err := input(path)
		if err != nil {
			return err
		}
		if i == 0 {
			err = v.ReadConfig(bytes.NewReader(raw))
		} else {
			err = v.MergeConfig(bytes.NewReader(raw))
		}
		clear(raw)
		if err != nil {
			return ErrInvalid
		}
	}
	if v.UnmarshalExact(target, decoderOptions) != nil {
		return ErrInvalid
	}
	return nil
}

var durationType = reflect.TypeOf(time.Duration(0))

// formatValues 只在测试配置生成时把结构体时长转换为带单位字符串。
func formatValues(value any, t reflect.Type) (any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if value == nil {
		return nil, nil
	}
	if t == durationType {
		number, ok := value.(json.Number)
		if !ok {
			return nil, ErrInvalid
		}
		n, err := number.Int64()
		return time.Duration(n).String(), err
	}

	switch data := value.(type) {
	case map[string]any:
		if t.Kind() != reflect.Struct && t.Kind() != reflect.Map {
			return value, nil
		}
		result := make(map[string]any, len(data))
		for key, item := range data {
			name, fieldType := key, t
			if t.Kind() == reflect.Map {
				fieldType = t.Elem()
			} else {
				found := false
				for i := 0; i < t.NumField(); i++ {
					field := t.Field(i)
					tag := strings.Split(field.Tag.Get("json"), ",")[0]
					if field.PkgPath != "" || tag == "-" {
						continue
					}
					if tag == "" {
						tag = field.Name
					}
					if strings.EqualFold(key, tag) {
						name, fieldType, found = tag, field.Type, true
						break
					}
				}
				if !found {
					return nil, ErrInvalid
				}
			}
			if _, exists := result[name]; exists {
				return nil, ErrInvalid
			}
			converted, err := formatValues(item, fieldType)
			if err != nil {
				return nil, err
			}
			result[name] = converted
		}
		return result, nil
	case []any:
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return value, nil
		}
		for i := range data {
			converted, err := formatValues(data[i], t.Elem())
			if err != nil {
				return nil, err
			}
			data[i] = converted
		}
	}
	return value, nil
}

// Marshal 生成可读YAML：期限有单位，字节密钥为Base64，不改变原始值。
func Marshal(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalid
	}
	defer clear(raw)
	var data any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&data) != nil {
		return nil, ErrInvalid
	}
	data, err = formatValues(data, reflect.TypeOf(value))
	if err != nil {
		return nil, ErrInvalid
	}
	values, ok := omitNull(data).(map[string]any)
	if !ok {
		return nil, ErrInvalid
	}
	v := viper.New()
	v.SetConfigType("yaml")
	if v.MergeConfigMap(values) != nil {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	if v.WriteConfigTo(&out) != nil {
		return nil, ErrInvalid
	}
	return out.Bytes(), nil
}

// omitNull 生成配置时省略可选空指针/空字节字段；显式输入null仍被覆盖加载器拒绝。
func omitNull(value any) any {
	switch v := value.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
		if n, err := strconv.ParseUint(string(v), 10, 64); err == nil {
			return n
		}
		n, _ := v.Float64()
		return n
	case map[string]any:
		for k, item := range v {
			if item == nil {
				delete(v, k)
			} else {
				v[k] = omitNull(item)
			}
		}
	case []any:
		for i, item := range v {
			v[i] = omitNull(item)
		}
	}
	return value
}
