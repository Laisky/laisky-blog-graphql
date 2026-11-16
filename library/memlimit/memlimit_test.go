package memlimit

import (
	"math"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// file returns a map file with content.
func file(content string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(content)} }

// TestDetectCgroupLimits covers the cgroup layouts a container sees.
func TestDetectCgroupLimits(t *testing.T) {
	cases := map[string]struct {
		fs     fstest.MapFS
		want   int64
		source string
	}{
		"v2 private namespace": {fstest.MapFS{
			"proc/self/cgroup":         file("0::/\n"),
			"sys/fs/cgroup/memory.max": file("2147483648\n"),
		}, 2 << 30, SourceCgroupV2},
		"v2 host namespace, tightest ancestor wins": {fstest.MapFS{
			"proc/self/cgroup":                                       file("0::/system.slice/docker-abc.scope\n"),
			"sys/fs/cgroup/memory.max":                               file("max\n"),
			"sys/fs/cgroup/system.slice/memory.max":                  file("8589934592\n"),
			"sys/fs/cgroup/system.slice/docker-abc.scope/memory.max": file("1073741824\n"),
		}, 1 << 30, SourceCgroupV2},
		"v2 unlimited": {fstest.MapFS{
			"proc/self/cgroup":         file("0::/user.slice\n"),
			"sys/fs/cgroup/memory.max": file("max\n"),
		}, 0, ""},
		"v1": {fstest.MapFS{
			"proc/self/cgroup":                           file("12:cpu,cpuacct:/docker/abc\n4:memory:/docker/abc\n"),
			"sys/fs/cgroup/memory/memory.limit_in_bytes": file("536870912\n"),
		}, 512 << 20, SourceCgroupV1},
		"v1 unlimited": {fstest.MapFS{
			"proc/self/cgroup":                           file("4:memory:/\n"),
			"sys/fs/cgroup/memory/memory.limit_in_bytes": file("9223372036854771712\n"),
		}, 0, ""},
		"no cgroup file":   {fstest.MapFS{}, 0, ""},
		"garbage contents": {fstest.MapFS{"proc/self/cgroup": file("0::/\n"), "sys/fs/cgroup/memory.max": file("lots\n")}, 0, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := detect(tc.fs, math.MaxInt64)
			require.Equal(t, tc.want, got.Container)
			require.Equal(t, tc.source, got.ContainerSource)
			require.Zero(t, got.GoSoftLimit, "math.MaxInt64 means no soft limit")
		})
	}
}

// TestLimitsEffective: the tighter of the Go soft limit and the container
// limit applies.
func TestLimitsEffective(t *testing.T) {
	_, _, ok := Limits{}.Effective()
	require.False(t, ok)

	v, src, ok := Limits{Container: 2 << 30, ContainerSource: SourceCgroupV2}.Effective()
	require.True(t, ok)
	require.Equal(t, int64(2<<30), v)
	require.Equal(t, SourceCgroupV2, src)

	v, src, _ = Limits{GoSoftLimit: 1 << 30, Container: 2 << 30, ContainerSource: SourceCgroupV2}.Effective()
	require.Equal(t, int64(1<<30), v)
	require.Equal(t, SourceGoSoftLimit, src)

	v, src, _ = Limits{GoSoftLimit: 4 << 30, Container: 2 << 30, ContainerSource: SourceCgroupV2}.Effective()
	require.Equal(t, int64(2<<30), v)
	require.Equal(t, SourceCgroupV2, src)

	require.Equal(t, int64(3<<30), detect(fstest.MapFS{}, 3<<30).GoSoftLimit)
}

// TestSetGoSoftLimitFromContainer: the soft limit is derived from the
// container limit only when nobody chose one.
func TestSetGoSoftLimitFromContainer(t *testing.T) {
	var set int64
	setter := func(v int64) int64 { set = v; return math.MaxInt64 }
	noEnv := func(string) (string, bool) { return "", false }
	container := Limits{Container: 1 << 30, ContainerSource: SourceCgroupV2}

	ratio := DefaultSoftLimitRatio
	want := int64(float64(1<<30) * ratio)
	require.Equal(t, want, setGoSoftLimitFromContainer(noEnv, container, setter, ratio))
	require.Equal(t, want, set)

	set = 0
	off := func(string) (string, bool) { return "off", true }
	require.Zero(t, setGoSoftLimitFromContainer(off, container, setter, DefaultSoftLimitRatio), "GOMEMLIMIT=off is a choice")
	require.Zero(t, setGoSoftLimitFromContainer(noEnv, Limits{GoSoftLimit: 1 << 29, Container: 1 << 30}, setter, DefaultSoftLimitRatio))
	require.Zero(t, setGoSoftLimitFromContainer(noEnv, Limits{}, setter, DefaultSoftLimitRatio))
	require.Zero(t, set, "nothing set")
}
