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
	"path"
	"sort"
)

//go:embed all:context
var embedded embed.FS

// BinarySuffix marks binary assets that must retain their exact bytes.
const BinarySuffix = ".bin"

// IsBinary reports whether an asset's build-time bytes must bypass text normalization.
func IsBinary(name string, data []byte) bool {
	return path.Ext(name) == BinarySuffix || bytes.Contains(data, []byte{0})
}

// Normalize converts text asset CRLF line endings to LF and preserves binary bytes.
func Normalize(name string, data []byte) []byte {
	if IsBinary(name, data) {
		return data
	}
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

// Context returns a read-only, LF-normalized embedded image build context.
// Paths are relative to context; BinarySuffix files and NUL-containing files retain their exact bytes.
func Context() fs.FS {
	context, err := fs.Sub(embedded, "context")
	if err != nil {
		panic(err)
	}
	return normalizedFS{context}
}

// Hash returns the SHA256 asset hash of a normalized image build context.
// It hashes "sandboxed-agents:image-context:v1\n" followed by files in lexical path order. Each file
// contributes its uint64 big-endian path length, path bytes, uint64 big-endian
// content length, and content bytes. Directories, modes and times are excluded.
func Hash(context fs.FS) (string, error) {
	hash := sha256.New()
	hash.Write([]byte("sandboxed-agents:image-context:v1\n"))
	_, err := walkAssets(context, func(path string, data []byte) error {
		binary.Write(hash, binary.BigEndian, uint64(len(path)))
		hash.Write([]byte(path))
		binary.Write(hash, binary.BigEndian, uint64(len(data)))
		hash.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// walkAssets owns normalization, regular-file validation, global path ordering and reads.
func walkAssets(source fs.FS, visit func(string, []byte) error) ([]string, error) {
	if _, normalized := source.(normalizedFS); !normalized {
		source = normalizedFS{source}
	}
	var paths []string
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("image context %s: only regular files are supported", name)
		}
		paths = append(paths, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	for _, name := range paths {
		data, err := fs.ReadFile(source, name)
		if err != nil {
			return nil, err
		}
		if err := visit(name, data); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

// normalizedFS keeps normalization in one place for the asset hash and release preprocessing.
type normalizedFS struct{ fs.FS }

// Open passes directories through and returns normalized file contents with their adjusted size.
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
	data = Normalize(name, data)
	return &normalizedFile{Reader: bytes.NewReader(data), info: normalizedInfo{FileInfo: info, size: int64(len(data))}}, nil
}

type normalizedFile struct {
	*bytes.Reader
	info fs.FileInfo
}

// Stat returns metadata with the normalized content size.
func (file *normalizedFile) Stat() (fs.FileInfo, error) { return file.info, nil }

// Close succeeds without releasing resources because normalized contents are held in memory.
func (file *normalizedFile) Close() error { return nil }

type normalizedInfo struct {
	fs.FileInfo
	size int64
}

// Size returns the normalized content length instead of the underlying file size.
func (info normalizedInfo) Size() int64 { return info.size }
