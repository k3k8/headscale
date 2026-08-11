package hscontrol

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestConfigWatcher starts a watcher over a file seeded with content and
// returns the path plus a function that blocks until the callback has fired at
// least want times.
func newTestConfigWatcher(t *testing.T, content string) (string, func(want int32) bool) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	var calls atomic.Int32

	cw, err := newConfigWatcher(path, func() { calls.Add(1) })
	require.NoError(t, err)

	cw.debounce = 20 * time.Millisecond

	go cw.Run()
	t.Cleanup(cw.Close)

	waitFor := func(want int32) bool {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if calls.Load() >= want {
				return true
			}

			time.Sleep(10 * time.Millisecond)
		}

		return false
	}

	return path, waitFor
}

// TestConfigWatcherDetectsInPlaceWrite covers how a management UI updates the
// file: opened and rewritten in place.
func TestConfigWatcherDetectsInPlaceWrite(t *testing.T) {
	path, waitFor := newTestConfigWatcher(t, "dns:\n  magic_dns: true\n")

	require.NoError(t, os.WriteFile(path, []byte("dns:\n  magic_dns: false\n"), 0o600))

	assert.True(t, waitFor(1), "in-place write should have triggered a reload")
}

// TestConfigWatcherIgnoresIdenticalRewrite makes sure a rewrite that does not
// change the contents does not push a pointless update to every node.
func TestConfigWatcherIgnoresIdenticalRewrite(t *testing.T) {
	const content = "dns:\n  magic_dns: true\n"

	path, waitFor := newTestConfigWatcher(t, content)

	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	assert.False(t, waitFor(1), "identical contents should not have triggered a reload")
}

// TestConfigWatcherDetectsAtomicRename covers editors and tools that write a
// temporary file and rename it over the target, which detaches the inode the
// file watch was attached to.
func TestConfigWatcherDetectsAtomicRename(t *testing.T) {
	path, waitFor := newTestConfigWatcher(t, "dns:\n  magic_dns: true\n")

	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte("dns:\n  magic_dns: false\n"), 0o600))
	require.NoError(t, os.Rename(tmp, path))

	assert.True(t, waitFor(1), "atomic rename should have triggered a reload")
}

// TestConfigWatcherDetectsKubernetesConfigMapUpdate reproduces the layout
// kubelet publishes for a ConfigMap volume and the way it swaps revisions:
//
//	config.yaml -> ..data/config.yaml     (stable symlink)
//	..data      -> ..<timestamp>          (symlink, replaced by rename)
//
// The visible file never changes, so only the directory watch can see this.
// This is the case the whole feature exists for.
func TestConfigWatcherDetectsKubernetesConfigMapUpdate(t *testing.T) {
	dir := t.TempDir()

	writeRevision := func(name, content string) string {
		revDir := filepath.Join(dir, name)
		require.NoError(t, os.Mkdir(revDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(revDir, "config.yaml"), []byte(content), 0o600))

		return revDir
	}

	writeRevision("..2026_08_09_00_00_00.000000", "dns:\n  magic_dns: true\n")
	require.NoError(t, os.Symlink("..2026_08_09_00_00_00.000000", filepath.Join(dir, "..data")))
	require.NoError(t, os.Symlink(filepath.Join("..data", "config.yaml"), filepath.Join(dir, "config.yaml")))

	path := filepath.Join(dir, "config.yaml")

	var calls atomic.Int32

	cw, err := newConfigWatcher(path, func() { calls.Add(1) })
	require.NoError(t, err)

	cw.debounce = 20 * time.Millisecond

	go cw.Run()

	t.Cleanup(cw.Close)

	// Publish a new revision the way kubelet does: new directory, new "..data"
	// symlink written next to it, then renamed into place.
	writeRevision("..2026_08_09_01_00_00.000000", "dns:\n  magic_dns: false\n")

	staging := filepath.Join(dir, "..data_tmp")
	require.NoError(t, os.Symlink("..2026_08_09_01_00_00.000000", staging))
	require.NoError(t, os.Rename(staging, filepath.Join(dir, "..data")))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	assert.Positive(t, calls.Load(), "ConfigMap revision swap should have triggered a reload")

	// The file the watcher reads must now resolve to the new revision.
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "magic_dns: false")
}

// TestConfigWatcherIgnoresSiblingFiles guards against reloading on every write
// to the database or socket that shares /etc/headscale with the config file.
func TestConfigWatcherIgnoresSiblingFiles(t *testing.T) {
	path, waitFor := newTestConfigWatcher(t, "dns:\n  magic_dns: true\n")

	sibling := filepath.Join(filepath.Dir(path), "db.sqlite")
	for range 5 {
		require.NoError(t, os.WriteFile(sibling, []byte(time.Now().String()), 0o600))
		time.Sleep(5 * time.Millisecond)
	}

	assert.False(t, waitFor(1), "writes to sibling files should not have triggered a reload")
}

// TestConfigWatcherCoalescesBurst checks the debounce: a burst of writes should
// produce one reload, not one per write.
func TestConfigWatcherCoalescesBurst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("a: 0\n"), 0o600))

	var calls atomic.Int32

	cw, err := newConfigWatcher(path, func() { calls.Add(1) })
	require.NoError(t, err)

	cw.debounce = 150 * time.Millisecond

	go cw.Run()

	t.Cleanup(cw.Close)

	for i := range 10 {
		require.NoError(t, os.WriteFile(path, []byte("a: "+string(rune('0'+i))+"\n"), 0o600))
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(time.Second)

	assert.Equal(t, int32(1), calls.Load(), "a burst of writes should collapse into one reload")
}

// TestConfigWatcherCloseIsIdempotent makes sure the deferred Close in Serve
// cannot panic if Close runs twice.
func TestConfigWatcherCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("a: 0\n"), 0o600))

	cw, err := newConfigWatcher(path, func() {})
	require.NoError(t, err)

	go cw.Run()

	cw.Close()
	assert.NotPanics(t, cw.Close)
}

// TestNewConfigWatcherRejectsMissingFile checks that startup does not silently
// watch nothing.
func TestNewConfigWatcherRejectsMissingFile(t *testing.T) {
	_, err := newConfigWatcher("", func() {})
	require.Error(t, err)

	_, err = newConfigWatcher(filepath.Join(t.TempDir(), "nope", "config.yaml"), func() {})
	require.Error(t, err)
}
