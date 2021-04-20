package bp2build

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"android/soong/shared"
)

// A tree structure that describes what to do at each directory in the created
// symlink tree. Currently it is used to enumerate which files/directories
// should be excluded from symlinking. Each instance of "node" represents a file
// or a directory. If excluded is true, then that file/directory should be
// excluded from symlinking. Otherwise, the node is not excluded, but one of its
// descendants is (otherwise the node in question would not exist)
type node struct {
	name     string
	excluded bool // If false, this is just an intermediate node
	children map[string]*node
}

// Ensures that the a node for the given path exists in the tree and returns it.
func ensureNodeExists(root *node, path string) *node {
	if path == "" {
		return root
	}

	if path[len(path)-1] == '/' {
		path = path[:len(path)-1] // filepath.Split() leaves a trailing slash
	}

	dir, base := filepath.Split(path)

	// First compute the parent node...
	dn := ensureNodeExists(root, dir)

	// then create the requested node as its direct child, if needed.
	if child, ok := dn.children[base]; ok {
		return child
	} else {
		dn.children[base] = &node{base, false, make(map[string]*node)}
		return dn.children[base]
	}
}

// Turns a list of paths to be excluded into a tree made of "node" objects where
// the specified paths are marked as excluded.
func treeFromExcludePathList(paths []string) *node {
	result := &node{"", false, make(map[string]*node)}

	for _, p := range paths {
		ensureNodeExists(result, p).excluded = true
	}

	return result
}

// Calls readdir() and returns it as a map from the basename of the files in dir
// to os.FileInfo.
func readdirToMap(dir string) map[string]os.FileInfo {
	entryList, err := ioutil.ReadDir(dir)
	result := make(map[string]os.FileInfo)

	if err != nil {
		if os.IsNotExist(err) {
			// It's okay if a directory doesn't exist; it just means that one of the
			// trees to be merged contains parts the other doesn't
			return result
		} else {
			fmt.Fprintf(os.Stderr, "Cannot readdir '%s': %s\n", dir, err)
			os.Exit(1)
		}
	}

	for _, fi := range entryList {
		result[fi.Name()] = fi
	}

	return result
}

// Creates a symbolic link at dst pointing to src
func symlinkIntoForest(topdir, dst, src string) {
	err := os.Symlink(shared.JoinPath(topdir, src), shared.JoinPath(topdir, dst))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot create symlink at '%s' pointing to '%s': %s", dst, src, err)
		os.Exit(1)
	}
}

// Recursively plants a symlink forest at forestDir. The symlink tree will
// contain every file in buildFilesDir and srcDir excluding the files in
// exclude. Collects every directory encountered during the traversal of srcDir
// into acc.
func plantSymlinkForestRecursive(topdir string, forestDir string, buildFilesDir string, srcDir string, exclude *node, acc *[]string) {
	if exclude != nil && exclude.excluded {
		// This directory is not needed, bail out
		return
	}

	*acc = append(*acc, srcDir)
	srcDirMap := readdirToMap(shared.JoinPath(topdir, srcDir))
	buildFilesMap := readdirToMap(shared.JoinPath(topdir, buildFilesDir))

	allEntries := make(map[string]bool)
	for n, _ := range srcDirMap {
		allEntries[n] = true
	}

	for n, _ := range buildFilesMap {
		allEntries[n] = true
	}

	err := os.MkdirAll(shared.JoinPath(topdir, forestDir), 0777)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot mkdir '%s': %s\n", forestDir, err)
		os.Exit(1)
	}

	for f, _ := range allEntries {
		if f[0] == '.' {
			continue // Ignore dotfiles
		}

		// The full paths of children in the input trees and in the output tree
		fp := shared.JoinPath(forestDir, f)
		sp := shared.JoinPath(srcDir, f)
		bp := shared.JoinPath(buildFilesDir, f)

		// Descend in the exclusion tree, if there are any excludes left
		var ce *node
		if exclude == nil {
			ce = nil
		} else {
			ce = exclude.children[f]
		}

		sf, sExists := srcDirMap[f]
		bf, bExists := buildFilesMap[f]
		excluded := ce != nil && ce.excluded

		if excluded {
			continue
		}

		if !sExists {
			if bf.IsDir() && ce != nil {
				// Not in the source tree, but we have to exclude something from under
				// this subtree, so descend
				plantSymlinkForestRecursive(topdir, fp, bp, sp, ce, acc)
			} else {
				// Not in the source tree, symlink BUILD file
				symlinkIntoForest(topdir, fp, bp)
			}
		} else if !bExists {
			if sf.IsDir() && ce != nil {
				// Not in the build file tree, but we have to exclude something from
				// under this subtree, so descend
				plantSymlinkForestRecursive(topdir, fp, bp, sp, ce, acc)
			} else {
				// Not in the build file tree, symlink source tree, carry on
				symlinkIntoForest(topdir, fp, sp)
			}
		} else if sf.IsDir() && bf.IsDir() {
			// Both are directories. Descend.
			plantSymlinkForestRecursive(topdir, fp, bp, sp, ce, acc)
		} else {
			// Both exist and one is a file. This is an error.
			fmt.Fprintf(os.Stderr,
				"Conflict in workspace symlink tree creation: both '%s' and '%s' exist and at least one of them is a file\n",
				sp, bp)
			os.Exit(1)
		}
	}
}

// Creates a symlink forest by merging the directory tree at "buildFiles" and
// "srcDir" while excluding paths listed in "exclude". Returns the set of paths
// under srcDir on which readdir() had to be called to produce the symlink
// forest.
func PlantSymlinkForest(topdir string, forest string, buildFiles string, srcDir string, exclude []string) []string {
	deps := make([]string, 0)
	os.RemoveAll(shared.JoinPath(topdir, forest))
	excludeTree := treeFromExcludePathList(exclude)
	plantSymlinkForestRecursive(topdir, forest, buildFiles, srcDir, excludeTree, &deps)
	return deps
}