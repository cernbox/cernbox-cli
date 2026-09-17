package auth

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cacheVersion lets a future format change be detected rather than
// mis-parsed. An unrecognised version is treated as an empty cache.
const cacheVersion = 1

// Cache stores tokens between invocations so that a shell loop does not
// re-authenticate on every command.
//
// It deliberately lives in node-local /tmp rather than the user's home
// directory. On lxplus, home is a network filesystem shared across every node
// in the cluster; a long-lived bearer token sitting there has a materially
// wider exposure than one in /tmp, whose lifetime and reach match the Kerberos
// credential cache the token was derived from.
type Cache struct {
	path string
	mu   sync.Mutex
}

type cacheFile struct {
	Version int               `json:"version"`
	Entries map[string]*Token `json:"entries"`
}

// DefaultCachePath returns the token cache location: $CERNBOX_TOKEN_CACHE if
// set, otherwise /tmp/cernbox_cc_<uid>, mirroring where KRB5CCNAME points.
func DefaultCachePath() string {
	if p := os.Getenv("CERNBOX_TOKEN_CACHE"); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "cernbox_cc_"+strconv.Itoa(os.Getuid()))
}

// NewCache returns a cache backed by the file at path. An empty path selects
// DefaultCachePath.
func NewCache(path string) *Cache {
	if path == "" {
		path = DefaultCachePath()
	}
	return &Cache{path: path}
}

// Path returns the file backing the cache.
func (c *Cache) Path() string { return c.path }

// Get returns the token stored under key. A missing file, a malformed file, or
// a file with permissions wider than 0600 all read as "no entry": a cache is an
// optimisation, and failing the command because of one would be worse than
// re-authenticating.
func (c *Cache) Get(key string) (*Token, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	f, err := c.read()
	if err != nil {
		return nil, false
	}
	tok, ok := f.Entries[key]
	if !ok || tok == nil {
		return nil, false
	}
	return tok, true
}

// Put stores a token under key and prunes any entries that have expired.
func (c *Cache) Put(key string, tok *Token) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	f, err := c.read()
	if err != nil || f.Entries == nil {
		f = &cacheFile{Version: cacheVersion, Entries: map[string]*Token{}}
	}
	f.Entries[key] = tok
	pruneExpired(f.Entries)
	return c.write(f)
}

// Delete removes one entry.
func (c *Cache) Delete(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	f, err := c.read()
	if err != nil {
		return nil
	}
	delete(f.Entries, key)
	return c.write(f)
}

// DeletePrefix removes every entry whose key starts with prefix. Logout uses it
// to drop all identities for one endpoint.
func (c *Cache) DeletePrefix(prefix string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	f, err := c.read()
	if err != nil {
		return nil
	}
	for _, k := range slices.Collect(maps.Keys(f.Entries)) {
		if strings.HasPrefix(k, prefix) {
			delete(f.Entries, k)
		}
	}
	return c.write(f)
}

// Clear removes the cache file entirely.
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := os.Remove(c.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Entries returns every cached token, for "cernbox status".
func (c *Cache) Entries() map[string]*Token {
	c.mu.Lock()
	defer c.mu.Unlock()

	f, err := c.read()
	if err != nil {
		return nil
	}
	return f.Entries
}

func (c *Cache) read() (*cacheFile, error) {
	info, err := os.Stat(c.path)
	if err != nil {
		return nil, err
	}
	// A cache readable by anyone else is not a cache we are willing to trust:
	// its contents may have been replaced, and using a token from it would mean
	// authenticating as whoever wrote it.
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("token cache %s has permissions %o, refusing to read it", c.path, info.Mode().Perm())
	}

	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, err
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Version != cacheVersion {
		return nil, fmt.Errorf("token cache %s has unsupported version %d", c.path, f.Version)
	}
	if f.Entries == nil {
		f.Entries = map[string]*Token{}
	}
	return &f, nil
}

// write replaces the cache atomically, so a concurrent reader never sees a
// half-written file and a crash cannot leave a truncated one.
func (c *Cache) write(f *cacheFile) error {
	f.Version = cacheVersion
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}

	dir := filepath.Dir(c.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(c.path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, c.path)
}

// pruneExpired drops entries that can no longer be used. A token with a refresh
// token is kept even when its access token has expired, because the refresh is
// the whole point.
func pruneExpired(entries map[string]*Token) {
	now := time.Now()
	for k, t := range entries {
		if t == nil {
			delete(entries, k)
			continue
		}
		if t.RefreshToken != "" {
			continue
		}
		if !t.Expiry.IsZero() && now.After(t.Expiry) {
			delete(entries, k)
		}
	}
}
