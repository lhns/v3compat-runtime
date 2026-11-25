//go:build !unix

package wrapper

import "errors"

func trustedDir(path string) error {
	return errors.New(path + ": ownership cannot be checked on this OS")
}
