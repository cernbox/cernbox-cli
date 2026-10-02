//go:build !windows

package auth

import (
	"io/fs"
	"os"
	"strconv"
)

// userSuffix tells apart the caches of different users sharing /tmp.
func userSuffix() string { return "_" + strconv.Itoa(os.Getuid()) }

// privateMode reports whether a cache file is closed to everyone but its owner.
func privateMode(m fs.FileMode) bool { return m.Perm()&0o077 == 0 }

func defaultKrb5Config() string { return "/etc/krb5.conf" }
