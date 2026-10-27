package bot

// splitMessage leaves room for Telegram's UTF-16 length accounting, including
// supplementary characters such as emoji, and never splits a UTF-8 character.
func splitMessage(text string) []string {
	const limit = 3500
	var chunks []string
	start, units := 0, 0
	for offset, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			chunks = append(chunks, text[start:offset])
			start, units = offset, 0
		}
		units += width
	}
	if start < len(text) {
		chunks = append(chunks, text[start:])
	}
	return chunks
}
