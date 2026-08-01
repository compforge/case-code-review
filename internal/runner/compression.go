package runner

import "strings"

func reviewCompressionPrompts(args Args) (string, string) {
	var system, instruction []string
	for _, message := range args.Template.MemoryCompressionTask.Messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if message.Role == "system" {
			system = append(system, content)
			continue
		}
		content = strings.ReplaceAll(
			content,
			"{{context}}",
			"Summarize the conversation supplied above according to the system instructions.",
		)
		instruction = append(instruction, content)
	}
	return strings.Join(system, "\n\n"), strings.Join(instruction, "\n\n")
}
