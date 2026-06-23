// Package watch re-scans an IaC directory on .tf file changes and prints
// the cost delta to stdout on each save.
package watch

import (
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/appfolio/oof/internal/hcl"
	"github.com/appfolio/oof/internal/schema"
	"github.com/fsnotify/fsnotify"
)

// Options for watch mode.
type Options struct {
	ScanOptions hcl.Options
	Region      string
}

// Run watches path for .tf file changes and re-scans on each save.
// Blocks until the process receives SIGINT or SIGTERM.
func Run(path string, opts Options) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	if err := addDirs(watcher, path); err != nil {
		return err
	}

	// Initial scan.
	baseline, err := doScan(path, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initial scan error: %v\n", err)
	} else {
		printScanSummary(baseline, opts.Region)
	}

	fmt.Printf("Watching %s for .tf changes (Ctrl+C to stop)…\n\n", path)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	debounce := time.NewTimer(0)
	<-debounce.C // drain initial tick

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if !strings.HasSuffix(event.Name, ".tf") {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			debounce.Reset(300 * time.Millisecond)

		case <-debounce.C:
			updated, err := doScan(path, opts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "scan error: %v\n", err)
				continue
			}
			if baseline != nil {
				printDelta(baseline, updated, opts.Region)
			} else {
				printScanSummary(updated, opts.Region)
			}
			baseline = updated

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(os.Stderr, "watcher error: %v\n", err)

		case <-stop:
			fmt.Println("\nStopped.")
			return nil
		}
	}
}

func doScan(path string, opts Options) (*schema.Project, error) {
	proj, warnings, err := hcl.ParseDirWithOptions(path, opts.ScanOptions)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "  ⚠  %s\n", w)
	}
	return proj, nil
}

func printScanSummary(proj *schema.Project, region string) {
	label := region
	if label == "" {
		label = "us-east-1"
	}
	fmt.Printf("[%s] Total: %s/mo (%s, %d resources)\n",
		time.Now().Format("15:04:05"),
		schema.FormatUSD(proj.MonthlyCost()),
		label,
		len(proj.Resources),
	)
}

func printDelta(before, after *schema.Project, region string) {
	beforeCost := before.MonthlyCost()
	afterCost := after.MonthlyCost()
	delta := afterCost - beforeCost

	label := region
	if label == "" {
		label = "us-east-1"
	}

	sign := "+"
	if delta < 0 {
		sign = "-"
		delta = -delta
	} else if delta == 0 {
		fmt.Printf("[%s] No cost change (%s/mo)\n", time.Now().Format("15:04:05"), schema.FormatUSD(afterCost))
		return
	}

	fmt.Printf("[%s] %s%s/mo  (was %s/mo, now %s/mo, %s)\n",
		time.Now().Format("15:04:05"),
		sign,
		schema.FormatUSD(delta),
		schema.FormatUSD(beforeCost),
		schema.FormatUSD(afterCost),
		label,
	)

	// Show top changed resources.
	beforeMap := map[string]float64{}
	for _, r := range before.Resources {
		beforeMap[r.Name] = r.MonthlyCost()
	}
	type change struct {
		name  string
		delta float64
	}
	var changes []change
	for _, r := range after.Resources {
		d := r.MonthlyCost() - beforeMap[r.Name]
		if d != 0 {
			changes = append(changes, change{r.Name, d})
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		ai, aj := changes[i].delta, changes[j].delta
		if ai < 0 {
			ai = -ai
		}
		if aj < 0 {
			aj = -aj
		}
		return ai > aj
	})
	if len(changes) > 5 {
		changes = changes[:5]
	}
	if len(changes) > 0 {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, c := range changes {
			prefix := "+"
			v := c.delta
			if v < 0 {
				prefix = "-"
				v = -v
			}
			fmt.Fprintf(w, "    %s\t%s%s/mo\n", c.name, prefix, schema.FormatUSD(v))
		}
		w.Flush()
	}
	fmt.Println()
}

// addDirs recursively adds directories under root to the watcher.
func addDirs(watcher *fsnotify.Watcher, root string) error {
	// fsnotify watches directories; we add the root and subdirs.
	return addDir(watcher, root)
}

func addDir(watcher *fsnotify.Watcher, dir string) error {
	if err := watcher.Add(dir); err != nil {
		return fmt.Errorf("watch %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() != ".terraform" && e.Name() != ".git" {
			_ = addDir(watcher, dir+"/"+e.Name())
		}
	}
	return nil
}
