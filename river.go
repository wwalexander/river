package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
)

// #cgo LDFLAGS: -lavformat -lavutil
// #include "libavutil/dict.h"
// #include "libavformat/avformat.h"
import "C"

func tags(filename string) (tags map[string]string, err error) {
	var fmtCtx *C.AVFormatContext = nil
	if ret := C.avformat_open_input(&fmtCtx, C.CString("sintel.flac"), nil, nil); ret != 0 {
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

var filename = flag.String("filename", "", "the file to read tags from")

func main() {
	C.av_register_all()
	tags, err := tags(*filename)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tags)
}
