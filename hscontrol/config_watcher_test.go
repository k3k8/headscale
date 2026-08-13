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

const (
	watchTestDebounce = 20 * time.Millisecond
	watchTestTimeout  = 5 * time.Second
	watchTestTick     = 10 * time.Millisecond
)

// startWatcher starts a watcher over path and returns a counter of how many
// times the reload callback has fired.
func startWatcher(t *testing.T, path string, debounce time.Duration) *atomic.Int32 {
	t.Helper()

	var calls atomic.Int32

	cw, err := newConfigWatcher(path, func() { calls.Add(1) })
	require.NoError(t, err)

	cw.debounce = debounce
	go cw.Run()

	t.Cleanup(cw.Close)

	return &calls
}

// newTestConfigWatcher seeds a config file, starts a watcher over it and
// returns the path plus the reload counter.
func newTestConfigWatcher(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("dns:\n  magic_dns: true\n"), 0o600))

	return path, startWatcher(t, path, watchTestDebounce)
}

// requireReloaded waits for at least one reload.
func requireReloaded(t *testing.T, calls *atomic.Int32, msg string) {
	t.Helper()

	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Positive(c, calls.Load())
	}, watchTestTimeout, watchTestTick, msg)
}

// requireNeverReloads asserts that no reload happens within the settle window.
func requireNeverReloads(t *testing.T, calls *atomic.Int32, msg string) {
	t.Helper()

	assert.Never(t, func() bool {
		return calls.Load() > 0
	}, time.Second, watchTestTick, msg)
}

// TestConfigWatcherDetectsInPlaceWrite covers how a management UI updates the
// file: opened and rewritten in place.
func TestConfigWatcherDetectsInPlaceWrite(t *testing.T) {
	path, calls := newTestConfigWatcher(t)

	require.NoError(t, os.WriteFile(path, []byte("dns:\n  magic_dns: false\n"), 0o600))

	requireReloaded(t, calls, "in-place write should have triggered a reload")
}

// TestConfigWatcherIgnoresIdenticalRewrite makes sure a rewrite that does not
// change the contents does not push a pointless update to every node.
func TestConfigWatcherIgnoresIdenticalRewrite(t *testing.T) {
	path, calls := newTestConfigWatcher(t)

	require.NoError(t, os.WriteFile(path, []byte("dns:\n  magic_dns: true\n"), 0o600))

	requireNeverReloads(t, calls, "identical contents should not have triggered a reload")
}

// TestConfigWatcherDetectsAtomicRename covers editors and tools that write a
// temporary file and rename it over the target, which detaches the inode the
// file watch was attached to.
func TestConfigWatcherDetectsAtomicRename(t *testing.T) {
	path, calls := newTestConfigWatcher(t)

	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte("dns:\n  magic_dns: false\n"), 0o600))
	require.NoError(t, os.Rename(tmp, path))

	requireReloaded(t, calls, "atomic rename should have triggered a reload")
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

	writeRevision := func(name, content string) {
		t.Helper()

		revDir := filepath.Join(dir, name)
		require.NoError(t, os.Mkdir(revDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(revDir, "config.yaml"), []byte(content), 0o600))
	}

	writeRevision("..2026_08_09_00_00_00.000000", "dns:\n  magic_dns: true\n")
	require.NoError(t, os.Symlink("..2026_08_09_00_00_00.000000", filepath.Join(dir, "..data")))
	require.NoError(t, os.Symlink(filepath.Join("..data", "config.yaml"), filepath.Join(dir, "config.yaml")))

	path := filepath.Join(dir, "config.yaml")
	calls := startWatcher(t, path, watchTestDebounce)

	// Publish a new revision the way kubelet does: new directory, new "..data"
	// symlink written next to it, then renamed into place.
	writeRevision("..2026_08_09_01_00_00.000000", "dns:\n  magic_dns: false\n")

	staging := filepath.Join(dir, "..data_tmp")
	require.NoError(t, os.Symlink("..2026_08_09_01_00_00.000000", staging))
	require.NoError(t, os.Rename(staging, filepath.Join(dir, "..data")))

	requireReloaded(t, calls, "ConfigMap revision swap should have triggered a reload")

	// The file the watcher reads must now resolve to the new revision.
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(b), "magic_dns: false")
}

// TestConfigWatcherIgnoresSiblingFiles guards against reloading on every write
// to the database or socket that shares /etc/headscale with the config file.
func TestConfigWatcherIgnoresSiblingFiles(t *testing.T) {
	path, calls := newTestConfigWatcher(t)

	sibling := filepath.Join(filepath.Dir(path), "db.sqlite")
	for i := range 5 {
		require.NoError(t, os.WriteFile(sibling, []byte{byte('0' + i)}, 0o600))
	}

	requireNeverReloads(t, calls, "writes to sibling files should not have triggered a reload")
}

// TestConfigWatcherCoalescesBurst checks the debounce: a burst of writes should
// produce one reload, not one per write.
func TestConfigWatcherCoalescesBurst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("a: 0\n"), 0o600))

	calls := startWatcher(t, path, 150*time.Millisecond)

	for i := range 10 {
		require.NoError(t, os.WriteFile(path, []byte{'a', ':', ' ', byte('0' + i), '\n'}, 0o600))
	}

	requireReloaded(t, calls, "the burst should have triggered a reload")

	// And exactly one: the debounce must not let each write through.
	assert.Never(t, func() bool {
		return calls.Load() > 1
	}, time.Second, watchTestTick, "a burst of writes should collapse into one reload")
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
