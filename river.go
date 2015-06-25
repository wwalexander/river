package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path"
)

// #cgo LDFLAGS: -lavformat -lavutil
// #include "libavutil/dict.h"
// #include "libavformat/avformat.h"
import "C"

func readTags(name string) (tags map[string]string, err error) {
	var fmtCtx *C.AVFormatContext
	if C.avformat_open_input(&fmtCtx, C.CString(name), nil, nil) != 0 {
		return nil, errors.New("C.avformat_open_input")
	}
	emptyCString := C.CString("")
	tags = make(map[string]string)
	for i := C.av_dict_get(fmtCtx.metadata, emptyCString, nil, C.AV_DICT_IGNORE_SUFFIX);
		i != nil;
		i = C.av_dict_get(fmtCtx.metadata, emptyCString, i, C.AV_DICT_IGNORE_SUFFIX) {
		tags[C.GoString(i.key)] = C.GoString(i.value)
	}
	C.avformat_close_input(&fmtCtx)
	return
}

var (
	library = flag.String("library", "", "the path to the library directory")
	port    = flag.Uint("port", 8080, "the port to listen on")
)

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
			fmt.Fprintln(w, tags)
		}
	}
}

func main() {
	flag.Parse()
	if *library == "" {
		log.Fatal("no library path specified")
	}
	C.av_register_all()
	http.HandleFunc("/", handler)
	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", *port), nil))
}
