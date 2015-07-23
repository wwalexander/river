river
=====

A personal music server

About
-----

River is a personal music streaming server that is designed to be an alternative
to proprietary streaming services. Users set up the River server on the computer
where they store their music library, and stream their songs via client
programs. (Custom clients can be built by using the small JSON API provided by
the server; I plan to build a reference iOS client soon). Songs in the user's
library can be streamed in the Ogg Opus or MP3 formats. When a stream is
requested, songs are automatically transcoded on-the-fly to the requested
format, if necessary.

River integrates with existing music libraries, so setting up River streams your
music without interfering or conflicting with other tools. The server is
designed to be simple to set up for anyone with the most basic experience with
the command line.

Running
-------

`river -library [path to library]`

### FFmpeg/LibAV

River calls `ffmpeg`/`avconv` and `ffprobe`/`avprobe` to transcode and read
audio files. If your operating system has a package manager, look for a package
called `ffmpeg` or `libav-tools` and install it. Otherwise, download an FFmpeg
build from [the official website](https://www.ffmpeg.org/download.html), and
either copy the `ffmpeg` and `ffprobe` executables to this directory, somewhere
in your system's PATH, or add the location of the executables to your system's
PATH. Windows and OS X releases should come with the executables bundled.

### Flags

`-library`: the library directory

`-port`: the port to listen on (defaults to 21313)
