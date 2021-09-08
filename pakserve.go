package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type PakFileEntry struct {
	method  uint16
	offset  int64
	size    int64 // raw (compressed) size
	filecrc uint32
	filelen uint32 // uncompressed size
	mtime   uint32
}

type SearchPath struct {
	path  string
	files map[string]PakFileEntry
}

type SearchPathMatch struct {
	match  *regexp.Regexp
	search []*SearchPath
}

const (
	LogLevelError = iota
	LogLevelInfo
	LogLevelDebug
)

type Config struct {
	Listen        string
	ContentType   string
	RefererCheck  string
	PakWhiteList  []string
	DirWhiteList  []string
	SearchPaths   map[string][]string
	LogLevel      int
	LogTimeStamps bool
}

var config = Config{Listen: ":8080", ContentType: "application/octet-stream", PakWhiteList: []string{""}}

var (
	refererCheck *regexp.Regexp
	pakWhiteList []*regexp.Regexp
	dirWhiteList []*regexp.Regexp
	searchPaths  []*SearchPathMatch
)

var dirCache = make(map[string][]*SearchPath)

func (entry *PakFileEntry) handleGzip(w http.ResponseWriter, r *io.SectionReader) {
	var b [10]byte

	w.Header().Set("Content-Length", strconv.FormatInt(entry.size+18, 10))
	w.Header().Set("Content-Encoding", "gzip")
	w.WriteHeader(http.StatusOK)

	if r == nil {
		return
	}

	// gzip header
	b[0] = 0x1f
	b[1] = 0x8b
	b[2] = 8 // Z_DEFLATED
	b[3] = 0 // options
	binary.LittleEndian.PutUint32(b[4:8], entry.mtime)
	b[8] = 0x00 // extra flags
	b[9] = 0x03 // UNIX
	w.Write(b[0:10])

	// raw deflate stream
	io.Copy(w, r)

	// gzip trailer
	binary.LittleEndian.PutUint32(b[0:4], entry.filecrc)
	binary.LittleEndian.PutUint32(b[4:8], entry.filelen)
	w.Write(b[0:8])
}

func (entry *PakFileEntry) handleRaw(w http.ResponseWriter, r *io.SectionReader) {
	w.Header().Set("Content-Length", strconv.FormatInt(entry.size, 10))
	if entry.method != 0 {
		// Send raw deflate stream (e.g. no zlib header/trailer).
		// This violates RFC 2616 but works.
		w.Header().Set("Content-Encoding", "deflate")
	}
	w.WriteHeader(http.StatusOK)
	if r != nil {
		io.Copy(w, r)
	}
}

func (entry *PakFileEntry) handleInflate(w http.ResponseWriter, r *io.SectionReader) {
	w.Header().Set("Content-Length", strconv.FormatInt(int64(entry.filelen), 10))
	w.WriteHeader(http.StatusOK)
	if r != nil {
		f := flate.NewReader(r)
		io.Copy(w, f)
		f.Close()
	}
}

// returns the longest match so that "^/" pattern works as expected
func findSearchPath(r *http.Request) (search []*SearchPath, path string) {
	path = strings.ToLower(pathpkg.Clean(r.URL.Path))
	longest := 0
	for _, s := range searchPaths {
		loc := s.match.FindStringIndex(path)
		if loc != nil && loc[0] == 0 && loc[1] > longest {
			search = s.search
			longest = loc[1]
		}
	}
	return search, path[longest:]
}

func parseAcceptEncoding(r *http.Request) (hasGzip, hasDeflate bool) {
	for _, value := range r.Header["Accept-Encoding"] {
		for _, encoding := range strings.Split(value, ",") {
			switch strings.ToLower(strings.TrimSpace(encoding)) {
			case "gzip":
				hasGzip = true
			case "deflate":
				hasDeflate = true
			}
		}
	}
	return
}

func matchWhiteList(list []*regexp.Regexp, s string) bool {
	for _, r := range list {
		if r.MatchString(s) {
			return true
		}
	}
	return false
}

func handler(w http.ResponseWriter, r *http.Request) {
	if filepath.Separator != '/' && strings.ContainsRune(r.URL.Path, filepath.Separator) {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	if !refererCheck.MatchString(r.Referer()) {
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	search, path := findSearchPath(r)
	if search == nil || len(path) == 0 {
		http.NotFound(w, r)
		return
	}

	allowPak := matchWhiteList(pakWhiteList, path)
	allowDir := matchWhiteList(dirWhiteList, path)
	if !allowPak && !allowDir {
		http.NotFound(w, r)
		return
	}

	hasGzip, hasDeflate := parseAcceptEncoding(r)

	w.Header().Set("Content-Type", config.ContentType)

	for _, s := range search {
		if s.files == nil {
			// look in the directory tree
			if !allowDir {
				continue
			}
			f, err := os.Open(filepath.Join(s.path, path))
			if err == nil {
				http.ServeContent(w, r, "", time.Time{}, f)
				f.Close()
				return
			}
			continue
		}

		if !allowPak {
			continue
		}

		// look in packfile
		entry, ok := s.files[path]
		if !ok {
			continue
		}

		var reader *io.SectionReader
		if r.Method != "HEAD" {
			f, err := os.Open(s.path)
			if err != nil {
				log.Printf(`ERROR: %s`, err.Error())
				http.Error(w, "500 Internal Server Error", http.StatusInternalServerError)
				return
			}
			defer f.Close()
			reader = io.NewSectionReader(f, entry.offset, entry.size)
		}

		if entry.method != 0 {
			// prefer gzip wrapping because it has CRC
			switch {
			case hasGzip:
				entry.handleGzip(w, reader)
			case hasDeflate:
				entry.handleRaw(w, reader)
			default:
				entry.handleInflate(w, reader)
			}
		} else {
			entry.handleRaw(w, reader)
		}

		return
	}

	http.NotFound(w, r)
}

type LoggingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *LoggingResponseWriter) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
	w.status = code
}

func logHandler(w http.ResponseWriter, r *http.Request) {
	wl := &LoggingResponseWriter{w, -1}
	handler(wl, r)

	encoding := wl.Header().Get("Content-Encoding")
	if len(encoding) == 0 {
		encoding = "-"
	}

	length := wl.Header().Get("Content-Length")
	if len(length) == 0 {
		length = "0"
	}

	log.Printf(`%s %s "%s %s %s" %d %s "%s" "%s" "%s"`,
		r.RemoteAddr, r.Host, r.Method, r.RequestURI, r.Proto,
		wl.status, length, encoding, r.Referer(), r.UserAgent())
}

func scanpak(name string) (*SearchPath, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var header struct {
		Ident  uint32
		Dirofs uint32
		Dirlen uint32
	}
	if err := binary.Read(f, binary.LittleEndian, &header); err != nil {
		return nil, err
	}
	if header.Ident != 'P'|'A'<<8|'C'<<16|'K'<<24 {
		return nil, errors.New("pak: bad ident")
	}
	if _, err := f.Seek(int64(header.Dirofs), io.SeekStart); err != nil {
		return nil, err
	}

	numFiles := int(header.Dirlen / 64)
	search := &SearchPath{name, make(map[string]PakFileEntry, numFiles)}
	for i := 0; i < numFiles; i++ {
		var entry struct {
			Name    [56]byte
			Filepos uint32
			Filelen uint32
		}
		if err := binary.Read(f, binary.LittleEndian, &entry); err != nil {
			return nil, err
		}
		b := bytes.IndexByte(entry.Name[:], 0)
		if b < 0 {
			b = len(entry.Name)
		}
		search.files[strings.ToLower(string(entry.Name[:b]))] = PakFileEntry{
			0, int64(entry.Filepos), int64(entry.Filelen), 0, 0, 0,
		}
	}
	return search, nil
}

func scanzip(name string) (*SearchPath, error) {
	r, err := zip.OpenReader(name)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	search := &SearchPath{name, make(map[string]PakFileEntry, len(r.File))}
	for _, f := range r.File {
		ofs, err := f.DataOffset()
		if err != nil {
			log.Println(err)
			continue
		}
		if f.Mode()&os.ModeDir != 0 {
			continue
		}
		search.files[strings.ToLower(f.Name)] = PakFileEntry{
			f.Method, int64(ofs), int64(f.CompressedSize64),
			f.CRC32, uint32(f.UncompressedSize64), uint32(f.Modified.Unix()),
		}
	}
	return search, nil
}

func atoi(s string) (v int, err error) {
	f := strings.FieldsFunc(s, func(c rune) bool {
		return !unicode.IsDigit(c)
	})
	if len(f) > 0 {
		return strconv.Atoi(f[0])
	}
	return 0, strconv.ErrSyntax
}

func scandir(name string) []*SearchPath {
	sp, ok := dirCache[name]
	if ok {
		return sp
	}

	f, err := os.Open(name)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	n, err := f.Readdirnames(0)
	if err != nil {
		log.Fatal(err)
	}
	paks := make([]string, 0, len(n))
	for _, v := range n {
		l := strings.ToLower(v)
		if strings.HasSuffix(l, ".pak") || strings.HasSuffix(l, ".pkz") {
			paks = append(paks, v)
		}
	}

	sort.Slice(paks, func(i, j int) bool {
		a := strings.ToLower(paks[j])
		b := strings.ToLower(paks[i])
		p1 := strings.HasPrefix(a, "pak")
		p2 := strings.HasPrefix(b, "pak")
		switch {
		case p1 && p2:
			v1, err1 := atoi(a)
			v2, err2 := atoi(b)
			if err1 == nil && err2 == nil {
				return v1 < v2
			}
			return a < b
		case p1:
			return true
		case p2:
			return false
		default:
			return a < b
		}
	})

	sp = make([]*SearchPath, 0, len(paks)+1)
	for _, v := range paks {
		scan := scanpak
		if strings.HasSuffix(strings.ToLower(v), ".pkz") {
			scan = scanzip
		}
		s, err := scan(filepath.Join(name, v))
		if err != nil {
			log.Printf(`ERROR: scan "%s": %s`, v, err.Error())
			continue
		}
		sp = append(sp, s)
	}

	if len(dirWhiteList) > 0 {
		sp = append(sp, &SearchPath{name, nil})
	} else if len(sp) == 0 {
		log.Printf(`WARNING: directory "%s" ignored due to empty DirWhiteList`, name)
	}
	dirCache[name] = sp
	return sp
}

func loadConfig(name string) {
	f, err := os.Open(name)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	b, err := ioutil.ReadAll(f)
	if err != nil {
		log.Fatal(err)
	}
	if err = json.Unmarshal(b, &config); err != nil {
		log.Fatal(err)
	}
	for _, r := range config.PakWhiteList {
		pakWhiteList = append(pakWhiteList, regexp.MustCompile(r))
	}
	for _, r := range config.DirWhiteList {
		dirWhiteList = append(dirWhiteList, regexp.MustCompile(r))
	}
	if !config.LogTimeStamps {
		log.SetFlags(0)
	}
	if len(config.SearchPaths) == 0 {
		log.Fatal("No search paths configured")
	}
	for k, v := range config.SearchPaths {
		sp := make([]*SearchPath, 0)
		for _, d := range v {
			sp = append(sp, scandir(d)...)
		}
		if config.LogLevel >= LogLevelInfo {
			log.Printf(`Search path for "%s":`, k)
			for _, s := range sp {
				if s.files == nil {
					log.Println(s.path)
				} else {
					log.Printf("%s (%d files)", s.path, len(s.files))
				}
			}
			log.Println("--------------------")
		}
		searchPaths = append(searchPaths, &SearchPathMatch{regexp.MustCompile(k), sp})
	}
	refererCheck = regexp.MustCompile(config.RefererCheck)
}

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("Usage: %s <config>", os.Args[0])
	}
	loadConfig(os.Args[1])
	h := handler
	if config.LogLevel >= LogLevelDebug {
		h = logHandler
	}
	http.HandleFunc("/", h)
	log.Fatal(http.ListenAndServe(config.Listen, nil))
}
