Directory structure
===================

`/songs`: list of `song`s in library

`/albums`: list of `albums` in library

`/artists`: list of `artists` in library


`/{songs,albums,artists}/uuid`: data corresponding to the `artist`/`album`/`song`'s UUID

`/reload`: rescan library and rebuild database

song
----

```json
"uuid": "[uuid]"
"path": "[path to file in library]",
"tags": {
	"[key]": "[value]",
	...
}
```

album
-----

```json
"name": "[album name]",
"uuid": "[UUID]",
"artist": "[artist]",
"songs": [
	...
]
```

artist
------

```json
"name": "[artist name]",
"uuid": "[UUID]",
"albums": [
	...
],
"songs": [
	...
]
```
