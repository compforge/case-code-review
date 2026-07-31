package viewer

import "testing"

func TestBrowserURL(t *testing.T) {
	tests := map[string]string{
		":3000":          "http://localhost:3000",
		"localhost:5483": "http://localhost:5483",
		"127.0.0.1:5483": "http://127.0.0.1:5483",
	}
	for addr, want := range tests {
		if got := BrowserURL(addr); got != want {
			t.Errorf("BrowserURL(%q) = %q, want %q", addr, got, want)
		}
	}
}
