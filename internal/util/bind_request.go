package util

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

// BindRequest 把 HTTP 请求数据绑定到 dest（必须是 struct 指针）。
//
// 支持三种请求格式，自动按 Content-Type / body 嗅探选择：
//   - application/json                         → 标准 JSON decode
//   - multipart/form-data                      → ParseMultipartForm(32MB)
//   - application/x-www-form-urlencoded        → ParseForm
//   - 漏标 Content-Type 但 body 以 { / [ 开头   → 按 JSON
//
// 字段匹配规则（按 tag 优先）：
//   1. `form:"xxx"` tag（如果有）
//   2. `json:"xxx"` tag（忽略 ,omitempty 等修饰）
//   3. 字段名小写
//
// 类型转换支持：
//   - 标量：string / bool / 各种 int / uint / float → 走 strconv
//     bool 额外识别 true/false/1/0/on/off/yes/no
//   - []byte：字符串按 base64 解码；multipart 时若该字段是文件则读文件内容
//   - []string：按 "," 分隔拆分；或同一 key 多次提交
//   - interface{}：如果字符串是合法 JSON → 反序列化；否则保留原字符串
//   - 嵌套 slice/struct/map：把字符串尝试 JSON 反序列化
//
// 参数优先级（同 key）：POST Form > URL Query，和常规直觉一致。
//
// 嵌套 JSON 字段约定（如 cmd 接口的 data、locate 接口的 wifi_scan）：
// form-data 不直接支持对象数组嵌套；推荐传 JSON 字符串形式，如：
//
//	data={"volume":30,"mode":"auto"}
//	wifi_scan=[{"mac":"AA:BB","rssi":-50}]
//
// BindRequest 会自动尝试 JSON 解析这些字段到 interface{}/[]struct{}/map 中。
func BindRequest(r *http.Request, dest interface{}) error {
	if dest == nil {
		return fmt.Errorf("dest is nil")
	}
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("dest must be a non-nil struct pointer")
	}

	ct := strings.ToLower(r.Header.Get("Content-Type"))
	isMultipart := strings.Contains(ct, "multipart/form-data")
	isURLEncoded := strings.Contains(ct, "application/x-www-form-urlencoded")
	isJSONCT := strings.Contains(ct, "application/json")

	// 读 body 一次：JSON 嗅探 / 失败后回填给 form 解析
	var raw []byte
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
		var err error
		raw, err = io.ReadAll(io.LimitReader(r.Body, 8<<20))
		_ = r.Body.Close()
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
	}
	trim := bytes.TrimSpace(raw)
	looksJSON := len(trim) > 0 && (trim[0] == '{' || trim[0] == '[')
	tryJSON := isJSONCT || (!isMultipart && !isURLEncoded && looksJSON)

	// ---- 1. JSON 路径 ----
	if tryJSON && len(trim) > 0 {
		if err := json.Unmarshal(trim, dest); err == nil {
			return mergeQueryFallback(r, dest, true)
		}
		// JSON 失败：body 已回填，继续 form（兼容误标 Content-Type）
	}

	// ---- 2. 解析 form（multipart 或 urlencoded / 空 CT） ----
	if isMultipart {
		if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
			// 不直接 return：可能是 body 为空但 query 有值
		}
	} else {
		if err := r.ParseForm(); err != nil {
			// 同上，不直接 return
		}
	}

	// ---- 3. 遍历 dest 字段赋值 ----
	elem := v.Elem()
	elemType := elem.Type()
	for i := 0; i < elem.NumField(); i++ {
		fieldT := elemType.Field(i)
		fieldV := elem.Field(i)
		if !fieldV.CanSet() {
			continue
		}
		// 跳过内嵌非结构体的非导出/私有字段（直接 continue）
		if fieldT.PkgPath != "" && !fieldT.Anonymous {
			continue
		}
		key := formKeyFor(fieldT)
		if key == "" {
			// 匿名结构体：递归绑定（如 dest 里面嵌套了别的结构体）
			if fieldT.Anonymous && fieldV.Kind() == reflect.Struct {
				subPtr := fieldV.Addr().Interface()
				if err := BindRequest(r, subPtr); err != nil {
					return err
				}
			}
			continue
		}

		// 取 value 字符串数组（如果是 multipart 文件另处理）
		var vals []string
		if r.PostForm != nil {
			vals = r.PostForm[key]
		}
		if len(vals) == 0 && r.Form != nil {
			vals = r.Form[key]
		}
		if len(vals) == 0 {
			// Multipart：如果是 []byte 且存在同名文件，直接读文件内容
			if r.MultipartForm != nil && r.MultipartForm.File != nil {
				if files, ok := r.MultipartForm.File[key]; ok && len(files) > 0 {
					if fieldV.Kind() == reflect.Slice && fieldV.Type().Elem().Kind() == reflect.Uint8 {
						data, err := readMultipartFile(files[0])
						if err != nil {
							return fmt.Errorf("form key %q read file: %w", key, err)
						}
						fieldV.SetBytes(data)
						continue
					}
				}
			}
			// 最后尝试 Query（因为 Form 已含 Query，但防万一）
			if qv := r.URL.Query().Get(key); qv != "" {
				vals = []string{qv}
			} else {
				continue // 没有值
			}
		}

		if err := setFieldValue(fieldV, vals, r, key); err != nil {
			return fmt.Errorf("form key %q: %w", key, err)
		}
	}

	return mergeQueryFallback(r, dest, false)
}

// mergeQueryFallback 遍历 dest，遇到零值字段时从 URL Query 再填一次。
// 如果 jsonBinded=true，意味着 JSON 已经赋值过了，这里只处理 query 独有的字段（一般不多）。
func mergeQueryFallback(r *http.Request, dest interface{}, jsonBinded bool) error {
	query := r.URL.Query()
	if len(query) == 0 {
		return nil
	}
	v := reflect.ValueOf(dest).Elem()
	elemType := v.Type()
	for i := 0; i < v.NumField(); i++ {
		fieldT := elemType.Field(i)
		fieldV := v.Field(i)
		if !fieldV.CanSet() || (fieldT.PkgPath != "" && !fieldT.Anonymous) {
			continue
		}
		if fieldT.Anonymous && fieldV.Kind() == reflect.Struct {
			_ = BindRequest(r, fieldV.Addr().Interface())
			continue
		}
		if jsonBinded && !isZeroValue(fieldV) {
			continue
		}
		key := formKeyFor(fieldT)
		if key == "" {
			continue
		}
		qv, ok := query[key]
		if !ok || len(qv) == 0 {
			continue
		}
		if isZeroValue(fieldV) {
			if err := setFieldValue(fieldV, qv, r, key); err != nil {
				return fmt.Errorf("query key %q: %w", key, err)
			}
		}
	}
	return nil
}

func isZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Slice:
		return v.Len() == 0
	case reflect.Map, reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Struct:
		return reflect.DeepEqual(v.Interface(), reflect.Zero(v.Type()).Interface())
	}
	return false
}

// formKeyFor 返回字段的 form key（按 form tag > json tag > 字段名小写）。
func formKeyFor(fieldT reflect.StructField) string {
	if tag, ok := fieldT.Tag.Lookup("form"); ok {
		if tag == "-" {
			return ""
		}
		// 忽略 ",optional" 之类修饰
		if i := strings.IndexByte(tag, ','); i >= 0 {
			tag = tag[:i]
		}
		return tag
	}
	if tag, ok := fieldT.Tag.Lookup("json"); ok {
		if tag == "-" {
			return ""
		}
		if i := strings.IndexByte(tag, ','); i >= 0 {
			tag = tag[:i]
		}
		if tag != "" {
			return tag
		}
	}
	return strings.ToLower(fieldT.Name)
}

func setFieldValue(fieldV reflect.Value, vals []string, r *http.Request, key string) error {
	// 如果是 []byte：先拿第一个值按 base64；若 multipart 同名文件存在则文件优先
	if fieldV.Kind() == reflect.Slice && fieldV.Type().Elem().Kind() == reflect.Uint8 {
		// 同名文件？（前面 MultipartForm 文件分支在上面走了，这里仅处理 base64 字符串）
		if len(vals) > 0 && vals[0] != "" {
			data, err := base64.StdEncoding.DecodeString(vals[0])
			if err != nil {
				// base64 失败？当作纯字节（有些客户端直接传 ASCII）
				data = []byte(vals[0])
			}
			fieldV.SetBytes(data)
		}
		return nil
	}

	// []string：拼接或按逗号分
	if fieldV.Kind() == reflect.Slice && fieldV.Type().Elem().Kind() == reflect.String {
		out := make([]string, 0, len(vals))
		for _, v := range vals {
			for _, piece := range strings.Split(v, ",") {
				piece = strings.TrimSpace(piece)
				if piece != "" {
					out = append(out, piece)
				}
			}
		}
		fieldV.Set(reflect.ValueOf(out))
		return nil
	}

	first := ""
	if len(vals) > 0 {
		first = strings.TrimSpace(vals[0])
	}
	if first == "" && len(vals) == 0 {
		return nil
	}

	switch fieldV.Kind() {
	case reflect.String:
		fieldV.SetString(first)

	case reflect.Bool:
		b, err := parseBool(first)
		if err != nil {
			return err
		}
		fieldV.SetBool(b)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(first, 10, 64)
		if err != nil {
			return fmt.Errorf("parse int: %w", err)
		}
		fieldV.SetInt(n)

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n, err := strconv.ParseUint(first, 10, 64)
		if err != nil {
			return fmt.Errorf("parse uint: %w", err)
		}
		fieldV.SetUint(n)

	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(first, 64)
		if err != nil {
			return fmt.Errorf("parse float: %w", err)
		}
		fieldV.SetFloat(n)

	case reflect.Interface:
		// Data interface{}：若字符串是合法 JSON → 解析；否则保留字符串原值
		if first == "" {
			return nil
		}
		var tmp interface{}
		if err := json.Unmarshal([]byte(first), &tmp); err == nil {
			fieldV.Set(reflect.ValueOf(tmp))
		} else {
			fieldV.Set(reflect.ValueOf(first))
		}

	case reflect.Map, reflect.Slice, reflect.Struct:
		// 嵌套类型：尝试 JSON 反序列化字符串（包括 wifi_scan 这种场景）
		if first == "" {
			return nil
		}
		ptr := reflect.New(fieldV.Type())
		if err := json.Unmarshal([]byte(first), ptr.Interface()); err != nil {
			return fmt.Errorf("parse nested json: %w", err)
		}
		fieldV.Set(ptr.Elem())

	default:
		return fmt.Errorf("unsupported field kind %s", fieldV.Kind())
	}
	return nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "1", "on", "yes":
		return true, nil
	case "false", "0", "off", "no", "":
		return false, nil
	}
	return false, strconv.ErrSyntax
}

func readMultipartFile(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}
