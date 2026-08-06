package corefile

// SelectCommand returns the suggested Command matching a short action name
// ("jstack" or "jmap-histo"), letting callers execute a specific offline
// inspection instead of only printing the whole suggestion list.
func SelectCommand(report Report, action string) (Command, bool) {
	for _, c := range report.Commands {
		switch action {
		case "jstack":
			if len(c.Args) > 0 && c.Args[0] == "jstack" {
				return c, true
			}
		case "jmap-histo":
			if len(c.Args) >= 2 && c.Args[0] == "jmap" && c.Args[1] == "--histo" {
				return c, true
			}
		}
	}
	return Command{}, false
}
