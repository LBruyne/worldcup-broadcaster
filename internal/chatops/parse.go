package chatops

import "strings"

// parseCommand maps the (mention-stripped) message text to a canonical command
// and its argument. Returns "" when the text is not a recognized command so
// the bot stays silent on ordinary chatter. Both bare Chinese verbs ("做 X")
// and slash/ascii forms ("/claude X", "do X") are accepted.
func parseCommand(text string) (cmd, arg string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	// Chinese verb prefixes, which may run straight into the argument with no
	// space. Longest-first so "测试"/"状态" win before any 1-rune prefix.
	cn := []struct{ prefix, cmd string }{
		{"测试", "test"},
		{"状态", "status"},
		{"帮助", "help"},
		{"做", "do"},
		{"问", "ask"},
	}
	for _, p := range cn {
		if strings.HasPrefix(text, p.prefix) {
			return p.cmd, strings.TrimSpace(strings.TrimPrefix(text, p.prefix))
		}
	}
	fields := strings.Fields(text)
	head := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	switch head {
	case "claude", "do":
		return "do", rest
	case "ask":
		return "ask", rest
	case "test":
		return "test", rest
	case "status":
		return "status", rest
	case "diff":
		return "diff", rest
	case "pr":
		return "pr", rest
	case "help", "?":
		return "help", rest
	}
	return "", ""
}
