# AGENTS.md

## Build and test

```sh
make build    # vet, tests, builds bin/ak820-utils
make install  # builds and installs to ~/.local/bin
```

Vet and gofmt-clean must pass before committing.

## Architecture

This repository is a pure consumer of the ak820 core binary. It must not
open HID devices or encode protocol knowledge:

- key geometry: `ak820 agent-context` (`per_key` section)
- per-key transport: `ak820 pipe --yes` (stdin lines: `<cmd-hex> <payload-hex>`, frame addresses from `per_key`)
- mode changes / restore: `ak820 set mode ...`

The core module (github.com/NotAProton/ak820-go-drivers) is the single source
of truth for the protocol; change it there, not here.

## Conventions

Interactive commands must restore keyboard lighting on every exit path,
including panics and signals. Follow the Google developer documentation style
guide and keep documentation timeless.
