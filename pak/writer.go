package pak

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

var (
	errNameTooLong   = errors.New("pak: file name too long")
	errAlreadyClosed = errors.New("pak: already closed")
	errFileNotOpen   = errors.New("pak: file not open")
	errFileTooBig    = errors.New("pak: file too big")
)

// Writer implements a PAK file writer.
type Writer struct {
	w      io.WriteSeeker
	files  []pakEntry
	offset int
	closed bool
	isFile bool
}

// OpenWriter returns a new Writer writing a PAK file specified by name.
func OpenWriter(name string) (*Writer, error) {
	f, err := os.Create(name)
	if err != nil {
		return nil, err
	}
	pak, err := NewWriter(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	pak.isFile = true
	return pak, nil
}

// NewWriter returns a new Writer writing a PAK file to w.
func NewWriter(w io.WriteSeeker) (*Writer, error) {
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := binary.Write(w, binary.LittleEndian, new(pakHeader)); err != nil {
		return nil, err
	}
	pak := &Writer{
		w:      w,
		offset: headerSize,
	}
	return pak, nil
}

func (pak *Writer) finishEntry() {
	if len(pak.files) > 0 {
		e := &pak.files[len(pak.files)-1]
		e.Filelen = uint32(pak.offset) - e.Filepos
	}
}

func (pak *Writer) finish() error {
	pak.finishEntry()

	dirLen := binary.Size(pak.files)
	if pak.offset > MaxOffset-dirLen {
		return errBadDirOfs
	}

	if err := binary.Write(pak.w, binary.LittleEndian, pak.files); err != nil {
		return err
	}
	if _, err := pak.w.Seek(0, io.SeekStart); err != nil {
		return err
	}

	header := &pakHeader{
		Ident:  pakIdent,
		Dirofs: uint32(pak.offset),
		Dirlen: uint32(dirLen),
	}
	if err := binary.Write(pak.w, binary.LittleEndian, header); err != nil {
		return err
	}
	return nil
}

// Close finishes writing the PAK file by writing the header and directory.
// If Writer was created by OpenWriter it also closes the underlying file.
func (pak *Writer) Close() error {
	if pak.closed {
		return errAlreadyClosed
	}

	err := pak.finish()
	pak.closed = true

	var err2 error
	if pak.isFile {
		err2 = pak.w.(io.Closer).Close()
	}
	if err != nil {
		return err
	}
	return err2
}

// Create adds a file to the PAK file using the provided name. File contents
// must be written using Write before the next call to Create or Close.
func (pak *Writer) Create(name string) error {
	if pak.closed {
		return errAlreadyClosed
	}
	pak.finishEntry()

	if len(name) > MaxFileName {
		return errNameTooLong
	}
	if len(pak.files) >= MaxFiles {
		return errTooManyFiles
	}

	entry := pakEntry{
		Filepos: uint32(pak.offset),
	}
	copy(entry.Name[:], name)

	pak.files = append(pak.files, entry)
	return nil
}

// Writes content of the file created by the last call to Create.
// Can be called multiple times to write the data in chunks.
func (pak *Writer) Write(p []byte) (int, error) {
	if pak.closed {
		return 0, errAlreadyClosed
	}
	if len(pak.files) == 0 {
		return 0, errFileNotOpen
	}
	if pak.offset > MaxOffset-len(p) {
		return 0, errFileTooBig
	}
	n, err := pak.w.Write(p)
	pak.offset += n
	return n, err
}
