# ak820-utils

Playful front-ends for the [ak820](https://github.com/NotAProton/ak820-go-drivers)
keyboard core. This repository owns no protocol or device code — everything
flows through the `ak820` binary: key geometry comes from
`ak820 agent-context`, and lighting updates stream through `ak820 pipe`.

## Tools

- **mole** — whack-a-mole on the physical keyboard. A key lights up, hit it
  before the bar empties. Combos, golden moles, lives, persistent high score.
- **visualizer** — audio on the keyboard and in the terminal, fed by
  [cava](https://github.com/karlstav/cava). Five geometric modes, cycled live
  with `m`/`tab`:

  - **ripple** — bass beats fire concentric shockwaves from the center keys;
    treble sparkles on the perimeter. Ripples stay circular by modeling the
    board's 2.8:1 physical aspect.
  - **bars** — straight vertical equalizer: band from the key's x position,
    lit strictly by row height, with falling peak markers.
  - **bloom** — dual stereo bubbles under the left (E/D) and right (I/K)
    hands expand and contract with the left/right channels, blending colors
    where they meet.
  - **plasma** — an undulating surface: bass drives height, mids drive phase,
    treble adds turbulence.
  - **wings** — bass pulses from the center keys and fans outward toward Esc
    and Enter.

  Themes: synthwave, ocean, fire (`t` cycles).

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
