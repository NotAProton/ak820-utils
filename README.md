# ak820-utils

Playful front-ends for the [ak820](https://github.com/NotAProton/ak820-go-drivers)
keyboard core. This repository owns no protocol or device code — everything
flows through the `ak820` binary: key geometry comes from
`ak820 agent-context`, and lighting updates stream through `ak820 pipe`.

## Tools

- **mole** — whack-a-mole on the physical keyboard. The target pulses
  white-hot to red as the bar drains, hits flash white, level-ups sweep a
  rainbow, and the terminal shows score and target cards with a game-over
  summary. Combos, golden moles (+1 life), lives, persistent high score.
- **visualizer** — audio on the keyboard and in the terminal, fed by
  [cava](https://github.com/karlstav/cava). Five modes with distinct color
  logic, cycled live with `m`/`tab`; quiet input fades to an idle drift
  instead of freezing:

  - **ripple** — bass beats fire concentric shockwaves from the center keys;
    treble shimmers coherently on the perimeter. Ripples stay circular by
    modeling the board's 2.8:1 physical aspect.
  - **bars** — the only literal equalizer: band from the key's x position,
    lit strictly by row height, with falling peak markers.
  - **bloom** — dual stereo bubbles under the left (E/D) and right (I/K)
    hands. The left bubble reads cool and the right reads warm, so stereo
    width shows as color.
  - **plasma** — the only flowing-hue mode: a drift field picks the color
    while the level picks the brightness. Mids rotate the hue.
  - **wings** — a bass comet: a bright front expands toward Esc and Enter
    with a fading tail; onsets add a leading-edge kick.

  Themes: synthwave, ocean, fire (`t` cycles). The terminal uses rounded
  cards, rainbow ribbons, and pills, and adapts to narrow windows.

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
ak820-utils visualizer --theme ocean  # m/tab cycles modes, t themes, q quits
```

## Dependencies

- [ak820](https://github.com/NotAProton/ak820-go-drivers) core binary in PATH
- cava for the visualizer

## License

[MIT](LICENSE)
