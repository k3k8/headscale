package hscontrol

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"
)

// defaultConfigWatchDebounce is how long to wait for a burst of filesystem
// events to settle before re-reading the configuration file. Editors and
// container runtimes both produce several events per logical change.
const defaultConfigWatchDebounce = 250 * time.Millisecond

// k8sAtomicWriterPrefix is the prefix Kubernetes gives the bookkeeping entries
// inside a projected volume ("..data", "..2026_08_09_17_00_00.123456"). The
// visible config file is a symlink that never changes; only these entries do,
// which is why the parent directory has to be watched as well.
const k8sAtomicWriterPrefix = ".."

// configWatcher watches a configuration file and invokes a callback whenever
// its contents actually change.
//
// It watches both the file and its parent directory, because the two ways a
// configuration file gets updated in practice look completely different to
// inotify:
//
//   - Written in place (a management UI, or an editor): events land on the
//     file itself.
//   - Replaced atomically (Kubernetes ConfigMap volumes, and editors that
//     write-then-rename): the watched inode is detached and no further events
//     arrive for it. The rename is only visible as an event in the directory.
//
// Contents are hashed, so the callback only fires for real changes and the
// noise from watching a directory that also holds a database or socket is
// filtered out.
type configWatcher struct {
	path     string
	dir      string
	watcher  *fsnotify.Watcher
	onChange func()
	debounce time.Duration

	hash      [32]byte
	closeCh   chan struct{}
	closeOnce sync.Once
}

// newConfigWatcher starts watching the configuration file at path. onChange is
// invoked from [configWatcher.Run]'s goroutine whenever the file's contents
// change.
func newConfigWatcher(path string, onChange func()) (*configWatcher, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty configuration file path", os.ErrInvalid)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving configuration file path: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating watcher: %w", err)
	}

	cw := &configWatcher{
		path:     abs,
		dir:      filepath.Dir(abs),
		watcher:  watcher,
		onChange: onChange,
		debounce: defaultConfigWatchDebounce,
		closeCh:  make(chan struct{}),
	}

	// Seed the hash so the first spurious event does not look like a change.
	if hash, err := hashFile(cw.path); err == nil {
		cw.hash = hash
	}

	// The directory watch is the one that must succeed: it is what survives an
	// atomic replace. The file watch is an optimisation for in-place writes.
	if err := watcher.Add(cw.dir); err != nil {
		watcher.Close()
		return nil, fmt.Errorf("watching configuration directory %s: %w", cw.dir, err)
	}

	if err := watcher.Add(cw.path); err != nil {
		log.Debug().Err(err).Str("path", cw.path).
			Msg("could not watch configuration file directly, relying on directory watch")
	}

	return cw, nil
}

// relevant reports whether an event could mean the configuration file changed.
func (c *configWatcher) relevant(name string) bool {
	if name == c.path {
		return true
	}

	// Kubernetes swaps the "..data" symlink to publish a new ConfigMap
	// revision; the file itself is a symlink into it and never changes.
	return filepath.Dir(name) == c.dir &&
		strings.HasPrefix(filepath.Base(name), k8sAtomicWriterPrefix)
}

// Run processes filesystem events until [configWatcher.Close] is called.
func (c *configWatcher) Run() {
	// Stopped timer, reset on every relevant event so a burst collapses into
	// a single reload.
	timer := time.NewTimer(c.debounce)
	if !timer.Stop() {
		<-timer.C
	}

	defer timer.Stop()

	for {
		select {
		case <-c.closeCh:
			return

		case event, ok := <-c.watcher.Events:
			if !ok {
				return
			}

			if !c.relevant(event.Name) {
				continue
			}

			log.Trace().Str("path", event.Name).Str("op", event.Op.String()).
				Msg("configuration file watcher received event")

			// An atomic replace detaches the inode we were watching, so the
			// file watch has to be re-established. The directory watch keeps
			// working regardless, so a failure here is not fatal.
			if event.Has(fsnotify.Remove) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Create) {
				_ = c.watcher.Remove(c.path)
				if err := c.watcher.Add(c.path); err != nil {
					log.Trace().Err(err).Str("path", c.path).
						Msg("could not re-watch configuration file, directory watch still active")
				}
			}

			timer.Reset(c.debounce)

		case <-timer.C:
			c.maybeReload()

		case err, ok := <-c.watcher.Errors:
			if !ok {
				return
			}

			log.Error().Err(err).Msg("configuration file watcher error")
		}
	}
}

// maybeReload re-hashes the configuration file and invokes the callback if the
// contents changed.
func (c *configWatcher) maybeReload() {
	hash, err := hashFile(c.path)
	if err != nil {
		log.Error().Err(err).Str("path", c.path).Msg("reading configuration file after change")
		return
	}

	// A zero-length read usually means we caught a write halfway through; the
	// completing write produces another event.
	if hash == ([32]byte{}) {
		return
	}

	if hash == c.hash {
		log.Trace().Msg("configuration file unchanged, skipping reload")
		return
	}

	c.hash = hash

	log.Info().Str("path", c.path).Msg("configuration file changed, reloading")
	c.onChange()
}

// Close stops the watcher.
func (c *configWatcher) Close() {
	c.closeOnce.Do(func() {
		close(c.closeCh)
		c.watcher.Close()
	})
}

// hashFile returns the SHA-256 of the file's contents, or the zero hash if the
// file is empty.
func hashFile(path string) ([32]byte, error) {
	var zero [32]byte

	b, err := os.ReadFile(path)
	if err != nil {
		return zero, fmt.Errorf("reading %s: %w", path, err)
	}

	if len(b) == 0 {
		return zero, nil
	}

	return sha256.Sum256(b), nil
}
