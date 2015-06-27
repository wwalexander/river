Directory structure
===================

`/artists`: list of `artists` in library
`/artists/[artist]`: list of `album`s by `artist`
`/albums`: list of `albums` in library
`/songs`: list of `song`s in library
`/reload`: rescan library and rebuild database

song
----

```json
"uris": [
	{
		"uri": "[uuid].[extension]",
		"type": "[MIME type]"
	},
	...	
],
"path": "[path to file in library]",
"tags": {
	"[key]": "value",
	...
}
```

album
-----

```json
"album": "[album name]",
"artist": "[artist]",
"songs": [
	...
]
```

artist
------

```json
"artist": "[artist name]",
"albums": [
	...
],
"songs": [
	...
]
```