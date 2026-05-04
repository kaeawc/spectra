package toolchain

import (
	"os"
	"strings"
)

// collectEnv captures the shell environment fields relevant to runtime
// resolution. Only named keys are captured — not full os.Environ().
func collectEnv() EnvSnapshot {
	snap := EnvSnapshot{
		JavaHome:  os.Getenv("JAVA_HOME"),
		GoPath:    os.Getenv("GOPATH"),
		GoRoot:    os.Getenv("GOROOT"),
		NpmPrefix: os.Getenv("NPM_CONFIG_PREFIX"),
		PnpmHome:  os.Getenv("PNPM_HOME"),
	}

	// Shell: use $SHELL, strip path prefix
	if sh := os.Getenv("SHELL"); sh != "" {
		snap.Shell = sh
		if idx := strings.LastIndex(sh, "/"); idx >= 0 {
			snap.Shell = sh[idx+1:]
		}
	}

	// PATH: ordered, deduped
	if path := os.Getenv("PATH"); path != "" {
		seen := map[string]bool{}
		for _, d := range strings.Split(path, ":") {
			if d != "" && !seen[d] {
				snap.PathDirs = append(snap.PathDirs, d)
				seen[d] = true
			}
		}
	}

	// Proxy vars
	proxyKeys := []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY",
		"no_proxy", "NO_PROXY", "all_proxy", "ALL_PROXY"}
	for _, k := range proxyKeys {
		if v := os.Getenv(k); v != "" {
			if snap.ProxyEnvVars == nil {
				snap.ProxyEnvVars = map[string]string{}
			}
			snap.ProxyEnvVars[strings.ToLower(k)] = v
		}
	}

	return snap
}
