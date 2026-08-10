package summaries

import "strings"

// NormalizeRemote collapses common URL variants of the same origin to
// one key. We strip a trailing ".git", lowercase, and convert SSH-style
// "git@host:owner/repo" to "host/owner/repo" so HTTPS and SSH clones
// of the same repo group together.
func NormalizeRemote(url string) string {
	s := strings.TrimSpace(url)
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimSuffix(s, "/")
	switch {
	case strings.HasPrefix(s, "git@"):
		// git@github.com:owner/repo → github.com/owner/repo
		s = strings.TrimPrefix(s, "git@")
		s = strings.Replace(s, ":", "/", 1)
	case strings.HasPrefix(s, "ssh://"):
		s = strings.TrimPrefix(s, "ssh://")
		s = strings.TrimPrefix(s, "git@")
	case strings.HasPrefix(s, "https://"):
		s = strings.TrimPrefix(s, "https://")
	case strings.HasPrefix(s, "http://"):
		s = strings.TrimPrefix(s, "http://")
	}
	return strings.ToLower(s)
}
