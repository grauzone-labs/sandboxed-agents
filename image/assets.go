// Package image supplies the embedded image build context and its asset hash.
package image

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

//go:embed all:context
var embedded embed.FS

// BinarySuffix marks binary assets that must retain their exact bytes.
const BinarySuffix = ".bin"

// Context returns a read-only, LF-normalized embedded image build context.
// Paths are relative to context; BinarySuffix files and NUL-containing files retain their exact bytes.
func Context() fs.FS {
	context, err := fs.Sub(embedded, "context")
	if err != nil {
		panic(err)
	}
	return normalizedFS{context}
}

// WriteContext writes an LF-normalized image build context into directory, preserving binary asset bytes.
func WriteContext(source fs.FS, directory string) error {
	if _, normalized := source.(normalizedFS); !normalized {
		source = normalizedFS{source}
	}
	return fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("image context %s: only regular files are supported", path)
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		name := filepath.Join(directory, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		return os.WriteFile(name, data, 0600)
	})
}

// Hash returns the SHA256 asset hash of a normalized image build context.
// It hashes "sandboxed-agents:image-context:v1\n" followed by files in lexical path order. Each file
// contributes its uint64 big-endian path length, path bytes, uint64 big-endian
// content length, and content bytes. Directories, modes and times are excluded.
func Hash(context fs.FS) (string, error) {
	hash := sha256.New()
	hash.Write([]byte("sandboxed-agents:image-context:v1\n"))
	if _, normalized := context.(normalizedFS); !normalized {
		context = normalizedFS{context}
	}
	var paths []string
	err := fs.WalkDir(context, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("image context %s: only regular files are supported", path)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := fs.ReadFile(context, path)
		if err != nil {
			return "", err
		}
		binary.Write(hash, binary.BigEndian, uint64(len(path)))
		hash.Write([]byte(path))
		binary.Write(hash, binary.BigEndian, uint64(len(data)))
		hash.Write(data)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// normalizedFS keeps normalization in one place for the asset hash and release preprocessing.
type normalizedFS struct{ fs.FS }

func (context normalizedFS) Open(name string) (fs.File, error) {
	file, err := context.FS.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if info.IsDir() {
		return file, nil
	}
	data, err := io.ReadAll(file)
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if path.Ext(name) != BinarySuffix && !bytes.Contains(data, []byte{0}) {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	return &normalizedFile{Reader: bytes.NewReader(data), info: normalizedInfo{FileInfo: info, size: int64(len(data))}}, nil
}

type normalizedFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (file *normalizedFile) Stat() (fs.FileInfo, error) { return file.info, nil }
func (file *normalizedFile) Close() error               { return nil }

type normalizedInfo struct {
	fs.FileInfo
	size int64
}

func (info normalizedInfo) Size() int64 { return info.size }
