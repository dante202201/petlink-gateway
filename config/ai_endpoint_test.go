package config

import "testing"

func TestAIConfigTranslateEndpoint(t *testing.T) {
	cases := []struct {
		name string
		cfg  AIConfig
		want string
	}{
		{
			name: "base+port",
			cfg:  AIConfig{BaseURL: "http://10.0.0.10", TranslatePort: 6001},
			want: "http://10.0.0.10:6001/translate",
		},
		{
			name: "base already has port",
			cfg:  AIConfig{BaseURL: "http://10.0.0.10:6001", TranslatePort: 6001},
			want: "http://10.0.0.10:6001/translate",
		},
		{
			name: "trailing slash",
			cfg:  AIConfig{BaseURL: "http://10.0.0.10/", TranslatePort: 6001},
			want: "http://10.0.0.10:6001/translate",
		},
		{
			name: "empty",
			cfg:  AIConfig{},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.TranslateEndpoint()
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
