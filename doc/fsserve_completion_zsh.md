## fsserve completion zsh

Generate the autocompletion script for zsh

### Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(fsserve completion zsh)

To load completions for every new session, execute once:

#### Linux:

	fsserve completion zsh > "${fpath[1]}/_fsserve"

#### macOS:

	fsserve completion zsh > $(brew --prefix)/share/zsh/site-functions/_fsserve

You will need to start a new shell for this setup to take effect.


```
fsserve completion zsh [flags]
```

### Options

```
  -h, --help              help for zsh
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

