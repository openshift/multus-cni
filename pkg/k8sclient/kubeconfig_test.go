package k8sclient

import (
	"testing"
)

func TestSanitizeBracketedHost(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "hostname in brackets",
			input:    "https://[api-int.ci-op-xxx.example.com]:6443",
			expected: "https://api-int.ci-op-xxx.example.com:6443",
		},
		{
			name:     "IPv4 in brackets",
			input:    "https://[10.0.0.1]:6443",
			expected: "https://10.0.0.1:6443",
		},
		{
			name:     "IPv6 preserved",
			input:    "https://[2001:db8::1]:6443",
			expected: "https://[2001:db8::1]:6443",
		},
		{
			name:     "IPv6 with hex ending preserved",
			input:    "https://[2001:db8::face]:6443",
			expected: "https://[2001:db8::face]:6443",
		},
		{
			name:     "IPv6 loopback preserved",
			input:    "https://[::1]:6443",
			expected: "https://[::1]:6443",
		},
		{
			name:     "no brackets unchanged",
			input:    "https://api-server.example.com:6443",
			expected: "https://api-server.example.com:6443",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeBracketedHost(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeBracketedHost(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
