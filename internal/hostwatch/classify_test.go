package hostwatch

import "testing"

func TestClassify(t *testing.T) {
	for _, tt := range []struct{ command, want string }{{"/usr/bin/java -Dfoo=GradleDaemon", "gradle"}, {"java org.jetbrains.kotlin.daemon.KotlinCompileDaemon", "kotlin-daemon"}, {"java -jar app.jar", "java"}, {"/usr/bin/xcodebuild", "xcodebuild"}, {"/Applications/My Tools.app/Contents/MacOS/node", "node"}, {"swift-frontend", "swift-frontend"}, {"clang++", "clang"}, {"compile", "go"}, {"gopls", "go"}, {"node", "node"}, {"bun", "bun"}, {"codex", "codex"}, {"claude", "claude"}, {"qemu-system-aarch64", "qemu"}, {"emulator", "qemu"}, {"launchd_sim", "simulator"}, {"bsdtar", "bsdtar"}, {"git", "git"}, {"mdworker_shared", "spotlight"}, {"something", "other"}} {
		t.Run(tt.command, func(t *testing.T) {
			if got := Classify(tt.command); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
