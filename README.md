# ak820-utils

Playful front-ends for the [ak820](https://github.com/NotAProton/ak820-go-drivers)
keyboard core. This repository owns no protocol or device code — everything
flows through the `ak820` binary: key geometry comes from
`ak820 agent-context`, and lighting updates stream through `ak820 pipe`.

## Tools

- **mole** — whack-a-mole on the physical keyboard. A key lights up, hit it
  before the bar empties. Combos, golden moles, lives, persistent high score.
- **visualizer** — audio spectrum on the keyboard and in the terminal, fed by
  [cava](https://github.com/karlstav/cava). Themes: synthwave, ocean, fire.

## Install

Requires the [ak820](https://github.com/NotAProton/ak820-go-drivers) core in
PATH and Go 1.23+.

```sh
git clone https://github.com/NotAProton/ak820-utils
cd ak820-utils
make install    # builds, tests, installs to ~/.local/bin
```

## Usage

```sh
ak820-utils mole
AK820_UTILS_JSON=1 ak820-utils mole   # end-of-run stats as JSON
ak820-utils visualizer --theme ocean  # t cycles themes, q quits
```

## Dependencies

- [ak820](https://github.com/NotAProton/ak820-go-drivers) core binary in PATH
- cava for the visualizer

## License

[MIT](LICENSE)
