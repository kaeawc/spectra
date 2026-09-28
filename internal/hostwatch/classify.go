package hostwatch

import (
	"path/filepath"
	"strings"
)

var directKinds = map[string]string{
	"java": "java", "xcodebuild": "xcodebuild", "swift-frontend": "swift-frontend", "clang": "clang", "clang++": "clang",
	"go": "go", "compile": "go", "link": "go", "gopls": "go", "node": "node", "bun": "bun", "codex": "codex", "claude": "claude",
	"emulator": "qemu", "launchd_sim": "simulator", "bsdtar": "bsdtar", "git": "git", "mds_stores": "spotlight", "gradle": "gradle",
}

func Classify(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "other"
	}
	name := strings.ToLower(filepath.Base(fields[0]))
	s := strings.ToLower(command)
	fullName := strings.ToLower(filepath.Base(command))
	if strings.Contains(s, "kotlin-daemon") || strings.Contains(s, "kotlincompilerdaemon") || strings.Contains(s, "kotlincompiledaemon") {
		return "kotlin-daemon"
	}
	if strings.Contains(s, "gradledaemon") || strings.Contains(s, "gradlewrappermain") || strings.Contains(s, "gradle-launcher") {
		return "gradle"
	}
	if kind, ok := directKinds[fullName]; ok {
		return kind
	}
	if strings.HasPrefix(name, "qemu-system") || strings.HasPrefix(fullName, "qemu-system") {
		return "qemu"
	}
	if strings.HasPrefix(name, "mdworker") || strings.HasPrefix(fullName, "mdworker") {
		return "spotlight"
	}
	if kind, ok := directKinds[name]; ok {
		return kind
	}
	return "other"
}
