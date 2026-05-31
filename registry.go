package acpruntime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	registryCacheMaxAge      = 24 * time.Hour
	registryHTTPTimeout      = 30 * time.Second
	maxRegistryJSONBytes     = 8 * 1024 * 1024
	maxArchiveDownloadBytes  = 256 * 1024 * 1024
	maxArchiveExtractBytes   = 512 * 1024 * 1024
	maxArchiveFileBytes      = 128 * 1024 * 1024
	maxArchiveExtractedFiles = 4096
)

var (
	registryURL        = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"
	registryHTTPClient = &http.Client{Timeout: registryHTTPTimeout}
)

var RegistryAgentAliases = map[string]string{
	"claude":         "claude-acp",
	"codex":          "codex-acp",
	"copilot":        "github-copilot-cli",
	"cursor-agent":   "cursor",
	"gemini-cli":     "gemini",
	"github":         "github-copilot-cli",
	"github-copilot": "github-copilot-cli",
	"open-code":      "opencode",
	"pi":             "pi-acp",
	"qwen":           "qwen-code",
	"sim":            LocalSimulatorAgentACPRegistryID,
	"simulator":      LocalSimulatorAgentACPRegistryID,
}

type Registry struct {
	Version string          `json:"version"`
	Agents  []RegistryAgent `json:"agents"`
}

type RegistryAgent struct {
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Version      string               `json:"version"`
	Description  string               `json:"description"`
	Distribution RegistryDistribution `json:"distribution"`
}

type RegistryDistribution struct {
	Binary map[string]BinaryTarget `json:"binary,omitempty"`
	NPX    *PackageTarget          `json:"npx,omitempty"`
	UVX    *PackageTarget          `json:"uvx,omitempty"`
}

type BinaryTarget struct {
	Archive string            `json:"archive"`
	Cmd     string            `json:"cmd"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type PackageTarget struct {
	Package string            `json:"package"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func ResolveRuntimeAgentID(agentID string) string {
	normalized := strings.TrimSpace(agentID)
	if alias, ok := RegistryAgentAliases[strings.ToLower(normalized)]; ok {
		return alias
	}
	return normalized
}

func ResolveRuntimeAgentFromRegistry(ctx context.Context, agentID string) (Agent, error) {
	resolved := ResolveRuntimeAgentID(agentID)
	if resolved == LocalSimulatorAgentACPRegistryID {
		return Agent{Type: LocalSimulatorAgentACPRegistryID, Command: "acp-simulator-agent", Args: []string{"--auth-mode", "none"}}, nil
	}
	switch resolved {
	case CodexACPRegistryID:
		return CreateCodexAgent(Agent{}), nil
	case ClaudeCodeACPRegistryID:
		return CreateClaudeCodeAgent(Agent{}), nil
	case GeminiCLIACPRegistryID:
		return CreateGeminiAgent(Agent{}), nil
	case GitHubCopilotACPRegistryID:
		return CreateGitHubCopilotAgent(Agent{}), nil
	case OpenCodeACPRegistryID:
		return CreateOpenCodeAgent(Agent{}), nil
	case PiACPRegistryID:
		return CreatePiAgent(Agent{}), nil
	}
	reg, err := fetchRegistry(ctx)
	if err != nil {
		return Agent{}, err
	}
	for _, item := range reg.Agents {
		if item.ID == resolved {
			launch, err := item.launch(ctx)
			if err != nil {
				return Agent{}, err
			}
			launch.Type = item.ID
			return launch, nil
		}
	}
	return Agent{}, fmt.Errorf("agent %q not found in ACP registry", resolved)
}

func ListRuntimeRegistryAgents(ctx context.Context) ([]RegistryAgent, error) {
	reg, err := fetchRegistry(ctx)
	if err != nil {
		return nil, err
	}
	return reg.Agents, nil
}

func fetchRegistry(ctx context.Context) (Registry, error) {
	cachePath := ResolveRuntimeCachePath("registry.json")
	if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < registryCacheMaxAge {
		bytes, err := os.ReadFile(cachePath)
		if err == nil {
			var reg Registry
			if json.Unmarshal(bytes, &reg) == nil {
				return reg, nil
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, registryURL, nil)
	if err != nil {
		return Registry{}, err
	}
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return Registry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Registry{}, fmt.Errorf("failed to fetch ACP registry: %s", resp.Status)
	}
	bytes, err := readLimited(resp.Body, maxRegistryJSONBytes, "ACP registry")
	if err != nil {
		return Registry{}, err
	}
	_ = os.MkdirAll(filepath.Dir(cachePath), 0o755)
	_ = os.WriteFile(cachePath, bytes, 0o644)
	var reg Registry
	return reg, json.Unmarshal(bytes, &reg)
}

func (a RegistryAgent) launch(ctx context.Context) (Agent, error) {
	if a.Distribution.NPX != nil {
		args := append([]string{"--yes", a.Distribution.NPX.Package}, a.Distribution.NPX.Args...)
		return Agent{Command: "npx", Args: args, Env: a.Distribution.NPX.Env}, nil
	}
	if a.Distribution.UVX != nil {
		args := append([]string{a.Distribution.UVX.Package}, a.Distribution.UVX.Args...)
		return Agent{Command: "uvx", Args: args, Env: a.Distribution.UVX.Env}, nil
	}
	if len(a.Distribution.Binary) > 0 {
		key := platformKey()
		target, ok := a.Distribution.Binary[key]
		if !ok {
			return Agent{}, fmt.Errorf("agent %s has no binary for %s", a.ID, key)
		}
		command, err := ensureBinary(ctx, a.ID, target)
		if err != nil {
			return Agent{}, err
		}
		return Agent{Command: command, Args: target.Args, Env: target.Env}, nil
	}
	return Agent{}, fmt.Errorf("agent %s has no supported distribution", a.ID)
}

func platformKey() string {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	if osName == "windows" {
		return "windows-" + arch
	}
	return osName + "-" + arch
}

func ensureBinary(ctx context.Context, agentID string, target BinaryTarget) (string, error) {
	filename := archiveFilename(target.Archive)
	if !isSupportedArchive(filename) {
		return "", fmt.Errorf("unsupported archive format %s", filename)
	}
	cacheDir, err := filepath.Abs(ResolveRuntimeCachePath("agents", agentID, archiveCacheName(filename)))
	if err != nil {
		return "", err
	}
	commandPath, err := safeCachePath(cacheDir, target.Cmd)
	if err != nil {
		return "", fmt.Errorf("invalid binary command path %q: %w", target.Cmd, err)
	}
	if info, err := os.Stat(commandPath); err == nil && !info.IsDir() {
		return commandPath, nil
	}
	parentDir := filepath.Dir(cacheDir)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(parentDir, "."+filepath.Base(cacheDir)+"-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.Archive, nil)
	if err != nil {
		return "", err
	}
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to download %s: %s", target.Archive, resp.Status)
	}
	if err := extractTarGzip(resp.Body, tmpDir); err != nil {
		return "", err
	}
	tmpCommandPath, err := safeCachePath(tmpDir, target.Cmd)
	if err != nil {
		return "", fmt.Errorf("invalid binary command path %q: %w", target.Cmd, err)
	}
	if info, err := os.Stat(tmpCommandPath); err != nil || info.IsDir() {
		if err != nil {
			return "", fmt.Errorf("binary command %q not found in archive: %w", target.Cmd, err)
		}
		return "", fmt.Errorf("binary command %q is a directory", target.Cmd)
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		return "", err
	}
	if err := os.Rename(tmpDir, cacheDir); err != nil {
		return "", err
	}
	return commandPath, nil
}

func readLimited(r io.Reader, max int64, label string) ([]byte, error) {
	bytes, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(bytes)) > max {
		return nil, fmt.Errorf("%s exceeds maximum size of %d bytes", label, max)
	}
	return bytes, nil
}

func archiveFilename(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Path != "" {
		return filepath.Base(parsed.Path)
	}
	return filepath.Base(rawURL)
}

func archiveCacheName(filename string) string {
	return strings.TrimSuffix(strings.TrimSuffix(filename, ".tar.gz"), ".tgz")
}

func isSupportedArchive(filename string) bool {
	return strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".tgz")
}

func isArchiveRootPath(name string) bool {
	return filepath.Clean(filepath.FromSlash(name)) == "."
}

func extractTarGzip(r io.Reader, destDir string) error {
	limited := &io.LimitedReader{R: r, N: maxArchiveDownloadBytes + 1}
	gz, err := gzip.NewReader(limited)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var totalBytes int64
	var files int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if isArchiveRootPath(hdr.Name) {
				continue
			}
			dest, err := safeCachePath(destDir, hdr.Name)
			if err != nil {
				return fmt.Errorf("unsafe archive directory %q: %w", hdr.Name, err)
			}
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if hdr.Size < 0 {
				return fmt.Errorf("archive entry %q has negative size", hdr.Name)
			}
			if hdr.Size > maxArchiveFileBytes {
				return fmt.Errorf("archive entry %q exceeds maximum file size", hdr.Name)
			}
			totalBytes += hdr.Size
			if totalBytes > maxArchiveExtractBytes {
				return fmt.Errorf("archive exceeds maximum extracted size of %d bytes", maxArchiveExtractBytes)
			}
			files++
			if files > maxArchiveExtractedFiles {
				return fmt.Errorf("archive exceeds maximum file count of %d", maxArchiveExtractedFiles)
			}
			dest, err := safeCachePath(destDir, hdr.Name)
			if err != nil {
				return fmt.Errorf("unsafe archive file %q: %w", hdr.Name, err)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode().Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported archive entry %q with type %d", hdr.Name, hdr.Typeflag)
		}
	}
	if limited.N == 0 {
		return fmt.Errorf("archive exceeds maximum download size of %d bytes", maxArchiveDownloadBytes)
	}
	return nil
}

func safeCachePath(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) {
		return "", fmt.Errorf("path must be relative")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes cache directory")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	dest := filepath.Join(rootAbs, clean)
	rel, err := filepath.Rel(rootAbs, dest)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes cache directory")
	}
	return dest, nil
}
