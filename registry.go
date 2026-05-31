package acpruntime

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const registryURL = "https://cdn.agentclientprotocol.com/registry/v1/latest/registry.json"

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
	if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Registry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Registry{}, fmt.Errorf("failed to fetch ACP registry: %s", resp.Status)
	}
	bytes, err := io.ReadAll(resp.Body)
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
	filename := filepath.Base(target.Archive)
	cacheDir := ResolveRuntimeCachePath("agents", agentID, strings.TrimSuffix(strings.TrimSuffix(filename, ".tar.gz"), ".tgz"))
	commandPath := filepath.Join(cacheDir, strings.TrimPrefix(target.Cmd, "./"))
	if info, err := os.Stat(commandPath); err == nil && !info.IsDir() {
		return commandPath, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.Archive, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("failed to download %s: %s", target.Archive, resp.Status)
	}
	if strings.HasSuffix(filename, ".tar.gz") || strings.HasSuffix(filename, ".tgz") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			dest := filepath.Join(cacheDir, filepath.Clean(hdr.Name))
			if !strings.HasPrefix(dest, cacheDir) {
				continue
			}
			if hdr.FileInfo().IsDir() {
				_ = os.MkdirAll(dest, 0o755)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
		}
		return commandPath, nil
	}
	return "", fmt.Errorf("unsupported archive format %s", filename)
}
