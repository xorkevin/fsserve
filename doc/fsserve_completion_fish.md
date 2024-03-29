## fsserve completion fish

Generate the autocompletion script for fish

### Synopsis

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	fsserve completion fish | source

To load completions for every new session, execute once:

	fsserve completion fish > ~/.config/fish/completions/fsserve.fish

You will need to start a new shell for this setup to take effect.


```
fsserve completion fish [flags]
```

### Options

```
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
  -b, --base string        static files base
      --config string      config file (default is fsserve.json)
      --log-level string   log level (default "info")
      --log-plain          output plain text logs
```

### SEE ALSO

* [fsserve completion](fsserve_completion.md)	 - Generate the autocompletion script for the specified shell

