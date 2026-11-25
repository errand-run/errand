//go:build !linux && !darwin

package system

import "errors"

func watchDir(dir, name string, on func()) (func(), error) {
	return nil, errors.New("not told by this system")
}
