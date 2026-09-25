package cmd

import "testing"

// TestResolveServerPort asserts the CLI --port / config precedence for the
// serve command: an explicitly-passed --port always wins over the config value,
// while an unset flag falls back to the config, then to the flag default.
func TestResolveServerPort(t *testing.T) {
	tests := []struct {
		name       string
		flagSet    bool
		flagPort   string
		configPort string
		want       string
	}{
		{
			name:       "explicit flag overrides config even when equal to default",
			flagSet:    true,
			flagPort:   ":8080",
			configPort: ":9000",
			want:       ":8080",
		},
		{
			name:       "explicit flag overrides config",
			flagSet:    true,
			flagPort:   ":7000",
			configPort: ":9000",
			want:       ":7000",
		},
		{
			name:       "unset flag uses config",
			flagSet:    false,
			flagPort:   ":8080",
			configPort: ":9000",
			want:       ":9000",
		},
		{
			name:       "unset flag and empty config uses flag default",
			flagSet:    false,
			flagPort:   ":8080",
			configPort: "",
			want:       ":8080",
		},
		{
			name:       "explicit flag with empty config uses flag",
			flagSet:    true,
			flagPort:   ":7000",
			configPort: "",
			want:       ":7000",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveServerPort(tc.flagSet, tc.flagPort, tc.configPort)
			if got != tc.want {
				t.Errorf("resolveServerPort(%v, %q, %q) = %q, want %q",
					tc.flagSet, tc.flagPort, tc.configPort, got, tc.want)
			}
		})
	}
}
