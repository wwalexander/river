river
=====

A personal music server

About
-----

River is a personal music streaming server that is designed to be an alternative
to proprietary streaming services. Users set up the River server on the computer
where they store their music library, and stream their songs via client
programs. Clients can be built by using the small JSON API provided by the
server. Songs in the user's library can be streamed in the Ogg Opus or MP3
formats (note that [all major browsers support at least one of these
formats](https://en.wikipedia.org/wiki/HTML5_Audio#Supported_browsers_2)).
When a stream is requested, songs are automatically transcoded to the requested
format, if necessary.

River integrates with existing music libraries, so setting up River streams your
music without interfering or conflicting with other tools. The server is
designed to be simple to set up for anyone with basic knowledge of the command
line who is capable of configuring dynamic DNS (or owns a static global IP).

Building
--------

    go build

### FFmpeg/LibAV

River calls `ffmpeg`/`avconv` and `ffprobe`/`avprobe` to transcode and read
audio files. If your operating system has a package manager, look for a package
called `ffmpeg` or `libav-tools` and install it. Otherwise, download an FFmpeg
build from [the official website](https://www.ffmpeg.org/download.html), and
either copy the `ffmpeg` and `ffprobe` executables to this directory, somewhere
in your system's PATH, or add the location of the executables to your system's
PATH. Windows and OS X (`darwin`) releases come with the executables bundled,
as they lack an official package manager.

Usage
-----

    river [-cert path] [-key path] [-port port] path

River serves the music located at the given path. The music can be accessed via
a client on the port specified with the -port flag, or the default port. If the
-cert and -key flags are specified, River will listen for HTTPS connections;
otherwise, River will listen for HTTP connections.

[river-web](https://github.com/wwalexander/river-web) is a browser-based River
client.

### API

Note that track and disc numbers should begin at `1`. Numbers lower than
`1` indicate that the field is missing or should be treated as such.

All API methods besides `OPTIONS` require basic authentication, where the
password matches the password given to the server by the user. Clients will need
to prompt the user for the password before accessing the API.

If you are building a browser client, note that direct `src` links to the stream
URLs will not work due to the required authentication. You can use URL-based
basic authentication (`protocol://:password@host...`) in `src` attributes, but
this is unsupported in Internet Explorer and probably other browsers soon. You
can use `URL.createObjectURL` in JavaScript instead, e.g.:

```javascript
var audio = document.createElement("audio");
var xhr = new XMLHttpRequest();

xhr.onload = function() {
	var source = document.createElement("source");
	source.src = URL.createObjectURL(xhr.response);
	source.type = "audio/ogg";
	audio.appendChild(source);
}

xhr.open("GET", "https://www.mydomain.com/songs/iswkgapo.opus");
xhr.responseType = "blob";
xhr.setRequestHeader("Authorization", "Basic " + btoa(":asanisimasa");
xhr.send();
```

However, this method requires the entire file to be downloaded before playback
begins.

#### Get a list of songs in the library

```http
GET /songs
```

```http
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
[
	{
		"id":     "iswkgapo",
		"path":   "Bob Dylan/Bringing It All Back Home/Mr. Tambourine Man.flac",
		"time":   "2015-05-28T10:06:04-08:00",
		"artist": "Bob Dylan",
		"album":  "Bringing It All Back Home",
		"disc":   2,
		"track":  1,
		"title":  "Mr. Tambourine Man",
		"fmt":    "flac",
		"codec":  "flac"
	},
	{
		"id":     "wybtohyc",
		"path":   "Neutral Milk Hotel/In the Aeroplane over the Sea/Holland, 1945.flac",
		"time":   "2015-05-28T10:06:04-08:00",
		"artist": "Neutral Milk Hotel",
		"album":  "In the Aeroplane over the Sea",
		"disc":   0,
		"track":  6,
		"title":  "Holland, 1945",
		"fmt":    "flac",
		"codec":  "flac"
	}
]
```

### Get info for a specific song in the library

```http
GET /songs/wybtohyc
```

```http
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
{
	"id":     "wybtohyc",
	"path":   "Neutral Milk Hotel/In the Aeroplane over the Sea/Holland, 1945.flac",
	"time":   "2015-05-28T10:06:04-08:00",
	"artist": "Neutral Milk Hotel",
	"album":  "In the Aeroplane over the Sea",
	"disc":   0,
	"track":  6,
	"title":  "Holland, 1945",
	"fmt":    "flac",
	"codec":  "flac"
}
```

#### Reload the library (i.e. after adding music or editing tags)

```http
PUT /songs
```

```http
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
[
	{
		"id":     "iswkgapo",
		"path":   "Bob Dylan/Bringing It All Back Home/Mr. Tambourine Man.flac",
		"time":   "2015-05-28T10:06:04-08:00",
		"artist": "Bob Dylan",
		"album":  "Bringing It All Back Home",
		"disc":   2,
		"track":  1,
		"title":  "Mr. Tambourine Man",
		"fmt":    "flac",
		"codec":  "flac"
	},
	{
		"id":     "ihnqqjce",
		"path":   "Neutral Milk Hotel/Ferris Wheel on Fire/Home.flac",
		"time":   "2015-05-28T10:06:04-08:00",
		"artist": "Neutral Milk Hotel",
		"album":  "Ferris Wheel on Fire",
		"disc":   0,
		"track":  3,
		"title":  "Home",
		"fmt":    "flac",
		"codec":  "flac"
	},
	{
		"id":     "wybtohyc",
		"path":   "Neutral Milk Hotel/In the Aeroplane over the Sea/Holland, 1945.flac",
		"time":   "2015-05-28T10:06:04-08:00",
		"artist": "Neutral Milk Hotel",
		"album":  "In the Aeroplane over the Sea",
		"disc":   0,
		"track":  6,
		"title":  "Holland, 1945",
		"fmt":    "flac",
		"codec":  "flac"
	}
]
```

#### Get the stream for a song

##### Opus

```http
GET /songs/wybtohyc.opus
```

```http
HTTP/1.1 200 OK
Content-Type: audio/ogg
```

##### MP3

```http
GET /songs/wybtohyc.mp3
```

```http
HTTP/1.1 200 OK
Content-Type: audio/mpeg
```
