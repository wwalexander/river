package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
	"path"
)

var avConvCmd string
var avProbeCmd string

func readTagsJSON(tags map[string]string, container map[string]interface{}) {
	tagsRaw, ok := container["tags"]
	if !ok {
		return
	}
	for key, value := range tagsRaw.(map[string]interface{}) {
		tags[key] = value.(string)
	}
}

func readTags(name string) (tags map[string]string, err error) {
	cmd := exec.Command(avProbeCmd, "-print_format", "json", "-show_format", name)
	stdout, err := cmd.StdoutPipe()
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
	tags = make(map[string]string)
	readTagsJSON(tags, format.Format)
	cmd = exec.Command(avProbeCmd, "-print_format", "json", "-show_streams", name)
	if stdout, err = cmd.StdoutPipe(); err != nil {
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
	for _, stream := range streams.Streams {
		readTagsJSON(tags, stream)
	}
	return
}

func findAVCmds() error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if _, err := exec.LookPath("avconv"); err != nil {
			return errors.New("could not find ffmpeg or avconv executable")
		}
		avConvCmd = "avconv"
	} else {
		avConvCmd = "ffmpeg"
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		if _, err := exec.LookPath("avprobe"); err != nil {
			return errors.New("could not find ffprobe or avprobe executable")
		}
		avProbeCmd = "avprobe"
	} else {
		avProbeCmd = "ffprobe"
	}
	return nil
}

var library = flag.String("library", "", "the path to the library directory")

func handler(w http.ResponseWriter, r *http.Request) {
	fis, err := ioutil.ReadDir(*library)
	if err != nil {
		fmt.Fprintln(w, "Error reading files")
		return
	}
	for _, fi := range fis {
		name := fi.Name()
		tags, err := readTags(path.Join(*library, name))
		if err != nil {
			fmt.Fprintf(w, "%s [%s]\n", name, err)
		} else {
			fmt.Fprintf(w, "%s - %s\n", tags["ARTIST"], tags["TITLE"])
		}
	}
}

func main() {
	var port = flag.Uint("port", 8080, "the port to listen on")
	flag.Parse()
	if *library == "" {
		log.Fatal("no library path specified")
	}
	if err := findAVCmds(); err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/", handler)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
