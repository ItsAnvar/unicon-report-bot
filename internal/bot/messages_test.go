package bot

import (
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestSplitMessage(t *testing.T) {
	for _, text := range []string{"", "Salom", strings.Repeat("o‘📝ж", 4000), strings.Repeat("a", 7001)} {
		chunks := splitMessage(text)
		if strings.Join(chunks, "") != text {
			t.Fatal("message changed")
		}
		for _, chunk := range chunks {
			if !utf8.ValidString(chunk) {
				t.Fatal("invalid UTF-8")
			}
			if len(utf16.Encode([]rune(chunk))) > 3500 {
				t.Fatal("chunk exceeds Telegram limit")
			}
		}
	}
}
