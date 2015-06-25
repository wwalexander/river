This directory contains C code that uses libav* to transcode input files of any
supported audio codec to Opus and MP3. Once this code is complete, it will be
translated into `cgo` and moved into `river.go`.

`transcode_aac.c` contains the [sample code](https://www.ffmpeg.org/doxygen/2.2/transcode_aac_8c-example.html) from the FFmpeg documentation. `transcode.c` contains the Opus/MP3 code adapted from `transcode_aac.c`.
