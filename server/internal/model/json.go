package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONMap 是 JSON 列的 GORM 适配类型（驱动无关，sqlite/mysql/postgres 均以 TEXT 承载）：
// Go 侧为 map[string]string，落库为 JSON 文本，API 序列化为 JSON 对象（空为 null）。
type JSONMap map[string]string

// Value 实现 driver.Valuer：nil → NULL，否则 → JSON 文本。
func (j JSONMap) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(map[string]string(j))
}

// Scan 实现 sql.Scanner：JSON 字节 → map。
func (j *JSONMap) Scan(v interface{}) error {
	if v == nil {
		*j = nil
		return nil
	}
	var b []byte
	switch x := v.(type) {
	case []byte:
		b = x
	case string:
		b = []byte(x)
	default:
		return fmt.Errorf("JSONMap.Scan: 不支持的类型 %T", v)
	}
	if len(b) == 0 || string(b) == "null" {
		*j = nil
		return nil
	}
	return json.Unmarshal(b, j)
}

// MarshalJSON 让 API 直接输出 JSON 对象（nil → null）。
func (j JSONMap) MarshalJSON() ([]byte, error) {
	if j == nil {
		return []byte("null"), nil
	}
	return json.Marshal(map[string]string(j))
}

// JSONObject 是「值为任意 JSON 类型」的列适配（JSONMap 值只能是 string，
// 审计 detail / 凭据 extra 这类嵌套混型对象用它）。nil 落库为 NULL。
type JSONObject map[string]interface{}

func (j JSONObject) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(map[string]interface{}(j))
}

func (j *JSONObject) Scan(v interface{}) error {
	if v == nil {
		*j = nil
		return nil
	}
	var b []byte
	switch x := v.(type) {
	case []byte:
		b = x
	case string:
		b = []byte(x)
	default:
		return fmt.Errorf("JSONObject.Scan: 不支持的类型 %T", v)
	}
	if len(b) == 0 || string(b) == "null" {
		*j = nil
		return nil
	}
	return json.Unmarshal(b, j)
}

func (j JSONObject) MarshalJSON() ([]byte, error) {
	if j == nil {
		return []byte("null"), nil
	}
	return json.Marshal(map[string]interface{}(j))
}
