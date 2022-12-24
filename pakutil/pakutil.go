package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"github.com/skullernet/pakserve/pak"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	args []string
)

func usage() {
	log.Printf("Usage: %s <cmd> [args]", os.Args[0])
	log.Println("  -l <pak>        | list pak contents")
	log.Println("  -c <pak> <dir>  | create pak from dir")
	log.Println("  -x <pak> <dir>  | extract pak into dir")
	log.Println("  -z <pak> <pkz>  | convert pak to pkz")
	log.Println("  -u <pkz> <pak>  | convert pkz to pak")
	os.Exit(1)
}

func list() {
	if len(args) != 1 {
		usage()
	}
	pak, err := pak.OpenReader(args[0])
	if err != nil {
		log.Fatal(err)
	}
	defer pak.Close()

	size := uint32(0)
	for _, f := range pak.File {
		fmt.Printf("%9d  %s\n", f.Filelen, f.Name)
		size += f.Filelen
	}
	fmt.Println("---------  ---------")
	fmt.Printf("%9d  %d files\n", size, len(pak.File))
}

func create() {
	if len(args) != 2 {
		usage()
	}
	pak, err := pak.OpenWriter(args[0])
	if err != nil {
		log.Fatal(err)
	}

	err = filepath.WalkDir(args[1], func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeType != 0 {
			return nil
		}

		rel, err := filepath.Rel(args[1], path)
		if err != nil {
			return err
		}
		if err = pak.Create(fsToPak(rel)); err != nil {
			return err
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = io.Copy(pak, f)
		return err
	})
	if err != nil {
		log.Fatal(err)
	}

	if err = pak.Close(); err != nil {
		log.Fatal(err)
	}
}

func fsToPak(n string) string {
	n = strings.ReplaceAll(n, `\`, `/`)
	return strings.ToLower(n)
}

func pakToFs(n string) string {
	n = strings.ReplaceAll(n, `\`, `/`)
	n = path.Clean("/" + n)
	return strings.ToLower(n[1:])
}

func extract() {
	if len(args) != 2 {
		usage()
	}
	pak, err := pak.OpenReader(args[0])
	if err != nil {
		log.Fatal(err)
	}
	defer pak.Close()

	for _, f := range pak.File {
		path := pakToFs(f.Name)
		if len(path) < 1 {
			log.Println("WARNING: skipping empty filename")
			continue
		}
		path = filepath.Join(args[1], path)

		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil && !errors.Is(err, os.ErrExist) {
			log.Fatal(err)
		}

		out, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}

		_, err = io.CopyN(out, f.Open(), int64(f.Filelen))
		if err != nil {
			log.Fatal(err)
		}
		if err = out.Close(); err != nil {
			log.Fatal(err)
		}
	}
}

func compress() {
	if len(args) != 2 {
		usage()
	}
	pak, err := pak.OpenReader(args[0])
	if err != nil {
		log.Fatal(err)
	}
	defer pak.Close()

	out, err := os.Create(args[1])
	if err != nil {
		log.Fatal(err)
	}

	zip := zip.NewWriter(out)
	for _, f := range pak.File {
		w, err := zip.Create(f.Name)
		if err != nil {
			log.Fatal(err)
		}
		_, err = io.CopyN(w, f.Open(), int64(f.Filelen))
		if err != nil {
			log.Fatal(err)
		}
	}
	if err = zip.Close(); err != nil {
		log.Fatal(err)
	}
	if err = out.Close(); err != nil {
		log.Fatal(err)
	}
}

func uncompress() {
	if len(args) != 2 {
		usage()
	}
	zip, err := zip.OpenReader(args[0])
	if err != nil {
		log.Fatal(err)
	}
	defer zip.Close()

	pak, err := pak.OpenWriter(args[1])
	if err != nil {
		log.Fatal(err)
	}

	for _, f := range zip.File {
		if f.Mode()&os.ModeDir != 0 {
			continue
		}
		if err = pak.Create(f.Name); err != nil {
			log.Fatal(err)
		}
		r, err := f.Open()
		if err != nil {
			log.Fatal(err)
		}
		_, err = io.CopyN(pak, r, int64(f.UncompressedSize64))
		if err != nil {
			log.Fatal(err)
		}
		r.Close()
	}

	if err = pak.Close(); err != nil {
		log.Fatal(err)
	}
}

func main() {
	log.SetFlags(0)

	if len(os.Args) < 2 {
		usage()
	}

	args = os.Args[2:]
	switch os.Args[1] {
	case "-l":
		list()
	case "-c":
		create()
	case "-x":
		extract()
	case "-z":
		compress()
	case "-u":
		uncompress()
	default:
		usage()
	}
}
