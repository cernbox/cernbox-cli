package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "cache"))
	tok := &Token{
		Header: "Authorization", Value: "Bearer abc",
		Expiry:   time.Now().Add(time.Hour).Round(time.Second),
		Provider: "kerberos", Subject: "einstein",
	}

	if err := c.Put("key", tok); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("key")
	if !ok {
		t.Fatal("Get returned nothing after Put")
	}
	if got.Value != tok.Value || got.Provider != tok.Provider || got.Subject != tok.Subject {
		t.Errorf("got %+v, want %+v", got, tok)
	}
	if !got.Expiry.Equal(tok.Expiry) {
		t.Errorf("expiry = %v, want %v", got.Expiry, tok.Expiry)
	}
}

func TestCacheMissingFile(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, ok := c.Get("key"); ok {
		t.Error("Get on a missing cache should report no entry")
	}
}

// TestCacheIsCreatedPrivate is a security requirement, not a detail: the file
// holds bearer tokens.
func TestCacheIsCreatedPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	c := NewCache(path)
	if err := c.Put("key", &Token{Value: "Bearer abc"}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache permissions = %o, want 600", perm)
	}
}

// TestCacheRefusesWorldReadableFile: if the file is readable by others it may
// also have been written by others, and using a token from it would mean
// authenticating as whoever put it there.
func TestCacheRefusesWorldReadableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	c := NewCache(path)
	if err := c.Put("key", &Token{Value: "Bearer abc"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get("key"); ok {
		t.Error("a cache readable by other users must not be trusted")
	}
}

func TestCacheIgnoresCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := NewCache(path)
	if _, ok := c.Get("key"); ok {
		t.Error("a corrupt cache should read as empty")
	}
	// It must still be writable: a corrupt cache should self-heal rather than
	// permanently break authentication.
	if err := c.Put("key", &Token{Value: "Bearer abc"}); err != nil {
		t.Fatalf("Put over a corrupt cache: %v", err)
	}
	if _, ok := c.Get("key"); !ok {
		t.Error("Put should have replaced the corrupt cache")
	}
}

func TestCacheIgnoresUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	data, _ := json.Marshal(map[string]any{"version": 99, "entries": map[string]any{
		"key": map[string]any{"value": "Bearer abc"},
	}})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := NewCache(path).Get("key"); ok {
		t.Error("a cache written by a future version should be ignored, not misread")
	}
}

func TestCachePrunesExpiredEntries(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "cache"))

	if err := c.Put("stale", &Token{Value: "Bearer old", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("fresh", &Token{Value: "Bearer new", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get("stale"); ok {
		t.Error("an expired entry should have been pruned on the next write")
	}
	if _, ok := c.Get("fresh"); !ok {
		t.Error("a valid entry was pruned")
	}
}

// TestCacheKeepsExpiredTokenWithRefresh: pruning it would throw away the
// refresh token, forcing a full re-authentication when a cheap renewal would
// have done.
func TestCacheKeepsExpiredTokenWithRefresh(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "cache"))

	if err := c.Put("refreshable", &Token{
		Value: "Bearer old", Expiry: time.Now().Add(-time.Hour), RefreshToken: "renew-me",
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("other", &Token{Value: "Bearer x", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	got, ok := c.Get("refreshable")
	if !ok {
		t.Fatal("an expired token with a refresh token must be kept")
	}
	if got.RefreshToken != "renew-me" {
		t.Errorf("refresh token = %q", got.RefreshToken)
	}
}

func TestCacheDelete(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "cache"))
	if err := c.Put("key", &Token{Value: "Bearer abc"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete("key"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("key"); ok {
		t.Error("Delete did not remove the entry")
	}
}

func TestCacheDeletePrefix(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "cache"))
	for _, key := range []string{
		"https://a.test\x00einstein", "https://a.test\x00marie", "https://b.test\x00einstein",
	} {
		if err := c.Put(key, &Token{Value: "Bearer " + key}); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.DeletePrefix("https://a.test\x00"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("https://a.test\x00einstein"); ok {
		t.Error("DeletePrefix left an entry behind")
	}
	if _, ok := c.Get("https://a.test\x00marie"); ok {
		t.Error("DeletePrefix left an entry behind")
	}
	if _, ok := c.Get("https://b.test\x00einstein"); !ok {
		t.Error("DeletePrefix removed an entry for a different endpoint")
	}
}

func TestCacheClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	c := NewCache(path)
	if err := c.Put("key", &Token{Value: "Bearer abc"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Clear did not remove the file")
	}
	// Clearing an already-clear cache is not an error.
	if err := c.Clear(); err != nil {
		t.Errorf("Clear on a missing file: %v", err)
	}
}

func TestCacheWriteIsAtomic(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(filepath.Join(dir, "cache"))
	if err := c.Put("key", &Token{Value: "Bearer abc"}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// TestDefaultCachePathIsNotInHome is the point of the whole location choice: on
// lxplus, home is shared across every node in the cluster.
func TestDefaultCachePathIsNotInHome(t *testing.T) {
	t.Setenv("CERNBOX_TOKEN_CACHE", "")
	os.Unsetenv("CERNBOX_TOKEN_CACHE")

	path := DefaultCachePath()
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(path, home) {
		t.Errorf("default cache path %q is inside the home directory", path)
	}
	if !strings.Contains(path, "cernbox_cc_") {
		t.Errorf("default cache path = %q, want it named after the uid", path)
	}
	if !strings.Contains(path, strconv.Itoa(os.Getuid())) {
		t.Errorf("default cache path %q does not include the uid", path)
	}
}

func TestCachePathOverride(t *testing.T) {
	t.Setenv("CERNBOX_TOKEN_CACHE", "/custom/location")
	if got := DefaultCachePath(); got != "/custom/location" {
		t.Errorf("DefaultCachePath() = %q, want the environment override", got)
	}
}

func TestCacheEntries(t *testing.T) {
	c := NewCache(filepath.Join(t.TempDir(), "cache"))
	if err := c.Put("a", &Token{Value: "Bearer a", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("b", &Token{Value: "Bearer b", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := c.Entries(); len(got) != 2 {
		t.Errorf("Entries() returned %d entries, want 2", len(got))
	}
}
