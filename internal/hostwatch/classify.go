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

func Classify(comm, args string) string {
	if comm == "" {
		return "other"
	}
	name := strings.ToLower(filepath.Base(comm))
	if name == "java" {
		s := strings.ToLower(args)
		if strings.Contains(s, "kotlin-daemon") || strings.Contains(s, "kotlincompilerdaemon") || strings.Contains(s, "kotlincompiledaemon") {
			return "kotlin-daemon"
		}
		if strings.Contains(s, "gradledaemon") || strings.Contains(s, "gradlewrappermain") || strings.Contains(s, "gradle-launcher") {
			return "gradle"
		}
	}
	if kind, ok := directKinds[name]; ok {
		return kind
	}
	if strings.HasPrefix(name, "qemu-system") {
		return "qemu"
	}
	if strings.HasPrefix(name, "mdworker") {
		return "spotlight"
	}
	return "other"
}
