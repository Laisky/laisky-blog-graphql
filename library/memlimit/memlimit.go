// Package memlimit discovers the memory limits the process runs under: the Go
// runtime's soft memory limit (GOMEMLIMIT or debug.SetMemoryLimit) and the
// cgroup (container) memory limit. Go 1.27 does not derive the soft limit from
// the cgroup by itself (golang/go#75164 is still a proposal), so callers that
// size memory budgets read both here. See docs/ref/image_memory_envelope_20261115.md.
package memlimit

import (
	"bufio"
	"bytes"
	"io/fs"
	"math"
	"os"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
)

// Source names of a detected limit.
const (
	SourceGoSoftLimit = "GOMEMLIMIT"
	SourceCgroupV2    = "cgroup v2 memory.max"
	SourceCgroupV1    = "cgroup v1 memory.limit_in_bytes"
)

// cgroupV1Unlimited is the threshold above which cgroup v1 reports "no
// limit" (the kernel reports a page-aligned value near MaxInt64).
const cgroupV1Unlimited = int64(1) << 62

// Limits are the memory limits that apply to the process. Zero means none.
type Limits struct {
	// GoSoftLimit is the Go runtime's soft memory limit.
	GoSoftLimit int64
	// Container is the tightest cgroup memory limit of the process's cgroup
	// and its ancestors.
	Container int64
	// ContainerSource names the cgroup file Container was read from.
	ContainerSource string
}

// Effective returns the tightest limit and its source; ok is false when no
// limit applies.
func (l Limits) Effective() (bytes int64, source string, ok bool) {
	switch {
	case l.GoSoftLimit > 0 && (l.Container <= 0 || l.GoSoftLimit <= l.Container):
		return l.GoSoftLimit, SourceGoSoftLimit, true
	case l.Container > 0:
		return l.Container, l.ContainerSource, true
	default:
		return 0, "", false
	}
}

// Detect returns the limits of the running process.
func Detect() Limits {
	return detect(os.DirFS("/"), debug.SetMemoryLimit(-1))
}

// detect reads the cgroup limits from fsys (a view of the root file system)
// and takes softLimit as the Go runtime's current soft limit.
func detect(fsys fs.FS, softLimit int64) Limits {
	var out Limits
	if softLimit > 0 && softLimit < math.MaxInt64 {
		out.GoSoftLimit = softLimit
	}
	out.Container, out.ContainerSource = cgroupLimit(fsys)
	return out
}

// cgroupLimit returns the tightest memory limit along the process's cgroup
// path, trying cgroup v2 first and then the v1 memory controller.
func cgroupLimit(fsys fs.FS) (int64, string) {
	raw, err := fs.ReadFile(fsys, "proc/self/cgroup")
	if err != nil {
		return 0, ""
	}
	var v2Path, v1Path string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		// hierarchy-ID:controller-list:cgroup-path
		parts := strings.SplitN(scanner.Text(), ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			v2Path = parts[2]
		case containsController(parts[1], "memory"):
			v1Path = parts[2]
		}
	}
	if v2Path != "" {
		if limit := tightestAlongPath(fsys, "sys/fs/cgroup", v2Path, "memory.max", parseCgroupV2); limit > 0 {
			return limit, SourceCgroupV2
		}
	}
	if v1Path != "" {
		if limit := tightestAlongPath(fsys, "sys/fs/cgroup/memory", v1Path, "memory.limit_in_bytes", parseCgroupV1); limit > 0 {
			return limit, SourceCgroupV1
		}
	}
	return 0, ""
}

// containsController reports whether the comma-separated list has name.
func containsController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// tightestAlongPath reads file in mount/cgroupPath and every ancestor up to
// mount itself and returns the smallest limit found (0 when none). Missing
// directories are skipped: inside a cgroup namespace the process's path is
// not visible below the mount, whose own file is then the container's limit.
func tightestAlongPath(fsys fs.FS, mount, cgroupPath, file string, parse func(string) int64) int64 {
	var tightest int64
	dir := path.Clean("/" + cgroupPath)
	for {
		raw, err := fs.ReadFile(fsys, path.Join(mount, dir, file))
		if err == nil {
			if limit := parse(strings.TrimSpace(string(raw))); limit > 0 && (tightest == 0 || limit < tightest) {
				tightest = limit
			}
		}
		if dir == "/" {
			return tightest
		}
		dir = path.Dir(dir)
	}
}

// parseCgroupV2 parses memory.max ("max" means no limit).
func parseCgroupV2(raw string) int64 {
	if raw == "max" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// parseCgroupV1 parses memory.limit_in_bytes (a huge value means no limit).
func parseCgroupV1(raw string) int64 {
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 || v >= cgroupV1Unlimited {
		return 0
	}
	return v
}

// DefaultSoftLimitRatio is the part of the container memory limit
// SetGoSoftLimitFromContainer gives the Go runtime: the Go GC guide leaves
// 5-10% for memory the runtime does not manage.
const DefaultSoftLimitRatio = 0.9

// SetGoSoftLimitFromContainer sets the Go runtime's soft memory limit to
// ratio of the container memory limit, as golang/go#75164 proposes the
// runtime itself should. Without it the collector lets the heap grow to about
// twice the live set before collecting (GOGC=100), so a burst can be killed by
// the container long before it runs out of live memory. It does nothing, and
// returns 0, when GOMEMLIMIT is present in the environment (any value,
// including "off"), a soft limit is already in effect, or no container limit
// applies; otherwise it returns the limit it set.
func SetGoSoftLimitFromContainer(ratio float64) int64 {
	return setGoSoftLimitFromContainer(os.LookupEnv, Detect(), debug.SetMemoryLimit, ratio)
}

// setGoSoftLimitFromContainer is SetGoSoftLimitFromContainer with its
// environment, limits and runtime setter injected.
func setGoSoftLimitFromContainer(lookupEnv func(string) (string, bool), limits Limits, set func(int64) int64, ratio float64) int64 {
	if _, present := lookupEnv("GOMEMLIMIT"); present {
		return 0
	}
	if limits.GoSoftLimit > 0 || limits.Container <= 0 || ratio <= 0 || ratio > 1 {
		return 0
	}
	limit := int64(float64(limits.Container) * ratio)
	set(limit)
	return limit
}
