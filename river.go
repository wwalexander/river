package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os/exec"
)

type river struct {
	library  string
	port uint16
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
	return r, nil
}

type song struct {
	tags map[string]string
}

func (s *song) readTags(container map[string]interface{}) {
	tagsRaw, ok := container["tags"]
	if !ok {
		return
	}
	for key, value := range tagsRaw.(map[string]interface{}) {
		s.tags[key] = value.(string)
	}
}

func (r *river) newSong(name string) (s *song, err error) {
	cmd := exec.Command(r.probeCmd, "-print_format", "json", "-show_streams", name)
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
		return nil, fmt.Errorf("'%s' does not contain an audio stream", name)
	}
	cmd = exec.Command(r.probeCmd, "-print_format", "json", "-show_format", name)
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
	s = &song{tags: make(map[string]string)}
	s.readTags(format.Format)
	for _, stream := range streams.Streams {
		s.readTags(stream)
	}
	return
}

func main() {
	var flagLibrary = flag.String("library", "", "the music library")
	var flagPort = flag.Uint("port", 8080, "the port to listen on")
	flag.Parse()
	if *flagLibrary == "" {
		log.Fatal("no library path specified")
	}
	_, err := newRiver(*flagLibrary, uint16(*flagPort))
	if err != nil {
		log.Fatal(err)
	}
}
