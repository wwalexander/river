package main

import (
	"container/heap"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
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
)

const (
	dbPath        = ".db.json"
	streamDirPath = ".stream"
)

const (
	idLeastByte    = 'a'
	idGreatestByte = 'z'
	idLength       = 8
)

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
	return h[i].Path < h[j].Path
}

func (h songHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *songHeap) Push(s interface{}) {
	*h = append(*h, s.(*song))
}

func (h *songHeap) Pop() interface{} {
	old := *h
	n := len(old)
	s := old[n-1]
	*h = old[0 : n-1]
	return s
}

// library represents a music library and server.
type library struct {
	// Songs is the primary record of songs in the library. Keys are
	// song.Paths, and values are songs.
	Songs map[string]*song `json:"songs"`
	// SongsByID is like songs, but indexed by song.ID instead of song.Path.
	SongsByID map[string]*song `json:"songsByID"`
	// SongsSorted is a list of songs in sorted order. Songs are sorted by
	// artist, then album, then track number.
	SongsSorted []*song `json:"songsSorted"`
	// path is the path to the library directory.
	path string
	// convCmd is the command used to transcode source files.
	convCmd string
	// probeCmd is the command used to read metadata tags from source files.
	probeCmd string
	// reading is used to delay database write operations while read
	// operations are occuring.
	reading sync.WaitGroup
	// writing is used to delay database read operations while a write
	// operation is occuring.
	writing sync.WaitGroup
	// encoding is used to delay stream operations while a streaming file
	// at the path represented by the key is encoding.
	encoding map[string]*sync.WaitGroup
	// songRE is a regular expression used to match song URLs.
	songRE *regexp.Regexp
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

func (l library) newSong(path string) (s *song, err error) {
	cmd := exec.Command(l.probeCmd,
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path)
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
			break
		}
	}
	if !audio {
		return nil, errors.New("no audio stream")
	}
	s = &song{
		Path: path,
	}
	if sOld, ok := l.Songs[s.Path]; ok {
		s.ID = sOld.ID
	} else {
		idBytes := make([]byte, 0, idLength)
		for i := 0; i < cap(idBytes); i++ {
			n, err := rand.Int(rand.Reader,
				big.NewInt(int64(idGreatestByte-idLeastByte)))
			if err != nil {
				return nil, err
			}
			idBytes = append(idBytes, byte(n.Int64()+idLeastByte))
		}
		s.ID = string(idBytes)
	}
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

func (l *library) reload() (err error) {
	newSongs := make(map[string]*song)
	newSongsByID := make(map[string]*song)
	h := &songHeap{}
	heap.Init(h)
	filepath.Walk(l.path, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		s, err := l.newSong(path)
		if err != nil {
			return nil
		}
		newSongs[path] = s
		newSongsByID[s.ID] = s
		heap.Push(h, s)
		return nil
	})
	l.Songs = newSongs
	l.SongsByID = newSongsByID
	l.SongsSorted = make([]*song, 0, len(l.SongsByID))
	for h.Len() > 0 {
		l.SongsSorted = append(l.SongsSorted, heap.Pop(h).(*song))
	}
	db, err := os.OpenFile(dbPath, os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return
	}
	defer db.Close()
	if err = json.NewEncoder(db).Encode(l); err != nil {
		return
	}
	filepath.Walk(streamDirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if _, ok := l.SongsByID[strings.TrimSuffix(info.Name(),
			filepath.Ext(name))]; !ok {
			os.Remove(path)
		}
		return nil
	})
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

func newLibrary(path string) (l *library, err error) {
	l = &library{
		path:     path,
		encoding: make(map[string]*sync.WaitGroup),
	}
	convCmd, err := chooseCmd("ffmpeg", "avconv")
	if err != nil {
		return nil, err
	}
	probeCmd, err := chooseCmd("ffprobe", "avprobe")
	if err != nil {
		return nil, err
	}
	l.convCmd = convCmd
	l.probeCmd = probeCmd
	songREPrefix := fmt.Sprintf("^\\/songs\\/[%c-%c]{%d}",
		idLeastByte,
		idGreatestByte,
		idLength)
	songRE, err := regexp.Compile(songREPrefix + "$")
	if err != nil {
		return nil, err
	}
	l.songRE = songRE
	streamRE, err := regexp.Compile(songREPrefix + "\\..+$")
	if err != nil {
		return nil, err
	}
	l.streamRE = streamRE
	os.Mkdir(streamDirPath, os.ModeDir)
	if db, err := os.Open(dbPath); err == nil {
		defer db.Close()
		if err = json.NewDecoder(db).Decode(l); err != nil {
			return nil, err
		}
	} else {
		l.Songs = make(map[string]*song)
		l.reload()
	}
	return
}

func (l *library) putSongs() {
	l.writing.Wait()
	l.reading.Wait()
	l.writing.Add(1)
	defer l.writing.Done()
	l.reload()
}

func (l library) getSongs(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	l.writing.Wait()
	l.reading.Add(1)
	defer l.reading.Done()
	json.NewEncoder(w).Encode(l.SongsSorted)
}

func httpError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func (l library) getSong(w http.ResponseWriter, r *http.Request) {
	_, id := filepath.Split(r.URL.Path)
	song, ok := l.SongsByID[id]
	if !ok {
		httpError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(song)
}

func (l library) encode(dest string, src *song, af afmt) (err error) {
	if _, ok := l.encoding[dest]; !ok {
		l.encoding[dest] = &sync.WaitGroup{}
	}
	encoding := l.encoding[dest]
	encoding.Add(1)
	defer encoding.Done()
	args := []string{
		"-i", src.Path,
		"-f", af.fmt,
	}
	args = append(args, af.args...)
	args = append(args, dest)
	if err = exec.Command(l.convCmd, args...).Run(); err != nil {
		if _, err = os.Stat(dest); err == nil {
			os.Remove(dest)
		}
	}
	return
}

func (l library) getStream(w http.ResponseWriter, r *http.Request) {
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
	streamPath := path.Join(streamDirPath, base)
	if _, ok := l.encoding[streamPath]; ok {
		l.encoding[streamPath].Wait()
	}
	_, err := os.Stat(streamPath)
	if err != nil && !os.IsNotExist(err) {
		httpError(w, http.StatusInternalServerError)
		return
	}
	if os.IsNotExist(err) && l.encode(streamPath, s, af) != nil {
		httpError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", af.mime)
	http.ServeFile(w, r, streamPath)
}

func (l *library) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/songs":
		switch r.Method {
		case "PUT":
			l.putSongs()
			fallthrough
		case "GET":
			l.getSongs(w)
		default:
			httpError(w, http.StatusMethodNotAllowed)
		}
	case l.songRE.MatchString(r.URL.Path):
		switch r.Method {
		case "GET":
			l.getSong(w, r)
		default:
			httpError(w, http.StatusMethodNotAllowed)
		}
	case l.streamRE.MatchString(r.URL.Path):
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
	l, err := newLibrary(*flibrary)
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/", l)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *fport), nil))
}
