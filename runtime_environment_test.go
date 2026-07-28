package acpruntime

import (
	"reflect"
	"testing"
)

func TestApplyRuntimeEnvironmentUsesHostRuntimeAuthority(t *testing.T) {
	original := Agent{
		Env: map[string]string{
			"HOME":                "/temporary/home",
			RuntimeHomeDirEnvVar:  "/temporary/runtime-home",
			RuntimeCacheDirEnvVar: "/temporary/runtime-cache",
			"KEEP":                "value",
		},
	}

	got := applyRuntimeEnvironment(original, RuntimeOptions{
		HomeDir:  "/persistent/provider-home",
		CacheDir: "/ephemeral/provider-cache",
	})

	want := map[string]string{
		"HOME":                "/persistent/provider-home",
		RuntimeHomeDirEnvVar:  "/persistent/provider-home",
		RuntimeCacheDirEnvVar: "/ephemeral/provider-cache",
		"KEEP":                "value",
	}
	if !reflect.DeepEqual(got.Env, want) {
		t.Fatalf("runtime environment = %#v, want %#v", got.Env, want)
	}
	if original.Env["HOME"] != "/temporary/home" ||
		original.Env[RuntimeHomeDirEnvVar] != "/temporary/runtime-home" ||
		original.Env[RuntimeCacheDirEnvVar] != "/temporary/runtime-cache" {
		t.Fatalf("input Agent environment was mutated: %#v", original.Env)
	}
}

func TestApplyRuntimeEnvironmentWithoutRuntimePathsPreservesAgent(t *testing.T) {
	original := Agent{Env: map[string]string{"HOME": "/agent/home"}}

	got := applyRuntimeEnvironment(original, RuntimeOptions{})

	if !reflect.DeepEqual(got, original) {
		t.Fatalf("Agent changed without runtime paths: got %#v, want %#v", got, original)
	}
}
