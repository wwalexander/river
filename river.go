package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

type song struct {
	Id   string            `json:"id"`
	Path string            `json:"path"`
	Tags map[string]string `json:"tags"`
}

type river struct {
	Songs    map[string]*song `json:"songs"`
	Library  string           `json:"library"`
	password string
	port     uint16
	convCmd  string
	probeCmd string
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
	absPath := path.Join(r.Library, relPath)
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
	s = &song{
		Id:   id(),
		Path: relPath,
		Tags: make(map[string]string),
	}
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
	absDir := path.Join(r.Library, relDir)
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
			r.Songs[s.Id] = s
		}
	}
	return
}

func (r *river) readLibrary() (err error) {
	log.Println("reading songs into database")
	r.Songs = make(map[string]*song)
	if err = r.readDir(""); err != nil {
		return
	}
	db, err := os.OpenFile("db.json", os.O_CREATE|os.O_TRUNC, 0200)
	if err != nil {
		return
	}
	defer db.Close()
	err = json.NewEncoder(db).Encode(r)
	return
}

type config struct {
	Library  string `json:"library"`
	Password string `json:"pass"`
	Port     uint16	`json:"port"`
}

func newRiver(c *config) (r *river, err error) {
	r = &river{
		password: c.Password,
		port: c.Port,
	}
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
		r.Library = c.Library
		r.readLibrary()
	} else {
		db, err := os.Open(dbPath)
		if err != nil {
			return nil, err
		}
		defer db.Close()
		if err = json.NewDecoder(db).Decode(r); err != nil {
			return nil, err
		}
		if r.Library != c.Library {
			r.Library = c.Library
			r.readLibrary()
		}
	}
	return
}

func (ri river) serveSongs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if err := json.NewEncoder(w).Encode(ri.Songs); err != nil {
		http.Error(w, "unable to encode song list", 500)
		return
	}
}

func (ri river) serveSong(w http.ResponseWriter, r *http.Request) {
	base := path.Base(r.URL.Path)
	ext := path.Ext(base)
	song, ok := ri.Songs[strings.TrimSuffix(base, ext)]
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
	cmd := exec.Command(ri.convCmd,
		"-i", path.Join(ri.Library, song.Path),
		"-c", codec,
		qFlag, quality,
		"-f", format,
		"-")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "unable to pipe output from encoder", 500)
		return
	}
	if err = cmd.Start(); err != nil {
		http.Error(w, "unable to start encoding file", 500)
		return
	}
	b, err := ioutil.ReadAll(stdout)
	if err != nil {
		http.Error(w, "error reading encoded file", 500)
		return
	}
	if err = cmd.Wait(); err != nil {
		http.Error(w, "error encoding file", 500)
		return
	}
	reader := bytes.NewReader(b)
	http.ServeContent(w, r, "", time.Time{}, reader)
}

func (ri river) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		ri.serveSongs(w, r)
	} else {
		ri.serveSong(w, r)
	}
}

func (r river) serve() {
	http.Handle("/", r)
	log.Println("ready")
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", r.port), nil))
}

func main() {
	flagConfig := flag.String("config", "config.json", "the configuration file")
	flagLibrary := flag.String("library", "", "the music library")
	flagPort := flag.Uint("port", 21313, "the port to listen on")
	flag.Parse()
	configFile, err := os.Open(*flagConfig)
	if err != nil {
		log.Fatalf("unable to open '%s'\n", *flagConfig)
	}
	var c config
	if err = json.NewDecoder(configFile).Decode(&c); err != nil {
		log.Fatalf("unable to parse '%s': %v", *flagConfig, err)
	}
	if c.Library == "" {
		c.Library = *flagLibrary
	}
	if c.Port == 0 {
		c.Port = uint16(*flagPort)
	}
	r, err := newRiver(&c)
	if err != nil {
		log.Fatal(err)
	}
	r.serve()
}
