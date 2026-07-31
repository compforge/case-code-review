package llm

import "testing"

func TestCountMessagesTokensSumsMessageText(t *testing.T) {
	messages := []Message{
		NewTextMessage("user", "first message"),
		NewTextMessage("assistant", "second message"),
	}
	want := CountTokens("first message") + CountTokens("second message")
	if got := CountMessagesTokens(messages); got != want {
		t.Fatalf("CountMessagesTokens() = %d, want %d", got, want)
	}
}

func TestStripMarkdownFences(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "json", raw: "```json\n{\"ok\":true}\n```", want: `{"ok":true}`},
		{name: "plain fence", raw: "```\ntext\n```", want: "text"},
		{name: "unfenced", raw: " text ", want: "text"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := StripMarkdownFences(test.raw); got != test.want {
				t.Fatalf("StripMarkdownFences() = %q, want %q", got, test.want)
			}
		})
	}
}
