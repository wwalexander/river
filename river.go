package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path"
	"unsafe"
)

// #cgo LDFLAGS: -lavformat -lavutil
// #include <libavutil/dict.h>
// #include <libavformat/avformat.h>
import "C"

func readAVDictionary(dict *C.AVDictionary, tags map[string]string) {
	emptyCString := C.CString("")
	for tag := C.av_dict_get(dict, emptyCString, nil, C.AV_DICT_IGNORE_SUFFIX);
		tag != nil;
		tag = C.av_dict_get(dict, emptyCString, tag, C.AV_DICT_IGNORE_SUFFIX) {
		tags[C.GoString(tag.key)] = C.GoString(tag.value)
	}
}

func readTags(name string) (tags map[string]string, err error) {
	var fmtCtx *C.AVFormatContext
	if C.avformat_open_input(&fmtCtx, C.CString(name), nil, nil) != 0 {
		return nil, errors.New("C.avformat_open_input")
	}
	tags = make(map[string]string)
	readAVDictionary(fmtCtx.metadata, tags)
	streamPtr := uintptr(unsafe.Pointer(fmtCtx.streams))
	for i := 0; i < int(fmtCtx.nb_streams); i++ {
		readAVDictionary((*(**C.AVStream)(unsafe.Pointer(streamPtr))).metadata, tags)
		streamPtr += uintptr(i)
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
			fmt.Fprintf(w, "%s - %s\n", tags["ARTIST"], tags["TITLE"])
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
