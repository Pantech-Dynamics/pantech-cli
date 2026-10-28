package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// errFileExists is a target already there, written only with --force.
var errFileExists = errors.New("already exists")

// checkTarget says whether path may be written: nothing there, or (with
// force) a regular file. A symbolic link is refused even with force, so a
// write never lands somewhere the link points; so is anything not a file.
func checkTarget(path string, force bool) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symbolic link: refusing to write through it", path)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s exists and is not a regular file", path)
	case !force:
		return fmt.Errorf("%s %w", path, errFileExists)
	}
	return nil
}

type pendingFile struct {
	path string
	data []byte
	temp string
}

// writeFiles writes every file next to its target under a temporary name,
// then renames each into place, so no target is ever half written and a
// failure while writing leaves none of them changed. Rename replaces a
// path, never following a link at it. Targets must have passed checkTarget.
func writeFiles(files []pendingFile, perm fs.FileMode) error {
	cleanup := func() {
		for _, f := range files {
			if f.temp != "" {
				_ = os.Remove(f.temp)
			}
		}
	}
	for i := range files {
		f := &files[i]
		tmp, err := os.CreateTemp(filepath.Dir(f.path), "."+filepath.Base(f.path)+".tmp-*")
		if err != nil {
			cleanup()
			return err
		}
		f.temp = tmp.Name()
		_, werr := tmp.Write(f.data)
		cerr := tmp.Chmod(perm)
		if err := errors.Join(werr, cerr, tmp.Close()); err != nil {
			cleanup()
			return fmt.Errorf("writing %s: %w", f.path, err)
		}
	}
	for i := range files {
		f := &files[i]
		if err := os.Rename(f.temp, f.path); err != nil {
			cleanup()
			return fmt.Errorf("writing %s: %w", f.path, err)
		}
		f.temp = ""
	}
	return nil
}
