# sbx intro

A 17 second intro for sbx (1920x1080, 60 fps, with sound), made with
[fframes](https://fframes.studio): the video is Rust code that returns an SVG tree per frame.

The box is one object through every cut:

| scene | time | what happens |
| --- | --- | --- |
| `CodeScene` | 0.0 to 3.2 s | `Sandbox.create({ template: "python", ttl: "5m" })` is typed, and an outline closes around it |
| `SandboxScene` | 3.2 to 7.8 s | the outline becomes a sandbox, `python main.py` prints `4`, and its defaults are listed: network off, read-only root, uid 1000, a TTL counting down |
| `PoolScene` | 7.8 to 12.2 s | the box shrinks into a pool of 21; the backend switches docker, gvisor, kata, firecracker and the pool ripples with each switch |
| `OutroScene` | 12.2 to 17.0 s | the TTL runs out, the reaper clears the pool from the outside in, and the last box becomes the sbx mark |

Colors are the dark theme of the website (`website/src/app.css`), the mark is
`website/static/favicon.svg`, and the type is Geist and Geist Mono (OFL, see `FONTS-LICENSE.txt`).
The sound effects are synthesized by `scripts/sfx.py` (numpy), so there is no third-party audio.

| file | what |
| --- | --- |
| `src/lib.rs` | the video: background, scene list, the audio bed |
| `src/scenes.rs` | the four scenes and the sound effects placed on their timing constants |
| `src/ui.rs` | palette, motion curves, the box |
| `src/main.rs` | the command line (`fframes::cli`) |
| `media/` | fonts and sounds, embedded into the binary |

## Render

Needs Rust and the ffmpeg build dependencies listed in the
[fframes README](https://github.com/dmtrKovalenko/fframes#installation)
(on Debian or Ubuntu: `yasm nasm libx264-dev libx265-dev libopus-dev libclang-dev clang ninja-build libvpx-dev libasound2-dev`).

```sh
cd video/intro
cargo run --release -- render                  # writes out.mp4
```

It renders on the CPU backend, so it works on any machine without a GPU; the full video takes
about 30 seconds on 8 cores. If linking fails with `undefined symbol: x264_encoder_open_163`, the
prebuilt FFmpeg does not match your libx264; build FFmpeg from source once with
`FFMPEG_FORCE_BUILD=1 cargo build --release`.

## Work on it

```sh
cargo run --release -- timeline                       # scenes, duration, audio tracks
cargo run --release -- strip PoolScene -n 8           # contact sheet, writes strip.png
cargo run --release -- frame OutroScene@end           # full-size PNG into frames/
cargo run --release -- inspect --pops                 # clipped text, missing fonts, popping elements
cargo run --release -- audio analyze                  # loudness and peaks per scene
cargo run --release -- render SandboxScene --draft    # one scene, half resolution
```
