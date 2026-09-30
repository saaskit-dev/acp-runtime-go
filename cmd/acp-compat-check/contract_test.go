package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestContractRequiresIndependentSchemaPrerequisite(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	status, _ := runContractCheck()
	if status != statusInfra {
		t.Fatalf("missing Python/schema tool reported %s", status)
	}
}
func TestContractIsFreshBoundedAndCredentialFree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	dir := t.TempDir()
	args := filepath.Join(dir, "arguments")
	for name, script := range map[string]string{"python3": "#!/bin/sh\nexit 0\n", "go": "#!/bin/sh\nif [ -n \"$MAGPIE_BASE_URL$OPENAI_API_KEY$ACP_LIVE_CODEX$GOFLAGS\" ]; then exit 9; fi\nprintf '%s\\n' \"$*\" > \"$CONTRACT_ARGUMENTS\"\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("CONTRACT_ARGUMENTS", args)
	t.Setenv("MAGPIE_BASE_URL", "fixture-only")
	t.Setenv("OPENAI_API_KEY", "fixture-only")
	t.Setenv("ACP_LIVE_CODEX", "1")
	t.Setenv("GOFLAGS", "-tags=cc")
	status, detail := runContractCheck()
	if status != statusPass {
		t.Fatalf("fixture contract failed: %s %s", status, detail)
	}
	data, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test -count=1 -timeout=4m . ./harness ./simulator\n" {
		t.Fatalf("contract test command lacks fresh/bounded flags: %s", data)
	}
}
func TestNPMMetadataMustBeExactAndDigestValid(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell fixture")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "npm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho unknown\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := npmLatestVersion("fixture"); err == nil {
		t.Fatal("non-exact npm version accepted")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho sha512-invalid\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := (agentCheck{pkg: "fixture"}).artifactDigest("1.0.0"); err == nil {
		t.Fatal("invalid artifact digest accepted")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho sha256-"+strings.Repeat("A", 43)+"=\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := (agentCheck{pkg: "fixture"}).artifactDigest("1.0.0"); err != nil {
		t.Fatal(err)
	}
}
