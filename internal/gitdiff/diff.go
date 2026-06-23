// Package gitdiff implements cost diff between two git refs using worktrees.
package gitdiff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/appfolio/oof/internal/hcl"
	"github.com/appfolio/oof/internal/schema"
)

// ResourceDelta describes the cost change for one resource.
type ResourceDelta struct {
	Address  string
	HeadCost float64
	BaseCost float64
	IsNew    bool // present in head but not base
	IsGone   bool // present in base but not head
}

func (d ResourceDelta) Delta() float64 {
	return d.HeadCost - d.BaseCost
}

// Result is the full cost diff between two refs.
type Result struct {
	HeadTotal float64
	BaseTotal float64
	Deltas    []ResourceDelta
}

func (r Result) TotalDelta() float64 {
	return r.HeadTotal - r.BaseTotal
}

// Options for computing a cost diff.
type DiffOptions struct {
	BaseRef      string // git ref to compare against; auto-detected from branch if ""
	ScanOptions  hcl.Options
}

// Run computes the cost diff for the IaC directory at path between HEAD
// and opts.BaseRef (or the upstream branch if BaseRef is "").
func Run(path string, opts DiffOptions) (*Result, error) {
	baseRef := opts.BaseRef
	if baseRef == "" {
		var err error
		baseRef, err = detectBaseRef(path)
		if err != nil {
			return nil, fmt.Errorf("could not detect base ref: %w", err)
		}
	}

	// Scan HEAD (current working tree).
	headProj, _, err := hcl.ParseDirWithOptions(path, opts.ScanOptions)
	if err != nil {
		return nil, fmt.Errorf("scan HEAD: %w", err)
	}

	// Create a temporary worktree at base ref.
	worktreeDir, cleanup, err := createWorktree(path, baseRef)
	if err != nil {
		return nil, fmt.Errorf("create worktree for %s: %w", baseRef, err)
	}
	defer cleanup()

	baseProj, _, err := hcl.ParseDirWithOptions(worktreeDir, opts.ScanOptions)
	if err != nil {
		return nil, fmt.Errorf("scan base: %w", err)
	}

	return buildResult(headProj, baseProj), nil
}

func detectBaseRef(path string) (string, error) {
	// Try upstream tracking branch first.
	cmd := exec.Command("git", "-C", path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	out, err := cmd.Output()
	if err == nil {
		ref := strings.TrimSpace(string(out))
		if ref != "" && ref != "HEAD" {
			return ref, nil
		}
	}

	// Fall back to origin/main or origin/master.
	for _, candidate := range []string{"origin/main", "origin/master"} {
		check := exec.Command("git", "-C", path, "rev-parse", "--verify", candidate)
		if check.Run() == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("could not auto-detect base branch; pass --base-ref explicitly")
}

func createWorktree(repoPath, ref string) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "oof-wt-*")
	if err != nil {
		return "", nil, err
	}

	cmd := exec.Command("git", "-C", repoPath, "worktree", "add", "--detach", tmpDir, ref)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
	}

	// The IaC files live in the subdirectory within the worktree that mirrors
	// the path relative to the git root.
	gitRoot, err := gitRootOf(repoPath)
	if err != nil {
		_ = removeWorktree(repoPath, tmpDir)
		return "", nil, err
	}

	rel, err := filepath.Rel(gitRoot, repoPath)
	if err != nil {
		_ = removeWorktree(repoPath, tmpDir)
		return "", nil, err
	}

	scanDir := filepath.Join(tmpDir, rel)
	if _, err := os.Stat(scanDir); err != nil {
		// If the sub-path doesn't exist in the base ref, scan the worktree root.
		scanDir = tmpDir
	}

	cleanup := func() {
		_ = removeWorktree(repoPath, tmpDir)
		os.RemoveAll(tmpDir)
	}
	return scanDir, cleanup, nil
}

func removeWorktree(repoPath, worktreePath string) error {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "remove", "--force", worktreePath)
	return cmd.Run()
}

func gitRootOf(path string) (string, error) {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func buildResult(head, base *schema.Project) *Result {
	headMap := map[string]float64{}
	for _, r := range head.Resources {
		headMap[r.Name] = r.MonthlyCost()
	}
	baseMap := map[string]float64{}
	for _, r := range base.Resources {
		baseMap[r.Name] = r.MonthlyCost()
	}

	seen := map[string]bool{}
	var deltas []ResourceDelta

	for addr, hc := range headMap {
		seen[addr] = true
		bc := baseMap[addr]
		if hc != bc {
			deltas = append(deltas, ResourceDelta{
				Address:  addr,
				HeadCost: hc,
				BaseCost: bc,
				IsNew:    bc == 0 && !inBaseKeys(addr, baseMap),
			})
		}
	}
	for addr, bc := range baseMap {
		if seen[addr] {
			continue
		}
		deltas = append(deltas, ResourceDelta{
			Address:  addr,
			HeadCost: 0,
			BaseCost: bc,
			IsGone:   true,
		})
	}

	return &Result{
		HeadTotal: head.MonthlyCost(),
		BaseTotal: base.MonthlyCost(),
		Deltas:    deltas,
	}
}

func inBaseKeys(addr string, baseMap map[string]float64) bool {
	_, ok := baseMap[addr]
	return ok
}
