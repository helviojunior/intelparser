package tools

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// memLimitHeadroom is the fraction of the container's memory limit handed to
// the Go heap as a soft limit. The remainder covers what the cgroup counts but
// the heap limit does not: goroutine stacks, the runtime's own metadata, and
// the off-heap buffers the HTTP stack holds while requests are in flight.
const memLimitHeadroom = 0.85

// memLimitNoLimit is the floor above which a reported cgroup limit means "no
// limit" rather than a real cap. cgroup v1 reports PAGE_COUNTER_MAX
// (9223372036854771712) when unconstrained, and no real container is capped at
// a pebibyte.
const memLimitNoLimit = int64(1) << 50

// cgroupLimitPaths are the files the memory limit is read from, cgroup v2
// first. Both report a byte count; v2 uses the literal "max" when there is no
// limit, v1 the sentinel above.
var cgroupLimitPaths = []string{
	"/sys/fs/cgroup/memory.max",                   // cgroup v2 (unified)
	"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
}

// ApplyMemoryLimit points the Go garbage collector at the memory limit of the
// cgroup this process runs in, and reports the soft limit it set together with
// the container limit it was derived from.
//
// Without it the collector has no idea a hard cap exists: at the default
// GOGC=100 it lets the heap grow to twice the live set before collecting, so a
// live set of half the container's memory is enough for the kernel OOM killer
// to end the run — which is exactly what happens on a long migration, where
// the live set is large and the allocation rate is enormous. A soft limit makes
// the collector run more often as the heap approaches the cap instead, trading
// CPU for staying alive.
//
// It is a soft limit by design: the runtime will exceed it rather than
// deadlock if the live heap genuinely does not fit, so this cannot mask a real
// leak, only absorb the collector's headroom.
//
// An explicit GOMEMLIMIT in the environment always wins — the runtime has
// already applied it and an operator's number should not be second-guessed.
// Returns ok=false when there is nothing to do: no cgroup limit (a bare host, a
// non-Linux platform), or GOMEMLIMIT already set.
func ApplyMemoryLimit() (soft int64, container int64, ok bool) {
	if _, set := os.LookupEnv("GOMEMLIMIT"); set {
		return 0, 0, false
	}

	container, ok = cgroupMemoryLimit()
	if !ok {
		return 0, 0, false
	}

	soft = int64(float64(container) * memLimitHeadroom)
	if soft <= 0 {
		return 0, 0, false
	}

	debug.SetMemoryLimit(soft)
	return soft, container, true
}

// cgroupMemoryLimit returns the memory limit of the cgroup this process belongs
// to, or ok=false when it is unconstrained or unreadable.
func cgroupMemoryLimit() (int64, bool) {
	for _, path := range cgroupLimitPaths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		if v == "max" {
			return 0, false
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 || n >= memLimitNoLimit {
			continue
		}
		return n, true
	}
	return 0, false
}
