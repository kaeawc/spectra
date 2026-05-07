package detect

import (
	"bytes"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type swiftInspectionSource interface {
	LinkedLibraries(exe string) []string
	ApplicationGroups(appPath string) []string
}

type realSwiftInspectionSource struct {
	appGroups []string
}

func (realSwiftInspectionSource) LinkedLibraries(exe string) []string {
	return otoolL(exe)
}

func (s realSwiftInspectionSource) ApplicationGroups(string) []string {
	return s.appGroups
}

func inspectSwiftApp(appPath, exe string, src swiftInspectionSource) *SwiftInspection {
	if exe == "" || src == nil {
		return nil
	}

	libs := src.LinkedLibraries(exe)
	if len(libs) == 0 {
		return nil
	}

	s := &SwiftInspection{
		RuntimeLibraries: swiftRuntimeLibraries(libs),
		AppleFrameworks:  appleFrameworks(libs),
		AppGroups:        src.ApplicationGroups(appPath),
	}
	s.UsesSwiftUI = containsString(s.AppleFrameworks, "SwiftUI.framework")
	s.UsesAppIntents = containsString(s.AppleFrameworks, "AppIntents.framework")
	s.UsesScreenCapture = containsString(s.AppleFrameworks, "ScreenCaptureKit.framework")

	if len(s.RuntimeLibraries) == 0 && !s.UsesSwiftUI && len(s.AppGroups) == 0 {
		return nil
	}
	return s
}

func swiftRuntimeLibraries(libs []string) []string {
	seen := map[string]struct{}{}
	for _, lib := range libs {
		base := filepath.Base(lib)
		if strings.HasPrefix(base, "libswift") && strings.HasSuffix(base, ".dylib") {
			seen[base] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func appleFrameworks(libs []string) []string {
	seen := map[string]struct{}{}
	for _, lib := range libs {
		if !isAppleFrameworkPath(lib) {
			continue
		}
		if fw := frameworkBase(lib); fw != "" {
			seen[fw] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func isAppleFrameworkPath(lib string) bool {
	return strings.HasPrefix(lib, "/System/Library/Frameworks/") ||
		strings.HasPrefix(lib, "/System/iOSSupport/System/Library/Frameworks/") ||
		strings.HasPrefix(lib, "/System/Library/PrivateFrameworks/")
}

func frameworkBase(lib string) string {
	for _, part := range strings.Split(lib, "/") {
		if strings.HasSuffix(part, ".framework") {
			return part
		}
	}
	return ""
}

var appGroupsBlockRE = regexp.MustCompile(`(?s)<key>com\.apple\.security\.application-groups</key>\s*<array>(.*?)</array>`)
var plistStringRE = regexp.MustCompile(`(?s)<string>\s*([^<]+?)\s*</string>`)

func applicationGroups(xml string) []string {
	m := appGroupsBlockRE.FindStringSubmatch(xml)
	if len(m) != 2 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, sm := range plistStringRE.FindAllStringSubmatch(m[1], -1) {
		if len(sm) != 2 {
			continue
		}
		group := strings.TrimSpace(sm[1])
		if group != "" {
			seen[group] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsString(list []string, want string) bool {
	i := sort.SearchStrings(list, want)
	return i < len(list) && list[i] == want
}

func parseOtoolLibraries(out []byte) []string {
	var libs []string
	for i, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			libs = append(libs, fields[0])
		}
	}
	return libs
}
