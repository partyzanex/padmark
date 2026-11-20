package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveUploadRoot makes the --upload-root value absolute and resolves its symbolic links. An
// empty value stays empty and turns uploads off. A value that is not a directory stops startup
// (fail-hard): the operator asked for uploads, so a wrong path is a configuration error.
func resolveUploadRoot(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	resolved, err := realPath(raw)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat upload root: %w", err)
	}

	if !info.IsDir() {
		return "", fmt.Errorf("upload root %q %w", raw, errUploadRootNotDir)
	}

	return resolved, nil
}

// openUploadFile opens a regular file under root for upload_file and returns the file and its
// upload name. A relative path is relative to root, not to the working directory, because the
// agent host selects the working directory. The caller closes the file.
//
// The checks resolve symbolic links first, so a link under root to a file outside root is
// refused. After the open, the function compares the opened file with the checked file. This
// prevents a time-of-check to time-of-use (TOCTOU) swap of the path between the two steps.
func openUploadFile(root, path string) (*os.File, string, error) {
	if root == "" {
		return nil, "", ErrUploadsOff
	}

	if strings.TrimSpace(path) == "" {
		return nil, "", errEmptyPath
	}

	// Resolve the root again at each call: a check result must not be used for a later operation.
	rootReal, err := realPath(root)
	if err != nil {
		return nil, "", err
	}

	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootReal, target)
	}

	resolved, err := realPath(target)
	if err != nil {
		return nil, "", err
	}

	rel, ok := relInsideRoot(rootReal, resolved)
	if !ok {
		return nil, "", fmt.Errorf("%q: %w", path, ErrPathOutsideRoot)
	}

	file, err := openInRoot(rootReal, rel, path)
	if err != nil {
		return nil, "", err
	}

	// The name the agent gave is kept: a link "report.pdf" to "blob-1" uploads as "report.pdf".
	return file, filepath.Base(target), nil
}

// openInRoot opens rel under rootDir through os.Root. os.Root refuses a link that leads out of
// rootDir, so a link changed after the checks cannot escape. shown is the path for errors.
func openInRoot(rootDir, rel, shown string) (*os.File, error) {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return nil, fmt.Errorf("open upload root: %w", err)
	}

	// The opened file stays valid after the root closes.
	file, err := openRegularFile(root, rel, shown)

	closeErr := root.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close upload root: %w", closeErr)
	}

	if err != nil {
		return nil, errors.Join(err, closeErr)
	}

	if closeErr != nil {
		return nil, errors.Join(closeErr, file.Close())
	}

	return file, nil
}

// openRegularFile opens rel and makes sure that it is the same regular file as the one that the
// stat saw before the open.
func openRegularFile(root *os.Root, rel, shown string) (*os.File, error) {
	checked, err := root.Stat(rel)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", shown, err)
	}

	if !checked.Mode().IsRegular() {
		return nil, fmt.Errorf("%q: %w", shown, ErrNotRegularFile)
	}

	file, err := root.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", shown, err)
	}

	opened, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("stat opened %q: %w", shown, err), file.Close())
	}

	if !opened.Mode().IsRegular() || !os.SameFile(checked, opened) {
		return nil, errors.Join(fmt.Errorf("%q: %w", shown, errFileReplaced), file.Close())
	}

	return file, nil
}

// realPath returns the absolute path of path with all symbolic links resolved.
func realPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path of %q: %w", path, err)
	}

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}

	return resolved, nil
}

// relInsideRoot returns the path of resolved relative to root, and false when resolved is not
// under root. Both paths must be absolute with resolved symbolic links.
func relInsideRoot(root, resolved string) (string, bool) {
	rel, err := filepath.Rel(root, resolved)
	if err != nil || filepath.IsAbs(rel) {
		return "", false
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}

	return rel, true
}
