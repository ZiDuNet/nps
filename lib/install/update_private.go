package install

import "os"

// Private update state can contain VKeys and proxy credentials. POSIX modes
// alone do not protect files on Windows, so use the platform's actual ACLs.
func CreatePrivateUpdateDirectory(parent, pattern string) (string, error) {
	dir, err := os.MkdirTemp(parent, pattern)
	if err != nil {
		return "", err
	}
	if err := secureUpdatePath(dir, true); err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	return dir, nil
}

func SecureUpdateFile(path string) error { return secureUpdatePath(path, false) }
