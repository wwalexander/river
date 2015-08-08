package main

import (
	"container/heap"
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
	"regexp"
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
)

const songIDLength = 8

// afmt represents an audio format supported by ffmpeg/avconv.
type afmt struct {
	// fmt is the format's name in ffmpeg/avconv.
	fmt string
	// mime is the MIME type of the format.
	mime string
	// args are the codec-specific ffmpeg/avconv arguments to use.
	args []string
}

var (
	afmts = map[string]afmt{
		".opus": {
			fmt:  "ogg",
			mime: "audio/ogg",
			args: []string{
				"-b:a", "128000",
				"-compression_level", "0",
			},
		},
		".mp3": {
			fmt:  "mp3",
			mime: "audio/mpeg",
			args: []string{
				"-q", "4",
			},
		},
	}
)

// song represents a song in a library.
type song struct {
	// ID is the unique ID of the song.
	ID string `json:"id"`
	// Path is the path to the song's source file.
	Path string `json:"path"`
	// Time is the last time the song's source file was modified.
	Time time.Time `json:"time"`
	// Artist is the song's artist.
	Artist string `json:"artist"`
	// Album is the album the song comes from.
	Album string `json:"album"`
	// Disc is the album disc the song comes from.
	Disc int `json:"disc"`
	// Track is the song's track number on the disc it comes from.
	Track int `json:"track"`
	// Title is the song's title.
	Title string `json:"title"`
}

type songHeap []*song

func (h songHeap) Len() int {
	return len(h)
}

func compareFold(s, t string) (eq bool, less bool) {
	sLower := strings.ToLower(s)
	tLower := strings.ToLower(t)
	return sLower == tLower, sLower < tLower
}

func (h songHeap) Less(i, j int) bool {
	if eq, less := compareFold(h[i].Artist, h[j].Artist); !eq {
		return less
	}
	if eq, less := compareFold(h[i].Album, h[j].Album); !eq {
		return less
	}
	if h[i].Disc != h[j].Disc {
		return h[i].Disc < h[j].Disc
	}
	if h[i].Track != h[j].Track {
		return h[i].Track < h[j].Track
	}
	if eq, less := compareFold(h[i].Title, h[j].Title); !eq {
		return less
	}
	_, less := compareFold(h[i].Path, h[j].Path)
	return less
}

func (h songHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *songHeap) Push(x interface{}) {
	s := x.(*song)
	*h = append(*h, s)
}

func (h *songHeap) Pop() interface{} {
	old := *h
	n := len(old)
	s := old[n-1]
	*h = old[0 : n-1]
	return s
}

// encoder encodes streaming files.
type encoder struct {
	// convCmd is the command used to transcode source files.
	convCmd string
	// encoding maps streaming filenames to mutexes. Encode operations wait for
	// a lock on the appropriate mutex before encoding.
	encoding map[string]*sync.Mutex
	// mutex is used to avoid concurrent writes to encoding.
	mutex sync.Mutex
}

func newEncoder(convCmd string) *encoder {
	return &encoder{
		convCmd:  convCmd,
		encoding: make(map[string]*sync.Mutex),
	}
}

func (e *encoder) encode(dest string, src string, af afmt) error {
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
		"-f", af.fmt,
	}
	args = append(args, af.args...)
	args = append(args, dest)
	if err = exec.Command(e.convCmd, args...).Run(); err != nil {
		if _, err = os.Stat(dest); err == nil {
			os.Remove(dest)
		}
	}
	return nil
}

// library represents a music library and server.
type library struct {
	// path is the path to the library directory.
	Path string `json:"path"`
	// Songs is the primary record of songs in the library. Keys are
	// song.Paths, and values are songs.
	Songs map[string]*song `json:"songs"`
	// SongsByID is like songs, but indexed by song.ID instead of song.Path.
	SongsByID map[string]*song `json:"songsByID"`
	// songsSorted is a list of songs in sorted order. Songs are sorted by
	// artist, then album, then track number.
	songsSorted []*song
	// probeCmd is the command used to read metadata tags from source files.
	probeCmd string
	// mutex is used to prevent concurrent write and read operations.
	mutex sync.RWMutex
	// enc is used to encode streaming files.
	enc *encoder
	// hash is the bcrypt hash of the library's password.
	hash []byte
	// streamRE is a regular expression used to match stream URLs.
	streamRE *regexp.Regexp
}

type tags struct {
	Format  map[string]interface{}
	Streams []map[string]interface{}
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

func (l library) absPath(path string) string {
	return filepath.Join(l.Path, path)
}

func (l library) relPath(path string) (rel string, err error) {
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

func (l library) newSong(path string) (s *song, err error) {
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
	if score := t.Format["probe_score"].(float64); score < 25 {
		return nil, errors.New("undeterminable file type")
	}
	audio := false
	for _, stream := range t.Streams {
		if stream["codec_type"] == "audio" {
			audio = true
		}
	}
	if !audio {
		return nil, errors.New("no audio stream")
	}
	s = &song{
		Path: path,
	}
	sOld, ok := l.Songs[s.Path]
	if ok {
		s.ID = sOld.ID
	} else {
		id, err := genID(songIDLength)
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

func deleteStream(s *song) (err error) {
	for ext, _ := range afmts {
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

func (l library) marshal() (err error) {
	db, err := os.OpenFile(marshalPath, os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return
	}
	defer db.Close()
	err = json.NewEncoder(db).Encode(l)
	return
}

func (l *library) reload() (err error) {
	filepath.Walk(l.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, err := l.relPath(path)
		if err != nil {
			return nil
		}
		sOld, ok := l.Songs[rel]
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
			l.Songs[rel] = s
			l.SongsByID[s.ID] = s
			deleteStream(s)
		}
		return nil
	})
	for path, s := range l.Songs {
		if _, err := os.Stat(l.absPath(path)); os.IsNotExist(err) {
			delete(l.Songs, path)
			delete(l.SongsByID, s.ID)
			deleteStream(s)
		}
	}
	l.songsSorted = make([]*song, 0, len(l.Songs))
	h := &songHeap{}
	heap.Init(h)
	for _, s := range l.Songs {
		heap.Push(h, s)
	}
	for h.Len() > 0 {
		l.songsSorted = append(l.songsSorted, heap.Pop(h).(*song))
	}
	err = l.marshal()
	return
}

func chooseCmd(s string, t string) (string, error) {
	_, errs := exec.LookPath(s)
	_, errt := exec.LookPath(t)
	if errs == nil {
		return s, nil
	} else if errt == nil {
		return t, nil
	}
	return "", fmt.Errorf("could not find '%s' or '%s' executable", s, t)
}

func newLibrary(path string, hash []byte) (l *library, err error) {
	l = &library{
		hash: hash,
	}
	l.probeCmd, err = chooseCmd("ffprobe", "avprobe")
	if err != nil {
		return nil, err
	}
	convCmd, err := chooseCmd("ffmpeg", "avconv")
	if err != nil {
		return nil, err
	}
	l.enc = newEncoder(convCmd)
	if l.streamRE, err = regexp.Compile(fmt.Sprintf("^\\/songs\\/[%c-%c]{%d}\\..+$",
		idLeastByte,
		idGreatestByte,
		songIDLength)); err != nil {
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
		l.Songs = make(map[string]*song)
		l.SongsByID = make(map[string]*song)
	}
	l.reload()
	return
}

func (l *library) putSongs(w http.ResponseWriter, r *http.Request) (success bool) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if l.reload() != nil {
		httpError(w, http.StatusInternalServerError)
	}
	return true
}

func (l library) getSongs(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	l.mutex.RLock()
	defer l.mutex.RUnlock()
	json.NewEncoder(w).Encode(l.songsSorted)
}

func (l library) optionsSongs(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Methods", "PUT, GET, OPTIONS")
	w.Header().Set("WWW-Authenticate", "Basic realm=\"River\"")
	w.WriteHeader(http.StatusOK)
}

func httpError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func streamPath(s *song, ext string) string {
	return filepath.Join(streamDirPath, s.ID) + ext
}

func (l library) getStream(w http.ResponseWriter, r *http.Request) {
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
	streamPath := streamPath(s, ext)
	if l.enc.encode(streamPath, l.absPath(s.Path), af) != nil {
		httpError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", af.mime)
	http.ServeFile(w, r, streamPath)
}

func (l *library) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization")
	streamREMatch := l.streamRE.MatchString(r.URL.Path)
	if !streamREMatch && r.Method != "OPTIONS" {
		_, password, ok := r.BasicAuth()
		if !ok || bcrypt.CompareHashAndPassword(l.hash,
			[]byte(password)) != nil {
			httpError(w, http.StatusUnauthorized)
			return
		}
	}
	switch {
	case r.URL.Path == "/songs":
		switch r.Method {
		case "OPTIONS":
			l.optionsSongs(w)
		case "PUT":
			if !l.putSongs(w, r) {
				return
			}
			fallthrough
		case "GET":
			l.getSongs(w)
		default:
			httpError(w, http.StatusMethodNotAllowed)
		}
	case streamREMatch:
		switch r.Method {
		case "GET":
			l.getStream(w, r)
		default:
			httpError(w, http.StatusMethodNotAllowed)
		}
	default:
		httpError(w, http.StatusNotFound)
	}
}

func main() {
	flibrary := flag.String("library", "", "the library directory")
	fport := flag.Uint("port", 21313, "the port to listen on")
	flag.Parse()
	if *flibrary == "" {
		log.Fatal("missing library flag")
	}
	fmt.Print("Enter a password: ")
	password, err := terminal.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		log.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		log.Fatal(err)
	}
	os.Mkdir(streamDirPath, os.ModeDir)
	l, err := newLibrary(*flibrary, hash)
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/", l)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *fport), nil))
}
