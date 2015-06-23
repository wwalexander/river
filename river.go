package main

import (
	"errors"
	"fmt"
	"log"
	"os"
)

// #cgo LDFLAGS: -lavformat -lavutil
// #include "libavutil/dict.h"
// #include "libavformat/avformat.h"
import "C"

type tag struct {
	name string
	value string
}

type file struct {
	fmtCtx *C.AVFormatContext
}

func open(name string) (f file, err error) {
	var fmtCtx *C.AVFormatContext
	if ret := C.avformat_open_input(&fmtCtx, C.CString(name), nil, nil); ret != 0 {
		return file{}, errors.New("C.avformat_open_input")
	}
	return file{fmtCtx: fmtCtx}, nil
}

func (f file) close() {
	C.avformat_close_input(&f.fmtCtx)
}

func (f file) readTags() (tags map[string]string) {
	emptyCString := C.CString("")
	tags = make(map[string]string)
	for i := C.av_dict_get(f.fmtCtx.metadata, emptyCString, nil, C.AV_DICT_IGNORE_SUFFIX);
		i != nil;
		i = C.av_dict_get(f.fmtCtx.metadata, emptyCString, i, C.AV_DICT_IGNORE_SUFFIX) {
		tags[C.GoString(i.key)] = C.GoString(i.value)
	}
	return
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("no filename provided")
	}
	C.av_register_all()
	file, err := open(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(file.readTags())
}
