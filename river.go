package main

import (
	"bufio"
	"container/heap"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math"
	"math/big"
	"math/rand"
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
	certPath      = ".cert.pem"
	keyPath       = ".key.pem"
	dbPath        = ".db.json"
	streamDirPath = ".stream"
)

const (
	idLeastByte    int = 'a'
	idGreatestByte int = 'z'
	idLength           = 8
)

// codec represents an audio codec supported by ffmpeg/avconv.
type afmt struct {
	// codec is the format's codec name in ffmpeg/avconv.
	codec string
	// ext is the file extension for the codec's container format.
	ext string
	// args are the codec-specific ffmpeg/avconv arguments to use.
	args []string
}

var (
	afmts = map[string]afmt{
		"opus": {
			codec: "opus",
			ext:   "mp3",
			args: []string{
				"-b:a", "128000",
				"-compression_level", "0",
			},
		},
		"mp3": {
			codec: "libmp3lame",
			ext:   "mp3",
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
	// Tag represents the key-value metadata tags of the song.
	Tags map[string]string `json:"tags"`
	// encoding is used to delay stream operations while a streaming file
	// for the song is encoding.
	encoding sync.WaitGroup
}

func (s song) tag(key string) (tag string, ok bool) {
	tag, ok = s.Tags[strings.ToUpper(key)]
	if ok {
		return
	}
	tag, ok = s.Tags[strings.ToLower(key)]
	return
}

func (s song) artist() (artist string) {
	artist, _ = s.tag("ARTIST")
	return
}

func (s song) album() (album string) {
	album, _ = s.tag("ALBUM")
	return
}

func (s song) track() (track string) {
	track, ok := s.tag("track")
	if ok {
		return
	}
	track, ok = s.tag("TRACKNUMBER")
	return
}

type songHeap []*song

func (h songHeap) Len() int {
	return len(h)
}

func (h songHeap) Less(i, j int) bool {
	iArtist := h[i].artist()
	jArtist := h[j].artist()
	if iArtist != jArtist {
		return iArtist < jArtist
	}
	iAlbum := h[i].album()
	jAlbum := h[j].album()
	if iAlbum != jAlbum {
		return iAlbum < jAlbum
	}
	iTrack, iErr := strconv.Atoi(h[i].track())
	jTrack, jErr := strconv.Atoi(h[j].track())
	if jErr != nil {
		return true
	} else if iErr != nil {
		return false
	}
	return iTrack < jTrack
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
	// songs is the primary record of songs in the library. Keys are
	// song.Paths, and values are songs.
	Songs map[string]*song `json:"songs"`
	// songsByID is like songs, but indexed by song.ID instead of song.Path.
	SongsByID map[string]*song `json:"songsByID"`
	// songsSorted is a list of songs in sorted order. Songs are sorted by
	// artist, then album, then track number.
	SongsSorted []*song `json:"songsSorted"`
	// path is the path to the library directory.
	path string
	// password is the password used to authenticate HTTP requests.
	password string
	// convCmd is the command used to transcode source files.
	convCmd string
	// probeCmd is the command used to read metadata tags from source files.
	probeCmd string
	// reading is used to delay write operations while read operations are
	// occuring.
	reading sync.WaitGroup
	// writing is used to delay operations while a write operation is
	// occuring.
	writing sync.WaitGroup
	// streamRE is a regular expression used to match stream URLs.
	streamRE *regexp.Regexp
}

func (s *song) readTags(container map[string]interface{}) {
	tags, ok := container["tags"]
	if !ok {
		return
	}
	for k, v := range tags.(map[string]interface{}) {
		s.Tags[k] = v.(string)
	}
}

func (l library) newSong(path string) (s *song, err error) {
	cmd := exec.Command(l.probeCmd,
		"-print_format", "json",
		"-show_streams",
		path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		return
	}
	var streams struct {
		Streams []map[string]interface{}
	}
	if err = json.NewDecoder(stdout).Decode(&streams); err != nil {
		return
	}
	if err = cmd.Wait(); err != nil {
		return
	}
	audio := false
	for _, stream := range streams.Streams {
		if stream["codec_type"] == "audio" {
			audio = true
			break
		}
	}
	if !audio {
		return nil,
			fmt.Errorf("'%s' does not contain an audio stream",
				path)
	}
	cmd = exec.Command(l.probeCmd,
		"-print_format", "json",
		"-show_format",
		path)
	stdout, err = cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		return
	}
	var format struct {
		Format map[string]interface{}
	}
	if err = json.NewDecoder(stdout).Decode(&format); err != nil {
		return
	}
	if err = cmd.Wait(); err != nil {
		return
	}
	s = &song{
		Path: path,
		Tags: make(map[string]string),
	}
	if current, ok := l.Songs[path]; ok {
		s.ID = current.ID
	} else {
		idBytes := make([]byte, 0, idLength)
		for i := 0; i < cap(idBytes); i++ {
			idByte := byte(rand.Intn(idGreatestByte-idLeastByte) +
				idLeastByte)
			idBytes = append(idBytes, idByte)
		}
		s.ID = string(idBytes)
	}
	s.readTags(format.Format)
	for _, st := range streams.Streams {
		s.readTags(st)
	}
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
	db, err := os.OpenFile(dbPath, os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	defer db.Close()
	err = json.NewEncoder(db).Encode(l)
	return
}

func chooseCmd(a string, b string) (string, error) {
	_, erra := exec.LookPath(a)
	_, errb := exec.LookPath(b)
	if erra == nil {
		return a, nil
	} else if errb == nil {
		return b, nil
	}
	return "", fmt.Errorf("could not find '%s' or '%s' executable", a, b)
}

func newLibrary(path string, password string) (l *library, err error) {
	l = &library{
		path:     path,
		password: password,
	}
	db, err := os.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err = json.NewDecoder(db).Decode(l); err != nil {
		return nil, err
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
	streamRE, err := regexp.Compile(fmt.Sprintf("^\\/songs\\/[%c-%c]{%d}\\/.+$",
		idLeastByte,
		idGreatestByte,
		idLength))
	if err != nil {
		return nil, err
	}
	l.streamRE = streamRE
	os.Mkdir(streamDirPath, os.ModeDir)
	l.reload()
	return
}

func createKeys() (err error) {
	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	if !os.IsNotExist(certErr) || !os.IsNotExist(keyErr) {
		return
	}
	serialNumber, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return
	}
	notBefore := time.Now()
	template := x509.Certificate{
		SerialNumber: serialNumber,
		NotBefore:    notBefore,
		NotAfter:     notBefore.Add(365 * 24 * time.Hour),
	}
	priv, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		return
	}
	cert, err := x509.CreateCertificate(crand.Reader,
		&template,
		&template,
		&priv.PublicKey,
		priv)
	if err != nil {
		return
	}
	certOut, err := os.Create(certPath)
	if err != nil {
		return
	}
	if err = pem.Encode(certOut, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert,
	}); err != nil {
		return
	}
	keyOut, err := os.Create(keyPath)
	if err != nil {
		return
	}
	err = pem.Encode(keyOut, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	return
}

func (l *library) putSongs(w http.ResponseWriter, r *http.Request) {
	l.writing.Wait()
	l.reading.Wait()
	l.writing.Add(1)
	defer l.writing.Done()
	l.reload()
}

func (l library) getSongs(w http.ResponseWriter, r *http.Request) {
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
	json.NewEncoder(w).Encode(song)
}

func (l library) encode(dest string, src *song, af afmt) (err error) {
	src.encoding.Add(1)
	defer src.encoding.Done()
	args := []string{
		"-i", src.Path,
		"-f", af.ext,
	}
	args = append(args, af.args...)
	args = append(args, dest)
	if out, err := exec.Command(l.convCmd, args...).CombinedOutput(); err != nil {
		log.Println(string(out))
		if _, err = os.Stat(dest); err == nil {
			os.Remove(dest)
		}
	}
	return
}

func (l library) getStream(w http.ResponseWriter, r *http.Request) {
	s, ok := l.SongsByID[path.Base(path.Dir(r.URL.Path))]
	if !ok {
		httpError(w, http.StatusNotFound)
		return
	}
	ext := path.Base(r.URL.Path)
	af, ok := afmts[ext]
	if !ok {
		httpError(w, http.StatusNotFound)
	}
	s.encoding.Wait()
	streamPath := path.Join(streamDirPath, s.ID+"."+ext)
	_, err := os.Stat(streamPath)
	if err != nil && !os.IsNotExist(err) {
		httpError(w, http.StatusInternalServerError)
		return
	}
	if os.IsNotExist(err) && l.encode(streamPath, s, af) != nil {
		httpError(w, http.StatusInternalServerError)
		return
	}
	http.ServeFile(w, r, streamPath)
}

func (l *library) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//_, password, ok := r.BasicAuth()
	//if !ok || password != l.password {
	//	httpError(w, http.StatusUnauthorized)
	//	return
	//}
	switch {
	case r.URL.Path == "/songs":
		switch r.Method {
		case "PUT":
			l.putSongs(w, r)
			fallthrough
		case "GET":
			l.getSongs(w, r)
		default:
			httpError(w, http.StatusMethodNotAllowed)
		}
	case path.Dir(r.URL.Path) == "/songs":
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
	fpwfile := flag.String("pwfile",
		".password",
		"the file containing the server password")
	flag.Parse()
	if *flibrary == "" {
		log.Fatal("missing library flag")
	}
	pwFile, err := os.Open(*fpwfile)
	if err != nil {
		log.Fatal(err)
	}
	defer pwFile.Close()
	scanner := bufio.NewScanner(pwFile)
	if !scanner.Scan() {
		err = scanner.Err()
		if err != nil {
			log.Fatal(err)
		}
	}
	l, err := newLibrary(*flibrary, scanner.Text())
	if err != nil {
		log.Fatal(err)
	}
	if err = createKeys(); err != nil {
		log.Fatal(err)
	}
	http.Handle("/", l)
	log.Fatal(http.ListenAndServeTLS(fmt.Sprintf(":%d", *fport),
		certPath,
		keyPath,
		nil))
}
