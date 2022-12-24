package pak

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

const (
	pakIdent   = 'P' | 'A'<<8 | 'C'<<16 | 'K'<<24
	headerSize = 12
	entrySize  = 64

	// Maximum number of files in PAK file.
	MaxFiles = 4096

	// Maximum length of file name in PAK file.
	MaxFileName = 56

	// Maximum size of PAK file and all files it contains.
	MaxOffset = 1<<31 - 1
)

var (
	errBadIdent     = errors.New("pak: bad ident")
	errBadDirLen    = errors.New("pak: bad directory length")
	errBadDirOfs    = errors.New("pak: bad directory offset")
	errBadFileLen   = errors.New("pak: bad file length")
	errBadFilePos   = errors.New("pak: bad file position")
	errTooManyFiles = errors.New("pak: too many files")
)

type pakHeader struct {
	Ident  uint32
	Dirofs uint32
	Dirlen uint32
}

type pakEntry struct {
	Name    [MaxFileName]byte
	Filepos uint32
	Filelen uint32
}

// A File is a single file in a PAK archive.
// The file content can be accessed by calling Open.
type File struct {
	Name    string
	Filepos uint32
	Filelen uint32
	pak     *Reader
}

// Open returns a SectionReader that provides access to the File's contents.
// Multiple files may be read concurrently.
func (f *File) Open() *io.SectionReader {
	return io.NewSectionReader(f.pak.r, int64(f.Filepos), int64(f.Filelen))
}

// A Reader serves content from a PAK archive.
type Reader struct {
	File []*File
	r    *io.SectionReader
}

// A ReadCloser is a Reader that must be closed when no longer needed.
type ReadCloser struct {
	Reader
	f *os.File
}

// OpenReader will open the PAK file specified by name and return a ReadCloser.
func OpenReader(name string) (*ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	pak := new(ReadCloser)
	if err := pak.init(f, fi.Size()); err != nil {
		f.Close()
		return nil, err
	}
	pak.f = f
	return pak, nil
}

// NewReader returns a new Reader reading from r, which is assumed to
// have the given size in bytes.
func NewReader(r io.ReaderAt, size int64) (*Reader, error) {
	pak := new(Reader)
	if err := pak.init(r, size); err != nil {
		return nil, err
	}
	return pak, nil
}

func (pak *Reader) init(r io.ReaderAt, size int64) error {
	pak.r = io.NewSectionReader(r, 0, size)
	var header pakHeader
	if err := binary.Read(pak.r, binary.LittleEndian, &header); err != nil {
		return err
	}
	if header.Ident != pakIdent {
		return errBadIdent
	}
	if header.Dirlen%entrySize != 0 {
		return errBadDirLen
	}
	numFiles := int(header.Dirlen / entrySize)
	if numFiles > MaxFiles {
		return errTooManyFiles
	}
	if header.Dirofs > MaxOffset-header.Dirlen {
		return errBadDirOfs
	}
	if _, err := pak.r.Seek(int64(header.Dirofs), io.SeekStart); err != nil {
		return err
	}
	pak.File = make([]*File, numFiles)
	for i := 0; i < numFiles; i++ {
		var entry pakEntry
		if err := binary.Read(pak.r, binary.LittleEndian, &entry); err != nil {
			return err
		}
		if entry.Filelen > MaxOffset {
			return errBadFileLen
		}
		if entry.Filepos > MaxOffset-entry.Filelen {
			return errBadFilePos
		}
		b := bytes.IndexByte(entry.Name[:], 0)
		if b < 0 {
			b = len(entry.Name)
		}
		pak.File[i] = &File{string(entry.Name[:b]), entry.Filepos, entry.Filelen, pak}
	}
	return nil
}

// Close closes the PAK file, rendering it unusable for I/O.
func (pak *ReadCloser) Close() error {
	return pak.f.Close()
}
