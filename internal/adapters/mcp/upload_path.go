package mcp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// resolveUploadRoot checks the --upload-root value and returns it as an absolute, clean path. An
// empty value stays empty and turns uploads off. A value that is not a directory after link
// resolution stops startup (fail-hard): the operator asked for uploads, so a wrong path is a
// configuration error. The returned path keeps its symbolic links: openUploadFile resolves them
// again at each call, and the agent can then use the root path that the operator gave.
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

	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("absolute path of upload root: %w", err)
	}

	return abs, nil
}

// openUploadFile opens a regular file under root for upload_file and returns the file and its
// upload name. A relative path is relative to root, not to the working directory, because the
// agent host selects the working directory. The caller closes the file.
//
// The function does a lexical root check before it reads the file system. Each path outside
// the root gets the same error, ErrPathOutsideRoot, without the path, so the error does not
// tell if a path outside the root exists. Then the checks resolve symbolic links, so a link
// under root to a file outside root gets the same error. After the open, the function compares
// the opened file with the checked file. This prevents a time-of-check to time-of-use (TOCTOU)
// swap of the path between the two steps.
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

	lexicalRel, inside := lexicalInsideRoot(root, rootReal, path)
	if !inside {
		return nil, "", ErrPathOutsideRoot
	}

	err = refuseHidden(lexicalRel)
	if err != nil {
		return nil, "", err
	}

	target := filepath.Join(rootReal, lexicalRel)

	resolved, err := resolveUnderRoot(rootReal, target, path)
	if err != nil {
		return nil, "", err
	}

	rel, inside := relInsideRoot(rootReal, resolved)
	if !inside {
		return nil, "", ErrPathOutsideRoot
	}

	// A link with a plain name can point to a hidden file, so the resolved path gets the check too.
	err = refuseHidden(rel)
	if err != nil {
		return nil, "", err
	}

	file, err := openInRoot(rootReal, rel, path)
	if err != nil {
		return nil, "", err
	}

	// The name the agent gave is kept: a link "report.pdf" to "blob-1" uploads as "report.pdf".
	return file, filepath.Base(target), nil
}

// refuseHidden returns ErrHiddenPath when an element of rel starts with a dot. Such names, for
// example .env, .git or .ssh, often hold secrets, and an agent that follows injected text can
// ask for them. No setting turns this check off.
func refuseHidden(rel string) error {
	if hasHiddenElement(rel) {
		return ErrHiddenPath
	}

	return nil
}

// lexicalInsideRoot returns the clean path of path relative to the root, without a file system
// call. root is the root that the operator gave and rootReal is the same root with resolved
// links. An absolute path can start with each of the two forms.
func lexicalInsideRoot(root, rootReal, path string) (string, bool) {
	if !filepath.IsAbs(path) {
		return relInsideRoot(rootReal, filepath.Join(rootReal, path))
	}

	clean := filepath.Clean(path)

	if rel, ok := relInsideRoot(rootReal, clean); ok {
		return rel, true
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}

	return relInsideRoot(rootAbs, clean)
}

// hasHiddenElement reports whether an element of rel starts with a dot. rel is a clean path
// relative to the root; "." is the root itself and has no hidden element.
func hasHiddenElement(rel string) bool {
	if rel == "." {
		return false
	}

	for elem := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if strings.HasPrefix(elem, ".") {
			return true
		}
	}

	return false
}

// resolveUnderRoot resolves the links of target, which is lexically under rootReal. When the
// resolution fails, the error must not tell if a path outside the root exists. A link under the
// root can lead out of it, and EvalSymlinks errors can hold the link target. Thus the function
// finds the deepest entry of the path that exists. If that entry resolves outside the root, or
// it is a link that does not resolve, the error is ErrPathOutsideRoot: a broken link can point
// out of the root. Otherwise the error holds only the agent's path and the cause.
func resolveUnderRoot(rootReal, target, shown string) (string, error) {
	resolved, err := realPath(target)
	if err == nil {
		return resolved, nil
	}

	for entry := target; len(entry) > len(rootReal); entry = filepath.Dir(entry) {
		_, statErr := os.Lstat(entry)
		if statErr != nil {
			continue
		}

		entryReal, realErr := realPath(entry)
		if realErr != nil {
			return "", ErrPathOutsideRoot
		}

		if _, ok := relInsideRoot(rootReal, entryReal); !ok {
			return "", ErrPathOutsideRoot
		}

		break
	}

	return "", resolveError(shown, err)
}

// resolveError removes the path that the file system error holds, because that path can be a
// resolved link target. A missing file gets a fixed text.
func resolveError(shown string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%q: %w", shown, errNoSuchFile)
	}

	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		return fmt.Errorf("resolve %q: %w", shown, pathErr.Err)
	}

	return fmt.Errorf("resolve %q: %w", shown, err)
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
