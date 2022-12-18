package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"gopkg.in/yaml.v3"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type PakFileEntry struct {
	offset  int64
	size    uint32 // raw (compressed) size
	filecrc uint32
	filelen uint32 // uncompressed size
	mtime   uint32
	method  uint16
}

type SearchPath struct {
	path  string
	files map[string]PakFileEntry
}

type CompiledSearchPath struct {
	match  *regexp.Regexp
	search []SearchPath
}

const (
	LogLevelError = iota
	LogLevelInfo
	LogLevelDebug
)

type ConfigSearchPath struct {
	Match  string   `yaml:"Match"`
	Search []string `yaml:"Search"`
}

type Config struct {
	Listen        string             `yaml:"Listen"`
	ListenTLS     string             `yaml:"ListenTLS"`
	CertFile      string             `yaml:"CertFile"`
	KeyFile       string             `yaml:"KeyFile"`
	ContentType   string             `yaml:"ContentType"`
	RefererCheck  string             `yaml:"RefererCheck"`
	PakBlackList  []string           `yaml:"PakBlackList"`
	DirWhiteList  []string           `yaml:"DirWhiteList"`
	SearchPaths   []ConfigSearchPath `yaml:"SearchPaths"`
	LogLevel      int                `yaml:"LogLevel"`
	LogTimeStamps bool               `yaml:"LogTimeStamps"`
}

var config = Config{Listen: ":8080", ContentType: "application/octet-stream"}

var (
	refererCheck     *regexp.Regexp
	pakBlackList     []*regexp.Regexp
	dirWhiteList     []*regexp.Regexp
	searchPaths      []CompiledSearchPath
	dirCache         map[string][]SearchPath
	searchPathsMutex sync.RWMutex
)

func (entry *PakFileEntry) handleGzip(w http.ResponseWriter, r *io.SectionReader) {
	var b [10]byte

	w.Header().Set("Content-Length", strconv.FormatInt(int64(entry.size)+18, 10))
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
	w.Header().Set("Content-Length", strconv.FormatInt(int64(entry.size), 10))
	if entry.method != 0 {
		// Send raw deflate stream (e.g. no zlib header/trailer).
		// This violates RFC 2616 but works with libcurl.
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
func findSearchPath(r *http.Request) (search []SearchPath, path string) {
	path = strings.ToLower(pathpkg.Clean(r.URL.Path))
	longest := 0

	searchPathsMutex.RLock()
	defer searchPathsMutex.RUnlock()

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

func matchRegexpList(list []*regexp.Regexp, s string) bool {
	for _, r := range list {
		if r.MatchString(s) {
			return true
		}
	}
	return false
}

func closeWithError(w http.ResponseWriter, r *http.Request, code int) {
	if r.ProtoAtLeast(1, 1) {
		w.Header().Set("Connection", "close")
	}
	w.WriteHeader(code)
}

func handler(w http.ResponseWriter, r *http.Request) {
	if filepath.Separator != '/' && strings.ContainsRune(r.URL.Path, filepath.Separator) {
		closeWithError(w, r, http.StatusForbidden)
		return
	}

	if !refererCheck.MatchString(r.Referer()) {
		closeWithError(w, r, http.StatusForbidden)
		return
	}

	search, path := findSearchPath(r)
	if search == nil || len(path) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	allowPak := !matchRegexpList(pakBlackList, path)
	allowDir := matchRegexpList(dirWhiteList, path)
	if !allowPak && !allowDir {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	hasGzip, hasDeflate := parseAcceptEncoding(r)

	for _, s := range search {
		if s.files == nil {
			// look in the directory tree
			if !allowDir {
				continue
			}
			f, err := os.Open(filepath.Join(s.path, path))
			if err == nil {
				w.Header().Set("Content-Type", config.ContentType)
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

		f, err := os.Open(s.path)
		if err != nil {
			continue
		}
		defer f.Close()

		var reader *io.SectionReader
		if r.Method != "HEAD" {
			reader = io.NewSectionReader(f, entry.offset, int64(entry.size))
		}

		w.Header().Set("Content-Type", config.ContentType)
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

	w.WriteHeader(http.StatusNotFound)
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

func normalizeName(n string) string {
	n = strings.ReplaceAll(n, `\`, `/`)
	n = pathpkg.Clean("/" + n)
	return strings.ToLower(n[1:])
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
		if entry.Filelen > math.MaxInt32 {
			return nil, errors.New("pak: bad directory")
		}
		b := bytes.IndexByte(entry.Name[:], 0)
		if b < 0 {
			b = len(entry.Name)
		}
		n := string(entry.Name[:b])
		search.files[normalizeName(n)] = PakFileEntry{
			offset: int64(entry.Filepos),
			size:   entry.Filelen,
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
		if f.CompressedSize == math.MaxUint32 || f.UncompressedSize == math.MaxUint32 {
			log.Printf(`WARNING: skipping oversize file "%s" in "%s"`, f.Name, name)
			continue
		}
		search.files[normalizeName(f.Name)] = PakFileEntry{
			offset:  ofs,
			size:    f.CompressedSize,
			filecrc: f.CRC32,
			filelen: f.UncompressedSize,
			mtime:   uint32(f.Modified.Unix()),
			method:  f.Method,
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

func scandir(name string) []SearchPath {
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

	sp = make([]SearchPath, 0, len(paks)+1)
	for _, v := range paks {
		scan := scanpak
		if strings.HasSuffix(strings.ToLower(v), ".pkz") {
			scan = scanzip
		}
		s, err := scan(filepath.Join(name, v))
		if err != nil {
			log.Printf(`ERROR: scan "%s": %s`, v, err)
			continue
		}
		sp = append(sp, *s)
	}

	if len(dirWhiteList) > 0 {
		sp = append(sp, SearchPath{name, nil})
	} else if len(sp) == 0 {
		log.Printf(`WARNING: directory "%s" ignored due to empty DirWhiteList`, name)
	}
	dirCache[name] = sp
	return sp
}

func loadConfig() {
	if len(os.Args) != 2 {
		log.Fatalf("Usage: %s <config>", os.Args[0])
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	b, err := ioutil.ReadAll(f)
	if err != nil {
		log.Fatal(err)
	}
	if err = yaml.Unmarshal(b, &config); err != nil {
		log.Fatal(err)
	}
	for _, r := range config.PakBlackList {
		pakBlackList = append(pakBlackList, regexp.MustCompile(r))
	}
	for _, r := range config.DirWhiteList {
		dirWhiteList = append(dirWhiteList, regexp.MustCompile(r))
	}
	refererCheck = regexp.MustCompile(config.RefererCheck)
	if len(config.SearchPaths) == 0 {
		log.Fatal("No search paths configured")
	}
	if len(config.Listen)+len(config.ListenTLS) == 0 {
		log.Fatal("At least one of Listen or ListenTLS must be set")
	}
	if len(config.ListenTLS) > 0 && (len(config.CertFile) == 0 || len(config.KeyFile) == 0) {
		log.Fatal("CertFile and KeyFile must be set if ListenTLS is set")
	}
	if config.LogTimeStamps {
		log.SetFlags(log.LstdFlags)
	}
}

func printSearchPath(match string, sp []SearchPath) {
	log.Printf(`Search path for "%s":`, match)
	for _, s := range sp {
		if s.files == nil {
			log.Println(s.path)
		} else {
			log.Printf("%s (%d files)", s.path, len(s.files))
		}
	}
	log.Println("--------------------")
}

func scanSearchPaths() {
	searchPathsMutex.Lock()
	defer searchPathsMutex.Unlock()

	searchPaths = make([]CompiledSearchPath, 0, len(config.SearchPaths))
	dirCache = make(map[string][]SearchPath)

	for _, cfg := range config.SearchPaths {
		sp := make([]SearchPath, 0)
		for _, dir := range cfg.Search {
			sp = append(sp, scandir(dir)...)
		}
		if config.LogLevel >= LogLevelInfo {
			printSearchPath(cfg.Match, sp)
		}
		searchPaths = append(searchPaths, CompiledSearchPath{regexp.MustCompile(cfg.Match), sp})
	}
}

func main() {
	log.SetFlags(0)

	loadConfig()
	scanSearchPaths()

	if config.LogLevel >= LogLevelDebug {
		http.HandleFunc("/", logHandler)
	} else {
		http.HandleFunc("/", handler)
	}

	if len(config.ListenTLS) > 0 {
		go func() { log.Fatal(http.ListenAndServeTLS(config.ListenTLS, config.CertFile, config.KeyFile, nil)) }()
	}

	if len(config.Listen) > 0 {
		go func() { log.Fatal(http.ListenAndServe(config.Listen, nil)) }()
	}

	waitForSignal()
}
