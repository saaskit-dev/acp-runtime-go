package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const cacheFormat = 2
const cacheMaxAge = 7 * 24 * time.Hour

type cacheIdentity struct {
	Suite               string `json:"suite"`
	Engine              string `json:"engine"`
	EngineVersion       string `json:"engineVersion"`
	ArtifactDigest      string `json:"artifactDigest"`
	RuntimeCommit       string `json:"runtimeCommit"`
	RuntimeBinaryDigest string `json:"runtimeBinaryDigest"`
	ContractDigest      string `json:"contractDigest"`
	FixtureDigest       string `json:"fixtureDigest"`
	ConfigDigest        string `json:"configDigest"`
	OS                  string `json:"os"`
	Arch                string `json:"arch"`
}
type cacheEntry struct {
	Identity   cacheIdentity `json:"identity"`
	Status     checkStatus   `json:"status"`
	VerifiedAt time.Time     `json:"verifiedAt"`
	ExpiresAt  time.Time     `json:"expiresAt"`
	Source     string        `json:"source"`
}
type cacheFile struct {
	Format  int                   `json:"format"`
	Entries map[string]cacheEntry `json:"entries"`
}

func newCache() cacheFile { return cacheFile{Format: cacheFormat, Entries: map[string]cacheEntry{}} }
func digestJSON(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (i cacheIdentity) key() string { return digestJSON(i) }
func (i cacheIdentity) complete() bool {
	return i.Suite != "" && i.Engine != "" && i.EngineVersion != "" && i.EngineVersion != "unknown" && i.ArtifactDigest != "" && i.RuntimeCommit != "" && i.RuntimeBinaryDigest != "" && i.ContractDigest != "" && i.FixtureDigest != "" && i.ConfigDigest != "" && i.OS != "" && i.Arch != ""
}
func (c cacheFile) hit(identity cacheIdentity, now time.Time) bool {
	entry, ok := c.Entries[identity.key()]
	return ok && identity.complete() && entry.Identity == identity && entry.Status == statusPass && !entry.VerifiedAt.After(now) && entry.ExpiresAt.After(now) && !entry.ExpiresAt.After(entry.VerifiedAt.Add(cacheMaxAge)) && entry.Source != ""
}
func loadCache(path string) (cacheFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return newCache(), err
	}
	var c cacheFile
	if err = json.Unmarshal(data, &c); err != nil {
		return newCache(), err
	}
	if c.Format != cacheFormat || c.Entries == nil {
		return newCache(), fmt.Errorf("legacy or invalid cache format; fresh verification required")
	}
	return c, nil
}
func saveCache(path string, cache cacheFile) error {
	cache.Format = cacheFormat
	now := time.Now()
	for key, entry := range cache.Entries {
		if entry.Status != statusPass || !entry.Identity.complete() || key != entry.Identity.key() || !cache.hit(entry.Identity, now) {
			delete(cache.Entries, key)
		}
	}
	// Keep only the newest 64 successful identities; no unbounded Actions cache.
	if len(cache.Entries) > 64 {
		keys := make([]string, 0, len(cache.Entries))
		for key := range cache.Entries {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return cache.Entries[keys[i]].VerifiedAt.After(cache.Entries[keys[j]].VerifiedAt) })
		for _, key := range keys[64:] {
			delete(cache.Entries, key)
		}
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'))
}
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".compat-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
func sourceDigests(root string) (string, string, error) {
	var source, fixtures []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") || rel == "go.mod" || rel == "go.sum" || rel == "Makefile" || rel == filepath.Join("harness", "validate_transcript.py") || strings.HasPrefix(filepath.ToSlash(rel), ".github/workflows/") {
			source = append(source, rel)
		}
		if (strings.Contains(filepath.ToSlash(rel), "testdata/") || strings.HasPrefix(filepath.ToSlash(rel), "harness/cases/") || strings.HasPrefix(filepath.ToSlash(rel), "schema/")) && (strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".py")) {
			fixtures = append(fixtures, rel)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	hashPaths := func(paths []string) (string, error) {
		sort.Strings(paths)
		h := sha256.New()
		for _, rel := range paths {
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "%s\x00%d\x00", rel, len(data))
			_, _ = h.Write(data)
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	a, err := hashPaths(source)
	if err != nil {
		return "", "", err
	}
	b, err := hashPaths(fixtures)
	return a, b, err
}
func runtimeIdentity() (cacheIdentity, error) {
	rootBytes, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return cacheIdentity{}, fmt.Errorf("runtime source unavailable: %w", err)
	}
	root := strings.TrimSpace(string(rootBytes))
	commit, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return cacheIdentity{}, fmt.Errorf("runtime commit unavailable: %w", err)
	}
	contract, fixtures, err := sourceDigests(root)
	if err != nil {
		return cacheIdentity{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return cacheIdentity{}, err
	}
	binaryDigest, err := fileDigest(executable)
	if err != nil {
		return cacheIdentity{}, err
	}
	return cacheIdentity{RuntimeBinaryDigest: binaryDigest, RuntimeCommit: strings.TrimSpace(string(commit)), ContractDigest: contract, FixtureDigest: fixtures, ConfigDigest: configDigest(), OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}
func configDigest() string {
	values := map[string]string{"prompt": sentinelToken, "gateway": fmt.Sprint(gatewayConfigured())}
	for _, name := range []string{"CLAUDE_GATEWAY_MODEL", "CODEX_GATEWAY_MODEL", "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "OPENAI_MODEL", "CODEX_MODEL"} {
		values[name] = os.Getenv(name)
	}
	// Never include credentials, arbitrary environment, URL userinfo/query or home paths.
	for _, name := range []string{gatewayBaseURLEnv, "ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"} {
		if parsed, err := url.Parse(os.Getenv(name)); err == nil {
			parsed.User = nil
			parsed.RawQuery = ""
			parsed.Fragment = ""
			values[name] = parsed.String()
		}
	}
	values["nativeHomeOverride"] = fmt.Sprint(os.Getenv("CODEX_NATIVE_TEST_HOME") != "")
	values["nativeConfigDigest"] = os.Getenv("COMPAT_NATIVE_CONFIG_DIGEST")
	return digestJSON(values)
}
