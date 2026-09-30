package main

import (
	"encoding/json"
	"errors"
	acp "github.com/saaskit-dev/acp-runtime-go"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureIdentity() cacheIdentity {
	return cacheIdentity{Suite: "wrapper", Engine: "engine", EngineVersion: "1.2.3", ArtifactDigest: "sha512-fixture", RuntimeCommit: "abc", RuntimeBinaryDigest: "binary", ContractDigest: "source", FixtureDigest: "schema-fixture", ConfigDigest: "config", OS: "linux", Arch: "amd64"}
}
func TestCacheIdentityEveryDimension(t *testing.T) {
	id := fixtureIdentity()
	now := time.Now().UTC()
	cache := newCache()
	cache.Entries[id.key()] = cacheEntry{Identity: id, Status: statusPass, VerifiedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), Source: "fixture"}
	if !cache.hit(id, now) {
		t.Fatal("exact identity missed")
	}
	changes := []func(*cacheIdentity){func(i *cacheIdentity) { i.Suite = "native" }, func(i *cacheIdentity) { i.Engine = "other" }, func(i *cacheIdentity) { i.EngineVersion = "1.2.4" }, func(i *cacheIdentity) { i.ArtifactDigest = "changed" }, func(i *cacheIdentity) { i.RuntimeCommit = "other" }, func(i *cacheIdentity) { i.RuntimeBinaryDigest = "changed" }, func(i *cacheIdentity) { i.ContractDigest = "changed" }, func(i *cacheIdentity) { i.FixtureDigest = "changed" }, func(i *cacheIdentity) { i.ConfigDigest = "changed" }, func(i *cacheIdentity) { i.OS = "darwin" }, func(i *cacheIdentity) { i.Arch = "arm64" }}
	for index, change := range changes {
		other := id
		change(&other)
		if cache.hit(other, now) {
			t.Fatalf("identity change %d falsely cached", index)
		}
	}
	if cache.hit(id, now.Add(2*time.Hour)) {
		t.Fatal("expired evidence hit")
	}
}
func TestCacheLegacyIsSafeMiss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte(`{"@agentclientprotocol/codex-acp":"1.0.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadCache(path)
	if err == nil || len(c.Entries) != 0 {
		t.Fatalf("legacy cache accepted: %+v %v", c, err)
	}
}
func TestCacheOnlyPersistsPassAndNewestEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	cache := newCache()
	id := fixtureIdentity()
	now := time.Now().UTC()
	cache.Entries[id.key()] = cacheEntry{Identity: id, Status: statusPass, VerifiedAt: now, ExpiresAt: now.Add(time.Hour), Source: "first"}
	if err := saveCache(path, cache); err != nil {
		t.Fatal(err)
	}
	id.EngineVersion = "1.2.4"
	cache.Entries[id.key()] = cacheEntry{Identity: id, Status: statusPass, VerifiedAt: now, ExpiresAt: now.Add(time.Hour), Source: "second"}
	bad := id
	bad.Engine = "failed"
	cache.Entries[bad.key()] = cacheEntry{Identity: bad, Status: statusFail, VerifiedAt: now, ExpiresAt: now.Add(time.Hour), Source: "failure"}
	if err := saveCache(path, cache); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.hit(id, now) || loaded.hit(bad, now) {
		t.Fatal("updated successful evidence lost or failure cached")
	}
}
func TestSourceDigestIncludesDirtyRuntimeSchemaAndFixtures(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "harness/cases"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "testdata/acp"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"runtime.go": "package runtime", "harness/cases/a.json": "{}", "testdata/acp/schema.json": "{}"}
	for path, text := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, fixture, err := sourceDigests(root)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "runtime.go"), []byte("package changed"), 0600)
	newSource, newFixture, _ := sourceDigests(root)
	if source == newSource || fixture != newFixture {
		t.Fatal("dirty runtime digest incorrect")
	}
	os.WriteFile(filepath.Join(root, "testdata/acp/schema.json"), []byte(`{"new":true}`), 0600)
	_, newFixture, _ = sourceDigests(root)
	if fixture == newFixture {
		t.Fatal("schema change missed")
	}
	fixture = newFixture
	os.WriteFile(filepath.Join(root, "harness/cases/a.json"), []byte(`{"new":true}`), 0600)
	_, newFixture, _ = sourceDigests(root)
	if fixture == newFixture {
		t.Fatal("fixture change missed")
	}
}
func TestConfigurationAndDiagnosticsDoNotExposeSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret-token-123")
	t.Setenv("UNIFIED_ROUTER_BASE_URL", "https://user:password@example.test/v1?token=hidden")
	first := configDigest()
	t.Setenv("OPENAI_API_KEY", "different-secret")
	if first != configDigest() {
		t.Fatal("secret changes must not be cached as credentials")
	}
	t.Setenv("CODEX_GATEWAY_MODEL", "changed-model")
	if first == configDigest() {
		t.Fatal("model config change missed")
	}
	diagnostic := redactDiagnostic("Authorization: Bearer abc123 api_key=xyz https://user:password@example.test/v1?token=hidden different-secret " + strings.Repeat("x", 5000))
	if len(diagnostic) > 4110 || strings.Contains(diagnostic, "different-secret") || strings.Contains(diagnostic, "password") || strings.Contains(diagnostic, "abc123") {
		t.Fatal("unbounded or unredacted diagnostic")
	}
	data, _ := json.Marshal(fixtureIdentity())
	if strings.Contains(string(data), "secret") {
		t.Fatal("cache stores secret")
	}
}
func TestTypedProtocolErrorBeatsTextHeuristics(t *testing.T) {
	for _, err := range []error{&checkError{Kind: failureProtocol, Cause: errors.New("authentication failed")}, &acp.RPCError{Code: -32602, Message: "invalid_api_key field missing"}, &acp.RuntimeError{Kind: acp.ErrorProtocol, Msg: "HTTP 503"}} {
		if got := classifyCheckResult(err, ""); got != "FAIL" {
			t.Fatalf("typed protocol error misclassified: %s", got)
		}
	}
	if classifyCheckResult(&checkError{Kind: failureInfra, Cause: errors.New("offline")}, "") != "INFRA_ERROR" {
		t.Fatal("typed infrastructure not respected")
	}
	if classifyCheckResult(nil, "not "+sentinelToken) != "FAIL" {
		t.Fatal("negative sentinel output passed")
	}
}
func TestSuiteRequiredOptionalSemantics(t *testing.T) {
	for _, tc := range []struct {
		results  []checkResult
		want     int
		complete bool
	}{{[]checkResult{{Required: true, Status: statusPass}, {Selected: true, Status: statusSkipped}}, 0, true}, {[]checkResult{{Required: true, Status: statusCached}, {Required: true, Status: statusSkipped}}, 2, false}, {[]checkResult{{Required: true, Status: statusPass}, {Selected: true, Status: statusFail}}, 1, true}, {[]checkResult{{Required: true, Status: statusInfra}}, 2, false}} {
		s := summarize(tc.results)
		if s.ExitCode != tc.want || s.RequiredComplete != tc.complete {
			t.Fatalf("wrong summary: %+v", s)
		}
	}
	for _, selection := range []string{"unknown", ""} {
		if _, _, err := suiteSelection(selection, ""); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	if _, _, err := suiteSelection("wrapper", "native"); err == nil {
		t.Fatal("unselected required suite accepted")
	}
	s := summarize([]checkResult{{Required: true, Status: statusCached}})
	if s.RequiredFreshPass {
		t.Fatal("cached evidence cannot automatically close issue")
	}
}
