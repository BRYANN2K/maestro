package editor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

// MaxEditableFileSize bounds the amount of file data the built-in editor will
// load into memory. Larger files are refused with a safe explanation while the
// current buffer remains active.
const MaxEditableFileSize int64 = 8 << 20 // 8 MiB

// FileBlockReason describes why file content was kept out of the editor.
// Values are deliberately closed: UI messages are derived from these constants
// and never interpolate bytes read from an untrusted file.
type FileBlockReason string

const (
	FileBlockExecutable  FileBlockReason = "executable"
	FileBlockBinary      FileBlockReason = "binary"
	FileBlockInvalidUTF8 FileBlockReason = "invalid-utf8"
	FileBlockControl     FileBlockReason = "unsafe-control"
	FileBlockTooLarge    FileBlockReason = "too-large"
	FileBlockNonRegular  FileBlockReason = "non-regular"
)

// Label returns a terminal-safe, user-facing reason.
func (r FileBlockReason) Label() string {
	switch r {
	case FileBlockExecutable:
		return "executable file"
	case FileBlockBinary:
		return "binary file"
	case FileBlockInvalidUTF8:
		return "content is not valid UTF-8 text"
	case FileBlockControl:
		return "content contains unsafe terminal control characters"
	case FileBlockTooLarge:
		return "file exceeds the 8 MiB editor limit"
	case FileBlockNonRegular:
		return "path is not a regular file"
	default:
		return "unsupported file content"
	}
}

// UnsupportedFileError is returned before unsafe bytes enter a Buffer.
type UnsupportedFileError struct {
	Reason FileBlockReason
	Size   int64
}

func (e *UnsupportedFileError) Error() string {
	if e == nil {
		return "editor blocked unsupported file content"
	}
	return "editor blocked file: " + e.Reason.Label() + " (content not loaded)"
}

// SafeOpenError turns filesystem and validation failures into a short message
// that contains neither an untrusted path nor file bytes.
func SafeOpenError(err error) string {
	var unsupported *UnsupportedFileError
	switch {
	case errors.As(err, &unsupported):
		return "File unavailable: " + unsupported.Reason.Label() + ". Content was not loaded."
	case errors.Is(err, os.ErrNotExist):
		return "File unavailable: path does not exist."
	case errors.Is(err, os.ErrPermission):
		return "File unavailable: permission denied."
	default:
		return "File unavailable: unable to read this path safely."
	}
}

func blockedFile(reason FileBlockReason, size int64) error {
	return &UnsupportedFileError{Reason: reason, Size: size}
}

// readTextFile reads a complete, bounded UTF-8 text file. It validates every
// byte before the caller constructs a Buffer.
func readTextFile(path string) ([]byte, error) {
	// Inspect the path before opening it: opening a FIFO can otherwise block the
	// event loop indefinitely. The descriptor is validated again below to close
	// the ordinary path-replacement window as far as portable os APIs allow.
	preInfo, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	if !preInfo.Mode().IsRegular() {
		return nil, blockedFile(FileBlockNonRegular, preInfo.Size())
	}
	if preInfo.Size() > MaxEditableFileSize {
		return nil, blockedFile(FileBlockTooLarge, preInfo.Size())
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, blockedFile(FileBlockNonRegular, info.Size())
	}
	if info.Size() > MaxEditableFileSize {
		return nil, blockedFile(FileBlockTooLarge, info.Size())
	}

	data, err := io.ReadAll(io.LimitReader(f, MaxEditableFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	if int64(len(data)) > MaxEditableFileSize {
		return nil, blockedFile(FileBlockTooLarge, int64(len(data)))
	}
	if reason := classifyFileText(data, true); reason != "" {
		return nil, blockedFile(reason, int64(len(data)))
	}

	// CR is meaningful text input but unsafe as raw terminal output. Normalize
	// line endings before the buffer sees them.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	return data, nil
}

func classifyFileText(data []byte, complete bool) FileBlockReason {
	if hasExecutableSignature(data) {
		return FileBlockExecutable
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return FileBlockBinary
	}

	remaining := data
	for len(remaining) > 0 {
		r, size := utf8.DecodeRune(remaining)
		if r == utf8.RuneError && size == 1 {
			if !complete && !utf8.FullRune(remaining) {
				break // the bounded probe ended in the middle of a valid rune
			}
			return FileBlockInvalidUTF8
		}
		if isUnsafeFileControl(r) {
			return FileBlockControl
		}
		remaining = remaining[size:]
	}
	return ""
}

func isUnsafeFileControl(r rune) bool {
	if r == '\n' || r == '\r' || r == '\t' {
		return false
	}
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

func hasExecutableSignature(data []byte) bool {
	if len(data) >= 4 {
		sig := [4]byte{data[0], data[1], data[2], data[3]}
		switch sig {
		case [4]byte{0x7f, 'E', 'L', 'F'},
			[4]byte{0xfe, 0xed, 0xfa, 0xce}, [4]byte{0xce, 0xfa, 0xed, 0xfe},
			[4]byte{0xfe, 0xed, 0xfa, 0xcf}, [4]byte{0xcf, 0xfa, 0xed, 0xfe},
			[4]byte{0xca, 0xfe, 0xba, 0xbe}, [4]byte{0xbe, 0xba, 0xfe, 0xca},
			[4]byte{0xca, 0xfe, 0xba, 0xbf}, [4]byte{0xbf, 0xba, 0xfe, 0xca}:
			return true
		}
	}
	return len(data) >= 2 && data[0] == 'M' && data[1] == 'Z'
}
