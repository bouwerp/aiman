package gateway

import (
	"strings"
	"testing"
)

func TestNodeName(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{"plain", "devbox", "aiman-gateway-devbox"},
		{"lowercased", "DevBox", "aiman-gateway-devbox"},
		{"dots and underscores", "dev_box.example.com", "aiman-gateway-dev-box-example-com"},
		{"trimmed", "  -box-  ", "aiman-gateway-box"},
		{"unicode", "bøx", "aiman-gateway-b-x"},
		{"empty", "", "aiman-gateway"},
		{"only symbols", "...", "aiman-gateway"},
		{"long", strings.Repeat("a", 80), "aiman-gateway-" + strings.Repeat("a", 63-len("aiman-gateway-"))},
		{"long ends in dash after cut", strings.Repeat("a", 48) + "-" + "bbbbbbbb", "aiman-gateway-" + strings.Repeat("a", 48)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NodeName(tt.host); got != tt.want {
				t.Fatalf("NodeName(%q) = %q, want %q", tt.host, got, tt.want)
			}
		})
	}
}
