package tools

import (
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func writeLimit(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.max")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func useLimitPaths(t *testing.T, paths ...string) {
	t.Helper()
	orig := cgroupLimitPaths
	cgroupLimitPaths = paths
	t.Cleanup(func() { cgroupLimitPaths = orig })
}

func TestCgroupMemoryLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		contents string
		want     int64
		wantOK   bool
	}{
		"cgroup v2 bytes":      {"21474836480\n", 21474836480, true},
		"cgroup v2 unlimited":  {"max\n", 0, false},
		"cgroup v1 unlimited":  {"9223372036854771712", 0, false},
		"zero is not a limit":  {"0", 0, false},
		"unparseable contents": {"not a number", 0, false},
	} {
		t.Run(name, func(t *testing.T) {
			useLimitPaths(t, writeLimit(t, tc.contents))
			got, ok := cgroupMemoryLimit()
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("cgroupMemoryLimit() = %d, %v; want %d, %v", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// A missing first path (cgroup v2 on a v1 host, or neither on a bare machine)
// must fall through rather than give up.
func TestCgroupMemoryLimitFallsThroughToTheNextPath(t *testing.T) {
	useLimitPaths(t, filepath.Join(t.TempDir(), "absent"), writeLimit(t, "1073741824"))
	got, ok := cgroupMemoryLimit()
	if !ok || got != 1073741824 {
		t.Errorf("cgroupMemoryLimit() = %d, %v; want 1073741824, true", got, ok)
	}
}

func TestApplyMemoryLimitLeavesHeadroom(t *testing.T) {
	useLimitPaths(t, writeLimit(t, "21474836480")) // 20 GiB, the container that was being OOM-killed
	os.Unsetenv("GOMEMLIMIT")
	// SetMemoryLimit is process-wide; put the runtime back where it was so the
	// rest of the package's tests are not run under a limit.
	t.Cleanup(func() { debug.SetMemoryLimit(math.MaxInt64) })

	soft, container, ok := ApplyMemoryLimit()
	if !ok {
		t.Fatal("ApplyMemoryLimit() did not apply a limit")
	}
	if container != 21474836480 {
		t.Errorf("container = %d, want 21474836480", container)
	}
	if want := int64(float64(21474836480) * memLimitHeadroom); soft != want {
		t.Errorf("soft = %d, want %d", soft, want)
	}
	if soft >= container {
		t.Error("the soft limit must sit below the container limit, not at it")
	}
}

// An operator who set GOMEMLIMIT has already told the runtime what to do; the
// cgroup must not override that number.
func TestApplyMemoryLimitDefersToTheEnvironment(t *testing.T) {
	useLimitPaths(t, writeLimit(t, "21474836480"))
	t.Setenv("GOMEMLIMIT", "8GiB")

	if _, _, ok := ApplyMemoryLimit(); ok {
		t.Error("ApplyMemoryLimit() overrode an explicit GOMEMLIMIT")
	}
}

// On a bare host there is no limit to apply and nothing should be logged.
func TestApplyMemoryLimitWithoutACgroupLimit(t *testing.T) {
	useLimitPaths(t, filepath.Join(t.TempDir(), "absent"))
	os.Unsetenv("GOMEMLIMIT")

	if _, _, ok := ApplyMemoryLimit(); ok {
		t.Error("ApplyMemoryLimit() applied a limit with no cgroup limit to read")
	}
}
