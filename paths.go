package acpruntime

import (
	"os"
	"path/filepath"
)

const (
	RuntimeHomeDirEnvVar  = "ACP_RUNTIME_HOME_DIR"
	RuntimeCacheDirEnvVar = "ACP_RUNTIME_CACHE_DIR"
)

func ResolveRuntimeHomePath(parts ...string) string {
	root := os.Getenv(RuntimeHomeDirEnvVar)
	if root == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			root = filepath.Join(home, ".acp-runtime")
		} else {
			root = ".acp-runtime"
		}
	}
	all := append([]string{root}, parts...)
	return filepath.Join(all...)
}

func ResolveRuntimeCachePath(parts ...string) string {
	root := os.Getenv(RuntimeCacheDirEnvVar)
	if root == "" {
		root = filepath.Join(ResolveRuntimeHomePath(), "cache")
	}
	all := append([]string{root}, parts...)
	return filepath.Join(all...)
}
