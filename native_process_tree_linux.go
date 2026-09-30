//go:build linux

package acpruntime

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type nativeProcessIdentity struct {
	pid, ppid, group int
	birth, state     string
}
type nativeOwnedProcess struct {
	identity nativeProcessIdentity
	process  *os.Process
}
type nativeProcessTree struct {
	group int
	owned map[int]nativeOwnedProcess
}

func nativeProcessIdentities() (map[int]nativeProcessIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := map[int]nativeProcessIdentity{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			return nil, err
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 20 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		group, _ := strconv.Atoi(fields[2])
		out[pid] = nativeProcessIdentity{pid: pid, ppid: ppid, group: group, state: fields[0], birth: fields[19]}
	}
	return out, nil
}
func (tree *nativeProcessTree) capture(pid int) error {
	tree.group = pid
	tree.owned = map[int]nativeOwnedProcess{}
	return tree.refresh(true)
}
func (tree *nativeProcessTree) refresh(initial bool) error {
	current, err := nativeProcessIdentities()
	if err != nil {
		return err
	}
	anchor := initial
	for pid, owned := range tree.owned {
		if now, exists := current[pid]; exists && now.birth == owned.identity.birth && now.group == tree.group {
			anchor = true
		}
	}
	candidates := map[int]bool{}
	for pid, identity := range current {
		if anchor && identity.group == tree.group {
			candidates[pid] = true
		}
	}
	// Also retain descendants observed leaving the group. A child that daemonizes
	// and is reparented before observation requires an external cgroup boundary.
	changed := true
	for changed {
		changed = false
		for pid, identity := range current {
			parent, ownedParent := tree.owned[identity.ppid]
			nowParent, parentExists := current[identity.ppid]
			ownedParent = ownedParent && parentExists && parent.identity.birth == nowParent.birth
			if (ownedParent || candidates[identity.ppid]) && !candidates[pid] {
				candidates[pid] = true
				changed = true
			}
		}
	}
	for pid := range candidates {
		if _, ok := tree.owned[pid]; ok {
			continue
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		// Linux Go uses pidfds where supported. Recheck birth after opening to avoid
		// retaining a reused PID during discovery, and never rediscover a retired PID.
		latest, err := nativeProcessIdentities()
		if err != nil {
			process.Release()
			return err
		}
		identity, ok := latest[pid]
		if !ok || identity.birth != current[pid].birth {
			process.Release()
			continue
		}
		tree.owned[pid] = nativeOwnedProcess{identity, process}
	}
	if !anchor {
		for _, identity := range current {
			if identity.group == tree.group && identity.state != "Z" && identity.state != "X" {
				return fmt.Errorf("unconfirmed process-group ownership; refusing a stale group signal")
			}
		}
	}
	return nil
}
func (tree *nativeProcessTree) signal(signal syscall.Signal) error {
	current, err := nativeProcessIdentities()
	if err != nil {
		return err
	}
	for pid, owned := range tree.owned {
		if now, ok := current[pid]; ok && now.birth == owned.identity.birth && now.state != "Z" && now.state != "X" {
			if err := owned.process.Signal(signal); err != nil && !os.IsNotExist(err) && err != os.ErrProcessDone {
				return err
			}
		}
	}
	return nil
}
func (tree *nativeProcessTree) alive() (bool, error) {
	if err := tree.refresh(false); err != nil {
		return true, err
	}
	current, err := nativeProcessIdentities()
	if err != nil {
		return true, err
	}
	for pid, owned := range tree.owned {
		if now, ok := current[pid]; ok && now.birth == owned.identity.birth && now.state != "Z" && now.state != "X" {
			return true, nil
		}
	}
	return false, nil
}
func (tree *nativeProcessTree) release() {
	for _, owned := range tree.owned {
		_ = owned.process.Release()
	}
	tree.owned = nil
}
