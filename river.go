package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os/exec"
	"path"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
)

type song struct {
	Path string            `json:"path"`
	Tags map[string]string `json:"tags"`
}

type river struct {
	Songs    map[string]*song `json:"songs"`
	db       *os.File
	library  string
	port     uint16
	convCmd  string
	probeCmd string
	json     []byte
}

func chooseCmd(a string, b string) (string, error) {
	if _, err := exec.LookPath(a); err != nil {
		if _, err := exec.LookPath(b); err != nil {
			return "", fmt.Errorf("could not find %s or %s executable", a, b)
		}
		return b, nil
	}
	return a, nil
}

func (s *song) readTags(container map[string]interface{}) {
	tagsRaw, ok := container["tags"]
	if !ok {
		return
	}
	for key, value := range tagsRaw.(map[string]interface{}) {
		s.Tags[key] = value.(string)
	}
}

func (r river) newSong(relPath string) (s *song, err error) {
	absPath := path.Join(r.library, relPath)
	cmd := exec.Command(r.probeCmd,
		"-print_format", "json",
		"-show_streams",
		absPath)
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
		return nil, fmt.Errorf("'%s' does not contain an audio stream", relPath)
	}
	cmd = exec.Command(r.probeCmd,
		"-print_format", "json",
		"-show_format",
		absPath)
	if stdout, err = cmd.StdoutPipe(); err != nil {
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
	s = &song{Path: relPath, Tags: make(map[string]string)}
	s.readTags(format.Format)
	for _, stream := range streams.Streams {
		s.readTags(stream)
	}
	return
}

func id() string {
	letters := []byte("abcdefghijklmnopqrstuvwxyz")
	rand.Seed(time.Now().UnixNano())
	idBytes := make([]byte, 0, 8)
	for i := 0; i < cap(idBytes); i++ {
		idBytes = append(idBytes, letters[rand.Intn(len(letters))])
	}
	return string(idBytes)
}

func (r *river) readDir(relDir string) (err error) {
	absDir := path.Join(r.library, relDir)
	fis, err := ioutil.ReadDir(absDir)
	if err != nil {
		return
	}
	for _, fi := range fis {
		relPath := path.Join(relDir, fi.Name())
		if fi.IsDir() {
			if err = r.readDir(relPath); err != nil {
				return
			}
		} else {
			s, err := r.newSong(relPath)
			if err != nil {
				continue
			}
			r.Songs[id()] = s
		}
	}
	return
}

func newRiver(library string, port uint16) (r *river, err error) {
	r = &river{library: library, port: port}
	convCmd, err := chooseCmd("ffmpeg", "avconv")
	if err != nil {
		return nil, err
	}
	r.convCmd = convCmd
	probeCmd, err := chooseCmd("ffprobe", "avprobe")
	if err != nil {
		return nil, err
	}
	r.probeCmd = probeCmd
	dbPath := "db.json"
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {	
		log.Println("reading songs into database")
		r.Songs = make(map[string]*song)
		if err = r.readDir(""); err != nil {
			return nil, err
		}
		db, err := os.OpenFile(dbPath, os.O_CREATE, 0200)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		if err = json.NewEncoder(db).Encode(r); err != nil {
			return nil, err
		}
	} else {
		db, err := os.Open(dbPath)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		if err = json.NewDecoder(db).Decode(r); err != nil {
			return nil, err
		}
	}
	return
}

type songsHandler struct {
	river
}

func (songsh songsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(songsh); err != nil {
		http.Error(w, "unable to encode song list", 500)
		return
	}
}

type songHandler struct {
	river
}

func (songh songHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	base := path.Base(r.URL.Path)
	ext := path.Ext(base)
	song, ok := songh.Songs[strings.TrimSuffix(base, ext)]
	if !ok {
		http.Error(w, "file not found", 404)
		return
	}
	var codec string
	var qFlag string
	var quality string
	var format string
	switch ext {
	case ".opus":
		codec = "opus"
		qFlag = "-compression_level"
		quality = "10"
		format = "opus"
		break
	case ".mp3":
		codec = "libmp3lame"
		qFlag = "-q"
		quality = "0"
		format = "mp3"
		break
	default:
		http.Error(w, "unsupported file extension", 403)
		return
	}
	cmd := exec.Command(songh.convCmd,
		"-i", path.Join(songh.library, song.Path),
		"-c", codec,
		qFlag, quality,
		"-f", format, "-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "unable to pipe output from encoder", 500)
		return
	}
	if err = cmd.Start(); err != nil {
		http.Error(w, "unable to start encoding file", 500)
		return
	}
	if _, err = io.Copy(w, stdout); err != nil {
		http.Error(w, "unable to stream file", 500)
		return
	}
	if err = cmd.Wait(); err != nil {
		http.Error(w, "error while encoding file", 500)
		return
	}
}

func (r river) serve() {
	http.Handle("/songs", songsHandler{r})
	http.Handle("/songs/", songHandler{r})
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", r.port), nil))
}

func main() {
	var flagLibrary = flag.String("library", "", "the music library")
	var flagPort = flag.Uint("port", 21313, "the port to listen on")
	flag.Parse()
	if *flagLibrary == "" {
		log.Fatal("no library path specified")
	}
	r, err := newRiver(*flagLibrary, uint16(*flagPort))
	if err != nil {
		log.Fatal(err)
	}
	r.serve()
}
