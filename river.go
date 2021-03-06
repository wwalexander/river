package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh/terminal"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	marshalPath   = ".library.json"
	streamDirPath = ".stream"
)

const (
	idLeastByte    = 'a'
	idGreatestByte = 'z'
	idLength       = 8
)

const (
	fportName = "port"
	fcertName = "cert"
	fkeyName  = "key"
)

const (
	httpOptions = "OPTIONS"
	httpGet     = "GET"
	httpPut     = "PUT"
)

// Afmt represents an audio format supported by ffmpeg/avconv.
type Afmt struct {
	// Fmt is the format's name in ffmpeg/avconv.
	Fmt string
	// Codec is the format's codec in ffprobe/avprobe.
	Codec string
	// Encoder is the format's encoder in ffmpeg/avconv.
	Encoder string
	// Mime is the MIME type of the format.
	Mime string
	// Args are the encoder-specific ffmpeg/avconv arguments to use.
	Args []string
}

var afmts = map[string]Afmt{
	".opus": {
		Fmt:     "ogg",
		Codec:   "opus",
		Encoder: "libopus",
		Mime:    "audio/ogg; codecs=\"opus\"",
		Args: []string{
			"-b:a", "128000",
			"-compression_level", "0",
		},
	},
	".mp3": {
		Fmt:     "mp3",
		Codec:   "mp3",
		Encoder: "libmp3lame",
		Mime:    "audio/mpeg; codecs=\"mp3\"",
		Args: []string{
			"-q", "4",
		},
	},
}

// Song represents a song in a library.
type Song struct {
	// ID is the unique ID of the Song.
	ID string `json:"id"`
	// Path is the path to the Song's source file.
	Path string `json:"path"`
	// Time is the last time the Song's source file was modified.
	Time time.Time `json:"time"`
	// Artist is the Song's artist.
	Artist string `json:"artist"`
	// Album is the album the Song comes from.
	Album string `json:"album"`
	// Disc is the album disc the Song comes from.
	Disc int `json:"disc"`
	// Track is the Song's track number on the disc it comes from.
	Track int `json:"track"`
	// Title is the Song's title.
	Title string `json:"title"`
	// Fmt is the Song's format in ffmpeg/avconv.
	Fmt string `json:"fmt"`
	// Codec is the Song's codec in ffprobe/avprobe.
	Codec string `json:"codec"`
}

// ByTags sorts Songs case-insensitively with the following priority:
// artist, album, disc number, track number, title, library path
type ByTags []*Song

func (t ByTags) Len() int      { return len(t) }
func (t ByTags) Swap(i, j int) { t[i], t[j] = t[j], t[i] }

func compareFold(s, t string) (eq bool, less bool) {
	sLower := strings.ToLower(s)
	tLower := strings.ToLower(t)
	return sLower == tLower, sLower < tLower
}

func (t ByTags) Less(i, j int) bool {
	if eq, less := compareFold(t[i].Artist, t[j].Artist); !eq {
		return less
	}
	if eq, less := compareFold(t[i].Album, t[j].Album); !eq {
		return less
	}
	if t[i].Disc != t[j].Disc {
		return t[i].Disc < t[j].Disc
	}
	if t[i].Track != t[j].Track {
		return t[i].Track < t[j].Track
	}
	if eq, less := compareFold(t[i].Title, t[j].Title); !eq {
		return less
	}
	_, less := compareFold(t[i].Path, t[j].Path)
	return less
}

// Encoder encodes streaming files.
type Encoder struct {
	convCmd  string
	encoding map[string]*sync.Mutex
	mutex    *sync.Mutex
}

// NewEncoder returns a new Encoder which uses convCmd as the encoding tool.
func NewEncoder(convCmd string) *Encoder {
	return &Encoder{
		convCmd:  convCmd,
		encoding: make(map[string]*sync.Mutex),
		mutex:    &sync.Mutex{},
	}
}

// Encode encodes src to the audio format specified by af, writing to dest.
func (e *Encoder) Encode(s *Song, dest string, src string, af Afmt) error {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	mutex, ok := e.encoding[dest]
	if !ok {
		mutex = &sync.Mutex{}
		e.encoding[dest] = mutex
	}
	mutex.Lock()
	defer mutex.Unlock()
	_, err := os.Stat(dest)
	if err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	args := []string{
		"-i", src,
		"-codec", af.Encoder,
		"-metadata", fmt.Sprintf("artist=%s", s.Artist),
		"-metadata", fmt.Sprintf("album=%s", s.Album),
		"-metadata", fmt.Sprintf("disc=%d", s.Disc),
		"-metadata", fmt.Sprintf("track=%d", s.Track),
		"-metadata", fmt.Sprintf("title=%s", s.Title),
		"-f", af.Fmt,
	}
	args = append(args, af.Args...)
	args = append(args, dest)
	err = exec.Command(e.convCmd, args...).Run()
	if err != nil {
		if _, err := os.Stat(dest); err == nil {
			os.Remove(dest)
		}
	}
	return nil
}

// Library represents a music library and server.
type Library struct {
	// Path is the path to the library directory.
	Path string `json:"path"`
	// SongsByPath maps Song.Paths to Songs.
	SongsByPath map[string]*Song `json:"songsByPath"`
	// SongsByID maps Song.IDs to Songs.
	SongsByID map[string]*Song `json:"songsByID"`
	sorted    []*Song
	probeCmd  string
	mutex     *sync.RWMutex
	enc       *Encoder
	hash      []byte
	songRE    *regexp.Regexp
	streamRE  *regexp.Regexp
}

func isKind(val interface{}, kind reflect.Kind) bool {
	return reflect.TypeOf(val).Kind() == kind
}

func (l *Library) probeCmdError() error {
	return fmt.Errorf("malformed %s output", l.probeCmd)
}

type tags struct {
	Format  map[string]interface{}   `json:"format"`
	Streams []map[string]interface{} `json:"streams"`
}

func valRaw(key string, cont map[string]interface{}) (val string, ok bool) {
	tags, ok := cont["tags"].(map[string]interface{})
	if !ok {
		return
	}
	if val, ok = tags[strings.ToLower(key)].(string); ok {
		return val, ok
	}
	val, ok = tags[strings.ToUpper(key)].(string)
	return
}

func (t tags) val(key string) (val string, ok bool) {
	if val, ok := valRaw(key, t.Format); ok {
		return val, ok
	}
	for _, stream := range t.Streams {
		if val, ok := valRaw(key, stream); ok {
			return val, ok
		}
	}
	return
}

func valInt(valString string) (val int) {
	val, _ = strconv.Atoi(strings.Split(valString, "/")[0])
	return
}

func (l *Library) absPath(path string) string {
	return filepath.Join(l.Path, path)
}

func (l *Library) relPath(path string) (rel string, err error) {
	return filepath.Rel(l.Path, path)
}

func genID(length int) (string, error) {
	idBytes := make([]byte, 0, length)
	for i := 0; i < cap(idBytes); i++ {
		n, err := rand.Int(rand.Reader,
			big.NewInt(int64(idGreatestByte-idLeastByte)))
		if err != nil {
			return "", err
		}
		idBytes = append(idBytes, byte(n.Int64())+idLeastByte)
	}
	return string(idBytes), nil
}

func (l *Library) newSong(path string) (s *Song, err error) {
	abs := l.absPath(path)
	cmd := exec.Command(l.probeCmd,
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		abs)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		return
	}
	var t tags
	if err = json.NewDecoder(stdout).Decode(&t); err != nil {
		return
	}
	if err = cmd.Wait(); err != nil {
		return
	}
	score, ok := t.Format["probe_score"]
	if !ok || !isKind(score, reflect.Float64) {
		return nil, l.probeCmdError()
	}
	if score.(float64) < 25 {
		return nil, errors.New("undeterminable file type")
	}
	fmt, ok := t.Format["format_name"]
	if !ok || !isKind(fmt, reflect.String) {
		return nil, l.probeCmdError()
	}
	s = &Song{
		Path: path,
		Fmt:  fmt.(string),
	}
	audio := false
	for _, stream := range t.Streams {
		codecTypeRaw, ok := stream["codec_type"]
		if !ok || !isKind(codecTypeRaw, reflect.String) {
			return nil, l.probeCmdError()
		}
		if codecType := codecTypeRaw.(string); codecType == "audio" {
			audio = true
			codec := stream["codec_name"]
			if !ok || !isKind(codec, reflect.String) {
				return nil, l.probeCmdError()
			}
			s.Codec = codec.(string)
		}
	}
	if !audio {
		return nil, errors.New("no audio stream")
	}
	sOld, ok := l.SongsByPath[s.Path]
	if ok {
		s.ID = sOld.ID
	} else {
		id, err := genID(idLength)
		if err != nil {
			return nil, err
		}
		s.ID = id
	}
	songFile, err := os.Open(abs)
	if err != nil {
		return
	}
	fi, err := songFile.Stat()
	if err != nil {
		return
	}
	s.Time = fi.ModTime()
	songFile.Close()
	s.Artist, _ = t.val("artist")
	s.Album, _ = t.val("album")
	disc, ok := t.val("disc")
	if !ok {
		disc, _ = t.val("discnumber")
	}
	s.Disc = valInt(disc)
	track, ok := t.val("track")
	if !ok {
		track, _ = t.val("tracknumber")
	}
	s.Track = valInt(track)
	s.Title, _ = t.val("title")
	return
}

func deleteStream(s *Song) (err error) {
	for ext := range afmts {
		path := streamPath(s, ext)
		if _, err = os.Stat(path); err == nil {
			if err = os.Remove(path); err != nil {
				return
			}
		} else if !os.IsNotExist(err) {
			return
		}
	}
	return
}

func (l *Library) marshal() (err error) {
	db, err := os.OpenFile(marshalPath, os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return
	}
	defer db.Close()
	err = json.NewEncoder(db).Encode(l)
	return
}

func (l *Library) reload() (err error) {
	filepath.Walk(l.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := l.relPath(path)
		if err != nil {
			return nil
		}
		sOld, ok := l.SongsByPath[rel]
		reload := false
		if ok {
			fi, err := os.Stat(path)
			if err != nil {
				return nil
			}
			reload = fi.ModTime().After(sOld.Time)
		} else {
			reload = true
		}
		if reload {
			s, err := l.newSong(rel)
			if err != nil {
				return nil
			}
			l.SongsByPath[rel] = s
			l.SongsByID[s.ID] = s
			deleteStream(s)
		}
		return nil
	})
	for path, s := range l.SongsByPath {
		if _, err := os.Stat(l.absPath(path)); os.IsNotExist(err) {
			delete(l.SongsByPath, path)
			delete(l.SongsByID, s.ID)
			deleteStream(s)
		}
	}
	l.sorted = make(ByTags, 0, len(l.SongsByPath))
	for _, s := range l.SongsByPath {
		l.sorted = append(l.sorted, s)
	}
	sort.Sort(ByTags(l.sorted))
	err = l.marshal()
	return
}

func chooseCmd(s, t string) (string, error) {
	_, errs := exec.LookPath(s)
	_, errt := exec.LookPath(t)
	if errs == nil {
		return s, nil
	} else if errt == nil {
		return t, nil
	}
	return "", fmt.Errorf("could not find '%s' or '%s' executable", s, t)
}

// NewLibrary returns a new Library for path which requires an authentication
// password whose bcrypt hash compares with hash.
func NewLibrary(path string, hash []byte) (l *Library, err error) {
	l = &Library{
		hash:  hash,
		mutex: &sync.RWMutex{},
	}
	l.probeCmd, err = chooseCmd("ffprobe", "avprobe")
	if err != nil {
		return nil, err
	}
	convCmd, err := chooseCmd("ffmpeg", "avconv")
	if err != nil {
		return nil, err
	}
	l.enc = NewEncoder(convCmd)
	songREFmt := fmt.Sprintf("^\\/songs\\/[%c-%c]{%d}",
		idLeastByte,
		idGreatestByte,
		idLength)
	if l.songRE, err = regexp.Compile(songREFmt + "$"); err != nil {
		return nil, err
	}
	if l.streamRE, err = regexp.Compile(songREFmt + "\\..+$"); err != nil {
		return nil, err
	}
	if db, err := os.Open(marshalPath); err == nil {
		defer db.Close()
		if err = json.NewDecoder(db).Decode(l); err != nil {
			return nil, err
		}
	}
	if l.Path != path {
		l.Path = path
		l.SongsByPath = make(map[string]*Song)
		l.SongsByID = make(map[string]*Song)
	}
	l.reload()
	return
}

func httpError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func (l *Library) putSongs(w http.ResponseWriter, r *http.Request) error {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if err := l.reload(); err != nil {
		httpError(w, http.StatusInternalServerError)
		return err
	}
	return nil
}

func (l *Library) getSongs(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	json.NewEncoder(w).Encode(l.sorted)
}

func streamPath(s *Song, ext string) string {
	return filepath.Join(streamDirPath, s.ID) + ext
}

func (l *Library) getSong(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	s, ok := l.SongsByID[path.Base(r.URL.Path)]
	if !ok {
		httpError(w, http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(s)
}

func (l *Library) getStream(w http.ResponseWriter, r *http.Request) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	base := path.Base(r.URL.Path)
	ext := path.Ext(base)
	s, ok := l.SongsByID[strings.TrimSuffix(base, ext)]
	if !ok {
		httpError(w, http.StatusNotFound)
		return
	}
	af, ok := afmts[ext]
	if !ok {
		httpError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", af.Mime)
	absPath := l.absPath(s.Path)
	if s.Fmt == af.Fmt && s.Codec == af.Codec {
		http.ServeFile(w, r, absPath)
		return
	}
	streamPath := streamPath(s, ext)
	if l.enc.Encode(s, streamPath, absPath, af) != nil {
		httpError(w, http.StatusInternalServerError)
		return
	}
	http.ServeFile(w, r, streamPath)
}

func (l *Library) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization")
	if r.Method != httpOptions {
		_, password, ok := r.BasicAuth()
		if !ok ||
			bcrypt.CompareHashAndPassword(l.hash, []byte(password)) != nil {
			w.Header().Set("WWW-Authenticate", "Basic realm=\"River\"")
			httpError(w, http.StatusUnauthorized)
			return
		}
	}
	handle := func(methodHandlers map[string]func()) {
		switch r.Method {
		case httpOptions:
			allowedMethods := make([]string, 0, len(methodHandlers))
			for method := range methodHandlers {
				allowedMethods = append(allowedMethods, method)
			}
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(allowedMethods, ", "))
			w.WriteHeader(http.StatusOK)
		default:
			handler, ok := methodHandlers[r.Method]
			if !ok {
				httpError(w, http.StatusMethodNotAllowed)
				return
			}
			handler()
		}
	}
	switch {
	case r.URL.Path == "/songs":
		handle(map[string]func(){
			httpPut: func() {
				if err := l.putSongs(w, r); err != nil {
					return
				}
				l.getSongs(w)
			},
			httpGet: func() {
				l.getSongs(w)
			},
		})
	case l.songRE.MatchString(r.URL.Path):
		handle(map[string]func(){
			httpGet: func() {
				l.getSong(w, r)
			},
		})
	case l.streamRE.MatchString(r.URL.Path):
		handle(map[string]func(){
			httpGet: func() {
				l.getStream(w, r)
			},
		})
	default:
		httpError(w, http.StatusNotFound)
	}
}

func getHash() (hash []byte, err error) {
	fmt.Print("Enter a password: ")
	password, err := terminal.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return
	}
	hash, err = bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	return
}

const usage = `usage: river [-cert file] [-key file] [-port port] directory

river serves the music in the given directory. The music can be accessed via a
client on port 21313, or on the port named by the -port flag. If the -cert and
-key flags are specified, river will listen for HTTPS connections; otherwise,
river will listen for HTTP connections.`

func main() {
	fcert := flag.String(fcertName, "", "the TLS certificate to use")
	fkey := flag.String(fkeyName, "", "the TLS key to use")
	fport := flag.Uint(fportName, 21313, "the port to listen on")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
	}
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(1)
	}
	libraryPath := args[0]
	fcertSet := false
	fkeySet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == fcertName {
			fcertSet = true
		}
		if f.Name == fkeyName {
			fkeySet = true
		}
	})
	if (fcertSet && !fkeySet) || (!fcertSet && fkeySet) {
		flag.Usage()
		os.Exit(1)
	}
	noTLS := !fcertSet && !fkeySet
	if noTLS {
		log.Println("no TLS files specified: connections are insecure!")
	}
	hash, err := getHash()
	if err != nil {
		log.Fatal(err)
	}
	os.Mkdir(streamDirPath, os.ModeDir)
	l, err := NewLibrary(libraryPath, hash)
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/", l)
	addr := fmt.Sprintf(":%d", *fport)
	if noTLS {
		err = http.ListenAndServe(addr, nil)
	} else {
		err = http.ListenAndServeTLS(addr, *fcert, *fkey, nil)
	}
	log.Fatal(err)
}
