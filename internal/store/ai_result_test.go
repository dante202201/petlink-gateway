package store

import (
	"encoding/json"
	"testing"
)

func TestDecodeAIResultJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // marshaled expected; empty means nil
	}{
		{name: "empty", in: "", want: ""},
		{name: "spaces", in: "  ", want: ""},
		{
			name: "object",
			in:   `{"pet_says":"你好","is_supported":true}`,
			want: `{"pet_says":"你好","is_supported":true}`,
		},
		{
			name: "invalid wraps string",
			in:   `not-json`,
			want: `"not-json"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeAIResultJSON(tc.in)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("want nil got %s", string(got))
				}
				return
			}
			if string(got) != tc.want {
				t.Fatalf("got %s want %s", string(got), tc.want)
			}
			if !json.Valid(got) {
				t.Fatal("result not valid json")
			}
		})
	}
}

func TestAudioRecordIMUMarshalsAsArray(t *testing.T) {
	r := AudioRecord{
		ID:  1,
		SN:  "PT-1",
		IMU: json.RawMessage(`[{"ts":1,"acc":[0,0,1],"gyro":[0,0,0]}]`),
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	arr, ok := m["imu"].([]interface{})
	if !ok || len(arr) != 1 {
		t.Fatalf("imu=%T %v", m["imu"], m["imu"])
	}
}

func TestTranslateRecordAIResultMarshalsAsObject(t *testing.T) {
	r := TranslateRecord{
		ID:            1,
		TranslateText: "hi",
		AIResult:      json.RawMessage(`{"pet_says":"hi","is_supported":true}`),
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ar, ok := m["ai_result"].(map[string]interface{})
	if !ok {
		t.Fatalf("ai_result should be object, got %T %v", m["ai_result"], m["ai_result"])
	}
	if ar["pet_says"] != "hi" {
		t.Fatalf("pet_says=%v", ar["pet_says"])
	}
}
