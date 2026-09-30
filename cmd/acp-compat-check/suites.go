package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	acp "github.com/saaskit-dev/acp-runtime-go"
)

type checkStatus string

const (
	statusPass    checkStatus = "PASS"
	statusCached  checkStatus = "CACHED"
	statusSkipped checkStatus = "SKIPPED"
	statusInfra   checkStatus = "INFRA_ERROR"
	statusFail    checkStatus = "FAIL"
)

type checkResult struct {
	Selected   bool          `json:"selected"`
	Identity   cacheIdentity `json:"identity"`
	Required   bool          `json:"required"`
	Status     checkStatus   `json:"status"`
	Reason     string        `json:"reason,omitempty"`
	VerifiedAt *time.Time    `json:"verifiedAt,omitempty"`
}
type summary struct {
	Format            int           `json:"format"`
	Results           []checkResult `json:"results"`
	RequiredComplete  bool          `json:"requiredComplete"`
	Complete          bool          `json:"complete"`
	ExitCode          int           `json:"exitCode"`
	MatrixDigest      string        `json:"matrixDigest"`
	RequiredFreshPass bool          `json:"requiredFreshPass"`
	CacheUpdated      bool          `json:"cacheUpdated"`
}

func suiteSelection(selected, required string) (map[string]bool, map[string]bool, error) {
	parse := func(input string) (map[string]bool, error) {
		out := map[string]bool{}
		for _, name := range strings.Split(input, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if name != "contract" && name != "wrapper" && name != "native" {
				return nil, fmt.Errorf("unknown suite %q", name)
			}
			out[name] = true
		}
		return out, nil
	}
	s, err := parse(selected)
	if err != nil {
		return nil, nil, err
	}
	if len(s) == 0 {
		return nil, nil, fmt.Errorf("at least one suite is required")
	}
	if required == "" {
		required = selected
	}
	r, err := parse(required)
	if err != nil {
		return nil, nil, err
	}
	for name := range r {
		if !s[name] {
			return nil, nil, fmt.Errorf("required suite %q is not selected", name)
		}
	}
	return s, r, nil
}
func summarize(results []checkResult) summary {
	s := summary{Format: cacheFormat, Results: results, RequiredComplete: true, Complete: true, RequiredFreshPass: true}
	var matrix []cacheIdentity
	requiredCount := 0
	for _, r := range results {
		if !r.Selected && !r.Required {
			continue
		}
		if r.Required {
			requiredCount++
			matrix = append(matrix, r.Identity)
			if r.Status != statusPass {
				s.RequiredFreshPass = false
			}
		}
		if r.Status == statusFail {
			s.ExitCode = 1
		}
		if r.Status != statusPass && r.Status != statusCached {
			s.Complete = false
			if r.Required {
				s.RequiredComplete = false
			}
		}
	}
	if requiredCount == 0 {
		s.RequiredFreshPass = false
		s.RequiredComplete = false
	}
	if s.ExitCode == 0 && !s.RequiredComplete {
		s.ExitCode = 2
	}
	s.MatrixDigest = digestJSON(matrix)
	return s
}
func runMain(args []string) int {
	flags := flag.NewFlagSet("acp-compat-check", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	suites := flags.String("suites", envDefault("COMPAT_SUITES", "contract"), "selected suites: contract,wrapper,native")
	required := flags.String("required", os.Getenv("COMPAT_REQUIRED"), "required selected suites (default all selected)")
	live := flags.Bool("live", os.Getenv("COMPAT_LIVE") == "1", "allow selected credential-gated live model calls")
	refresh := flags.Bool("refresh", false, "retest instead of consuming cached evidence")
	output := flags.String("summary", envDefault("COMPAT_SUMMARY", "compat-summary.json"), "machine-readable summary path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	selected, requiredSuites, err := suiteSelection(*suites, *required)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, identityErr := runtimeIdentity()
	cache, _ := loadCache(cacheFilePath())
	dirty := false
	results := []checkResult{}
	if selected["contract"] {
		id := base
		id.Suite = "contract"
		id.Engine = "go-contract"
		version, versionErr := exec.Command("go", "env", "GOVERSION").Output()
		id.EngineVersion = strings.TrimSpace(string(version))
		id.ArtifactDigest = base.ContractDigest
		r := checkResult{Identity: id, Selected: true, Required: requiredSuites["contract"]}
		if identityErr != nil {
			r.Status = statusInfra
			r.Reason = identityErr.Error()
		} else if versionErr != nil {
			r.Status = statusInfra
			r.Reason = "query Go toolchain version: " + versionErr.Error()
		} else {
			r.Status, r.Reason = runContractCheck()
		}
		if r.Status == statusPass {
			now := time.Now().UTC()
			r.VerifiedAt = &now
		}
		results = append(results, r)
	} else {
		id := base
		id.Suite = "contract"
		id.Engine = "go-contract"
		results = append(results, checkResult{Identity: id, Status: statusSkipped, Reason: "suite not selected"})
	}
	for _, c := range availableChecks() {
		suite := "wrapper"
		if c.localAuth {
			suite = "native"
		}
		id := base
		id.Suite = suite
		id.Engine = c.name
		r := checkResult{Identity: id, Selected: selected[suite], Required: requiredSuites[suite]}
		switch {
		case !selected[suite]:
			r.Status = statusSkipped
			r.Reason = "suite not selected"
		case identityErr != nil:
			r.Status = statusInfra
			r.Reason = identityErr.Error()
		case c.localAuth && !c.canRun():
			r.Status = statusSkipped
			r.Reason = c.localBinary + " CLI unavailable on PATH"
		default:
			version, err := c.version()
			if err != nil {
				r.Status = statusInfra
				r.Reason = "query exact engine version: " + err.Error()
				break
			}
			r.Identity.EngineVersion = version
			artifact, err := c.artifactDigest(version)
			if err != nil {
				r.Status = statusInfra
				r.Reason = "resolve engine artifact identity: " + err.Error()
				break
			}
			r.Identity.ArtifactDigest = artifact
			cacheable := (!c.localAuth || os.Getenv("COMPAT_NATIVE_CONFIG_DIGEST") != "") && (gatewayConfigured() || os.Getenv("CODEX_CONFIG") == "")
			if cacheable && !*refresh && cache.hit(r.Identity, time.Now()) {
				entry := cache.Entries[r.Identity.key()]
				r.Status = statusCached
				r.Reason = "previous successful evidence for identical matrix; not re-executed"
				r.VerifiedAt = &entry.VerifiedAt
				break
			}
			if !*live {
				r.Status = statusSkipped
				r.Reason = "live suite requires --live (may incur provider charges)"
				break
			}
			if !c.canRun() {
				r.Status = statusSkipped
				r.Reason = "required provider credentials/gateway unavailable"
				break
			}
			build := func() (acp.Agent, map[string]any) {
				agent, meta := c.buildFunc()
				if !c.localAuth {
					for i, arg := range agent.Args {
						if arg == c.pkg {
							agent.Args[i] = c.pkg + "@" + version
						}
					}
				}
				return agent, meta
			}
			status, detail := runAgentCheck(build, c.name)
			r.Status = checkStatus(status)
			r.Reason = detail
			if r.Status == statusPass && r.Identity.complete() {
				now := time.Now().UTC()
				r.VerifiedAt = &now
				if cacheable {
					cache.Entries[r.Identity.key()] = cacheEntry{Identity: r.Identity, Status: statusPass, VerifiedAt: now, ExpiresAt: now.Add(cacheMaxAge), Source: envDefault("GITHUB_RUN_ID", "local")}
					dirty = true
				}
			}
		}
		r.Reason = redactDiagnostic(r.Reason)
		results = append(results, r)
	}
	s := summarize(results)
	if dirty {
		if err := saveCache(cacheFilePath(), cache); err != nil {
			fmt.Fprintf(os.Stderr, "cache persistence failed: %s\n", redactDiagnostic(err.Error()))
		} else {
			s.CacheUpdated = true
		}
	}
	for _, r := range results {
		fmt.Printf("%s/%s: %s (%s)\n", r.Identity.Suite, r.Identity.Engine, r.Status, r.Reason)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err == nil {
		err = atomicWrite(*output, append(data, '\n'))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "summary persistence failed:", err)
		if s.ExitCode == 0 {
			return 2
		}
	}
	fmt.Printf("Result: exit=%d requiredComplete=%t complete=%t\n", s.ExitCode, s.RequiredComplete, s.Complete)
	return s.ExitCode
}
func envDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func (c agentCheck) artifactDigest(version string) (string, error) {
	if c.localAuth {
		path, err := exec.LookPath(c.localBinary)
		if err != nil {
			return "", err
		}
		return fileDigest(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "npm", "view", c.pkg+"@"+version, "dist.integrity").Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	algorithm, encoded, ok := strings.Cut(value, "-")
	digest, decodeErr := base64.StdEncoding.DecodeString(encoded)
	if !ok || decodeErr != nil || !((algorithm == "sha512" && len(digest) == 64) || (algorithm == "sha256" && len(digest) == 32)) {
		return "", fmt.Errorf("npm did not provide a valid SHA integrity digest")
	}
	return value, nil
}
func runContractCheck() (checkStatus, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, "python3", "-c", "from jsonschema import Draft202012Validator").Run(); err != nil {
		return statusInfra, "independent schema prerequisites unavailable: Python 3 and jsonschema are required"
	}
	cmd := exec.CommandContext(ctx, "go", "test", "-count=1", "-timeout=4m", ".", "./harness", "./simulator")
	cmd.WaitDelay = 5 * time.Second
	if root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output(); err == nil {
		cmd.Dir = strings.TrimSpace(string(root))
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(name, "ACP_LIVE_") || strings.HasPrefix(name, "MAGPIE_") || name == "GOFLAGS" || name == "RUN_CC" || strings.Contains(upper, "API_KEY") || name == gatewayKeyEnv {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "RUN_CC=0")
	tail := &boundedTail{limit: 8192}
	cmd.Stdout = tail
	cmd.Stderr = tail
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return statusInfra, redactDiagnostic(ctx.Err().Error() + "\n" + tail.String())
		}
		if isInfrastructureError(err, "") {
			return statusInfra, redactDiagnostic(err.Error() + "\n" + tail.String())
		}
		return statusFail, redactDiagnostic(tail.String())
	}
	return statusPass, "credential-free runtime, harness and simulator contract tests passed"
}

type boundedTail struct {
	limit int
	data  []byte
}

func (t *boundedTail) Write(p []byte) (int, error) {
	n := len(p)
	t.data = append(t.data, p...)
	if len(t.data) > t.limit {
		t.data = t.data[len(t.data)-t.limit:]
	}
	return n, nil
}
func (t *boundedTail) String() string { return string(t.data) }

var _ io.Writer = (*boundedTail)(nil)
