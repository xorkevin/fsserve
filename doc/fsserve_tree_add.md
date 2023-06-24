## fsserve tree add

Adds content and updates the content tree

### Synopsis

Adds content and updates the content tree

```
fsserve tree add [flags]
```

### Options

```
      --contenttype string   content type of src
  -e, --enc stringArray      encoded versions of the file in the form of (code:filename)
  -f, --file string          destination filepath
  -h, --help                 help for add
  -s, --src string           file to add
```

### Options inherited from parent commands

```
  -b, --base string        static files base
      --config string      config file (default is .fsserve.json)
      --log-level string   log level (default "info")
```

### SEE ALSO

* [fsserve tree](fsserve_tree.md)	 - Manages the server content tree

