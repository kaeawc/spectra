package hostwatch

import "testing"

func TestClassify(t *testing.T) {
	for _, tt := range []struct{ comm, args, want string }{
		{"/opt/homebrew/Cellar/openjdk@21/21.0.5/bin/java", "java org.gradle.launcher.daemon.bootstrap.GradleDaemon 9.7.1", "gradle"},
		{"/opt/homebrew/Cellar/openjdk@21/21.0.5/bin/java", "java org.jetbrains.kotlin.daemon.KotlinCompileDaemon", "kotlin-daemon"},
		{"/opt/homebrew/Cellar/openjdk@21/21.0.5/bin/java", "java org.gradle.wrapper.GradleWrapperMain", "gradle"},
		{"/opt/homebrew/Cellar/openjdk@21/21.0.5/bin/java", "java -jar app.jar", "java"},
		{"/usr/bin/xcodebuild", "", "xcodebuild"},
		{"/Applications/My Tools.app/Contents/MacOS/node", "", "node"},
		{"swift-frontend", "", "swift-frontend"}, {"clang++", "", "clang"},
		{"compile", "", "go"}, {"gopls", "", "go"}, {"node", "", "node"},
		{"bun", "", "bun"}, {"codex", "", "codex"}, {"claude", "", "claude"},
		{"qemu-system-aarch64", "", "qemu"}, {"emulator", "", "qemu"},
		{"launchd_sim", "", "simulator"}, {"bsdtar", "", "bsdtar"},
		{"git", "", "git"}, {"mdworker_shared", "", "spotlight"},
		{"something", "", "other"},
	} {
		t.Run(tt.comm+tt.args, func(t *testing.T) {
			if got := Classify(tt.comm, tt.args); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
