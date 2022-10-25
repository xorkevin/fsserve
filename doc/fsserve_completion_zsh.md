## fsserve completion zsh

Generate the autocompletion script for zsh

### Synopsis

Generate the autocompletion script for the zsh shell.

If shell completion is not already enabled in your environment you will need
to enable it.  You can execute the following once:

	echo "autoload -U compinit; compinit" >> ~/.zshrc

To load completions in your current shell session:

	source <(fsserve completion zsh); compdef _fsserve fsserve

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
      --config string   config file (default is $XDG_CONFIG_HOME/.fsserve.yaml)
      --debug           turn on debug output
```

### SEE ALSO

* [fsserve completion](fsserve_completion.md)	 - Generate the autocompletion script for the specified shell

