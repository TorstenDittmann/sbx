//! The four shots. Times inside a scene are seconds from the scene's start; sound effects are
//! placed from the same constants that drive the motion.

use fframes::{AudioMap, AudioTimestamp::*, AudioTrack, Duration, FFramesContext, Frame, Scene, Svgr};

use crate::ui::*;

fn sfx(name: &'static str, at: f32, gain: f32) -> AudioTrack<'static> {
    AudioTrack::new(name, Second(at.max(0.0))..Eof).gain_db(gain + MIX_GAIN_DB)
}

/// A pulse from 0 up to 1 and back over `length` seconds, starting at `start`.
fn pulse(t: f32, start: f32, length: f32) -> f32 {
    let local = (t - start) / length;
    if (0.0..1.0).contains(&local) { (local * std::f32::consts::PI).sin() } else { 0.0 }
}

/// Scale `content` around (`cx`, `cy`).
fn scaled<'a>(cx: f32, cy: f32, scale: f32, opacity: f32, content: Svgr<'a>) -> Svgr<'a> {
    let transform = format!("translate({cx} {cy}) scale({scale}) translate({} {})", -cx, -cy);
    fframes::svgr!(<g transform={transform} opacity={opacity}>{content}</g>)
}

/// Monospace text typed onto a character grid. Each token is (text, color); whitespace tokens
/// only advance the column.
struct Typed<'t> {
    tokens: &'t [(&'static str, &'static str)],
    x: f32,
    baseline: f32,
    size: f32,
}

impl Typed<'_> {
    fn len(&self) -> usize {
        self.tokens.iter().map(|(text, _)| text.len()).sum()
    }

    /// The first `typed` characters, and the column where the cursor sits.
    fn render(&self, advance: f32, typed: usize) -> (Svgr<'static>, f32) {
        let char_w = advance * self.size / 100.0;
        let mut col = 0;
        let mut parts = Vec::new();
        for (text, color) in self.tokens {
            let shown = typed.saturating_sub(col).min(text.len());
            if shown > 0 && !text.trim().is_empty() {
                let x = self.x + col as f32 * char_w;
                parts.push(fframes::svgr!(
                    <text x={x} y={self.baseline} font-family={MONO} font-size={self.size} font-weight="400" fill={*color}>
                        {text[..shown].to_owned()}
                    </text>
                ));
            }
            col += text.len();
        }
        let cursor_x = self.x + typed.min(col) as f32 * char_w;
        (fframes::svgr!(<g>{parts}</g>), cursor_x)
    }
}

fn cursor(x: f32, baseline: f32, size: f32, advance: f32, opacity: f32) -> Svgr<'static> {
    let w = advance * size / 100.0;
    let h = size * 1.1;
    fframes::svgr!(<rect x={x} y={baseline - size * 0.86} width={w} height={h} rx="2" fill={ACCENT} opacity={opacity} />)
}

/// Seconds at which each non-space character of `lines` appears, typing at `cps` from `start`.
fn key_times(lines: &[Typed], start: f32, cps: f32) -> Vec<f32> {
    let mut times = Vec::new();
    let mut i = 0;
    for line in lines {
        for (text, _) in line.tokens {
            for c in text.chars() {
                if !c.is_whitespace() {
                    times.push(start + i as f32 / cps);
                }
                i += 1;
            }
        }
    }
    times
}

fn key_clicks(times: &[f32]) -> Vec<AudioTrack<'static>> {
    // The text types faster than a person can, so only about two keys in three make a sound.
    // Four key variants, a few milliseconds of jitter and some level variation keep
    // it from sounding like a metronome.
    const KEYS: [&str; 4] = ["key-1.wav", "key-2.wav", "key-3.wav", "key-4.wav"];
    times
        .iter()
        .enumerate()
        .filter(|(i, _)| i % 3 != 2)
        .map(|(i, at)| {
            let jitter = ((i * 37) % 11) as f32 * 0.0015;
            // A multiplicative hash spreads the four variants without a repeating cycle.
            let variant = (i.wrapping_mul(2_654_435_761) >> 16) % 4;
            sfx(KEYS[variant], *at + jitter, -20.0 + ((i * 7) % 5) as f32 * 0.9)
        })
        .collect()
}

// ---------------------------------------------------------------------------------------------
// 1. Code: the SDK call is typed, and the outline of a sandbox closes around it.

const CODE_TYPE_START: f32 = 0.35;
const CODE_CPS: f32 = 44.0;
const CODE_ENTER: f32 = 2.05;

const CODE_LINES: [&[(&str, &str)]; 4] = [
    &[("const", KEYWORD), (" ", ""), ("box", FG), (" ", ""), ("=", DIM), (" ", ""), ("await", KEYWORD), (" ", ""), ("Sandbox", CONSTANT), (".", DIM), ("create", FUNCTION), ("({", DIM)],
    &[("  ", ""), ("template", FG), (":", DIM), (" ", ""), ("\"python\"", STRING), (",", DIM)],
    &[("  ", ""), ("ttl", FG), (":", DIM), (" ", ""), ("\"5m\"", STRING), (",", DIM)],
    &[("})", DIM)],
];

fn code_lines() -> [Typed<'static>; 4] {
    let line = |i: usize| Typed { tokens: CODE_LINES[i], x: 460.0, baseline: 470.0 + i as f32 * 68.0, size: 44.0 };
    [line(0), line(1), line(2), line(3)]
}

#[derive(Debug)]
pub struct CodeScene;

impl Scene for CodeScene {
    fn duration(&self) -> Duration<'_> {
        Duration::Seconds(3.2)
    }

    fn audio(&self) -> AudioMap<'_> {
        let mut tracks = key_clicks(&key_times(&code_lines(), CODE_TYPE_START, CODE_CPS));
        tracks.push(sfx("enter.wav", CODE_ENTER, -13.0));
        tracks.push(sfx("draw.wav", CODE_ENTER, -17.0));
        AudioMap::from(tracks)
    }

    fn render_frame<'a>(&'a self, mut frame: Frame, ctx: &FFramesContext<'a, '_>) -> Svgr<'a> {
        let t = frame.seconds();
        let advance = mono_advance(&mut frame, ctx);

        let mut typed = ((t - CODE_TYPE_START).max(0.0) * CODE_CPS) as usize;
        let mut lines = Vec::new();
        let mut cursor_at = (460.0, 470.0);
        for line in code_lines() {
            let (svg, x) = line.render(advance, typed);
            if typed > 0 || lines.is_empty() {
                cursor_at = (x, line.baseline);
            }
            typed = typed.saturating_sub(line.len());
            lines.push(svg);
        }
        // Solid while typing, blinking while it waits, gone after Enter.
        let typing_done = CODE_TYPE_START + code_lines().iter().map(Typed::len).sum::<usize>() as f32 / CODE_CPS;
        let blink = if t < typing_done || ((t - typing_done) / 0.5) as usize % 2 == 0 { 1.0 } else { 0.0 };
        let cursor_opacity = if t < CODE_ENTER { blink * p(&frame, &OUT_03, 0.05) } else { 0.0 };

        // After Enter the code dims while the outline closes, then leaves.
        let code_opacity = (1.0 - 0.55 * p(&frame, &OUT_03, CODE_ENTER)) * (1.0 - p(&frame, &IN_025, 2.8));
        let pose = POSE_PANEL;
        let perimeter = 2.0 * (pose.w + pose.h) - (8.0 - 2.0 * std::f32::consts::PI) * pose.rx;
        let drawn = p(&frame, &INOUT_075, CODE_ENTER + 0.05);
        let dash_offset = perimeter * (1.0 - drawn);
        let panel_y = lerp(28.0, 0.0, p(&frame, &SOFT, 0.0));

        fframes::svgr!(<g transform={format!("translate(0 {panel_y})")} opacity={p(&frame, &OUT_05, 0.0)}>
            <rect x={pose.x} y={pose.y} width={pose.w} height={pose.h} rx={pose.rx} fill={PANEL} stroke={EDGE} stroke-width="2" />
            <g opacity={code_opacity}>
                <text x="460" y="356" font-family={MONO} font-size="28" font-weight="400" fill={DIM}>"agent.ts"</text>
                <path d="M400 388 H1520" stroke={LINE} stroke-width="2" />
                {lines}
            </g>
            {cursor(cursor_at.0, cursor_at.1, 44.0, advance, cursor_opacity)}
            <rect x={pose.x} y={pose.y} width={pose.w} height={pose.h} rx={pose.rx} fill="none"
                stroke={ACCENT} stroke-width={pose.stroke} stroke-dasharray={perimeter} stroke-dashoffset={dash_offset}
                opacity={if drawn > 0.0 { 1.0 } else { 0.0 }} />
        </g>)
    }
}

// ---------------------------------------------------------------------------------------------
// 2. Sandbox: the outline becomes a box, a command runs inside, and its defaults are listed.

const COMMAND: &[(&str, &str)] = &[("$", ACCENT), (" ", ""), ("python", FG), (" ", ""), ("main.py", FG)];
const COMMAND_START: f32 = 0.95;
const COMMAND_CPS: f32 = 28.0;
const COMMAND_ENTER: f32 = 1.6;
const OUTPUT_AT: f32 = 1.75;
const ROWS_START: f32 = 1.35;
const ROW_STAGGER: f32 = 0.14;
const SANDBOX_EXIT: f32 = 3.85;
const DASH_AT: f32 = 4.0;

fn command_line() -> Typed<'static> {
    Typed { tokens: COMMAND, x: 352.0, baseline: 520.0, size: 40.0 }
}

#[derive(Debug)]
pub struct SandboxScene;

impl Scene for SandboxScene {
    fn duration(&self) -> Duration<'_> {
        Duration::Seconds(4.6)
    }

    fn audio(&self) -> AudioMap<'_> {
        let mut tracks = key_clicks(&key_times(&[command_line()], COMMAND_START, COMMAND_CPS));
        tracks.push(sfx("enter.wav", COMMAND_ENTER, -15.0));
        tracks.push(sfx("plink-a.wav", OUTPUT_AT, -19.0));
        for i in 0..4 {
            tracks.push(sfx("switch.wav", ROWS_START + i as f32 * ROW_STAGGER, -27.0));
        }
        tracks.push(sfx("draw.wav", DASH_AT - 0.45, -24.0));
        AudioMap::from(tracks)
    }

    fn render_frame<'a>(&'a self, mut frame: Frame, ctx: &FFramesContext<'a, '_>) -> Svgr<'a> {
        let t = frame.seconds();
        let advance = mono_advance(&mut frame, ctx);

        let mut pose = POSE_PANEL.lerp(&POSE_SANDBOX, p(&frame, &SNAPPY, 0.0));
        let dash = p(&frame, &OUT_05, DASH_AT);
        pose.dash = lerp(0.5, POSE_SANDBOX.dash, dash);
        pose.dash_w = lerp(0.5, POSE_SANDBOX.dash_w, dash);

        // Inside the box.
        let inside = visible(&frame, 0.55, SANDBOX_EXIT + 0.05);
        let command = command_line();
        let typed = ((t - COMMAND_START).max(0.0) * COMMAND_CPS) as usize;
        let (command_svg, cursor_x) = command.render(advance, typed);
        let cursor_opacity = if t > 0.7 && t < OUTPUT_AT { 1.0 } else { 0.0 };
        let output_y = lerp(18.0, 0.0, p(&frame, &SNAPPY, OUTPUT_AT));

        // The defaults, each tied to the box by a line that draws out of it.
        let ttl_since = ROWS_START + 3.0 * ROW_STAGGER + 0.15;
        let ttl_left = 300 - (t - ttl_since).max(0.0) as i32;
        let ttl = format!("{}:{:02}", ttl_left / 60, ttl_left % 60);
        let rows: Vec<Svgr> = [("network", "off".to_owned()), ("root fs", "read-only".to_owned()), ("user", "uid 1000".to_owned()), ("ttl", ttl)]
            .into_iter()
            .enumerate()
            .map(|(i, (key, value))| {
                let start = ROWS_START + i as f32 * ROW_STAGGER;
                let baseline = 470.0 + i as f32 * 78.0;
                let line_y = baseline - 12.0;
                let reach = p(&frame, &OUT_05, start);
                let line_end = lerp(858.0, 980.0, reach);
                let exit = SANDBOX_EXIT + (3 - i) as f32 * 0.04;
                let shown = visible(&frame, start + 0.15, exit);
                let line_opacity = if reach > 0.0 { 1.0 - p(&frame, &IN_025, exit) } else { 0.0 };
                let rise = lerp(16.0, 0.0, p(&frame, &SNAPPY, start + 0.15));
                let value_color = if i == 3 { ACCENT } else { FG };
                fframes::svgr!(<g>
                    <g opacity={line_opacity}>
                        <path d={format!("M858 {line_y} H{line_end}")} stroke={EDGE} stroke-width="2" />
                        <circle cx="856" cy={line_y} r="5" fill={ACCENT} />
                    </g>
                    <g opacity={shown} transform={format!("translate(0 {rise})")} font-family={MONO} font-size="34" font-weight="400">
                        <text x="1010" y={baseline} fill={DIM}>{key}</text>
                        <text x="1230" y={baseline} fill={value_color}>{value}</text>
                    </g>
                </g>)
            })
            .collect();
        let heading_y = lerp(30.0, 0.0, p(&frame, &SOFT, 0.95));

        fframes::svgr!(<g>
            {sandbox_box(&pose, ACCENT, 1.0)}
            <g opacity={inside} font-family={MONO} font-weight="400">
                <text x="352" y="331" font-size="28" fill={DIM}>"sbx_3f2a"</text>
                <rect x="632" y="300" width="180" height="44" rx="22" fill={ACCENT_BG} />
                <text x="722" y="331" text-anchor="middle" font-size="28" fill={ACCENT}>"sbx-agent"</text>
                <path d="M314 372 H850" stroke={LINE} stroke-width="2" />
                {command_svg}
                {cursor(cursor_x, command.baseline, 40.0, advance, cursor_opacity)}
                <text x="352" y={585.0 + output_y} font-size="40" fill={FG} opacity={p(&frame, &OUT_03, OUTPUT_AT)}>"4"</text>
                <text x="352" y="770" font-size="28" fill={DIM}>"python · 5m"</text>
            </g>
            <g opacity={visible(&frame, 0.95, SANDBOX_EXIT + 0.12)} transform={format!("translate(0 {heading_y})")}>
                <text x="1006" y="362" font-family={SANS} font-size="72" font-weight="600" letter-spacing="-2" fill={FG}>"Closed by default."</text>
            </g>
            {rows}
        </g>)
    }
}

// ---------------------------------------------------------------------------------------------
// 3. Pool: the box shrinks into a pool of sandboxes; the backend changes and the pool ripples.

const COLS: usize = 7;
const ROWS: usize = 3;
const CENTER: (usize, usize) = (3, 1);
const POOL_SPAWN: f32 = 0.45;
const BACKENDS: [&str; 4] = ["docker", "gvisor", "kata", "firecracker"];
const BACKEND_ON: f32 = 1.5;
const SWITCHES: [f32; 3] = [2.2, 2.9, 3.6];
const POOL_EXIT: f32 = 3.95;
const HEADLINE: &str = "Same API on Docker, gVisor, Kata, or Firecracker.";

fn tile_pose(col: usize, row: usize) -> BoxPose {
    BoxPose { x: 420.0 + col as f32 * 160.0, y: 280.0 + row as f32 * 160.0, ..POSE_TILE }
}

fn distance(col: usize, row: usize) -> f32 {
    (col as f32 - CENTER.0 as f32).hypot(row as f32 - CENTER.1 as f32)
}

fn tiles() -> impl Iterator<Item = (usize, usize)> {
    (0..ROWS).flat_map(|row| (0..COLS).map(move |col| (col, row))).filter(|tile| *tile != CENTER)
}

fn spawn_at(col: usize, row: usize) -> f32 {
    POOL_SPAWN + distance(col, row) * 0.075
}

/// How much the pool is squeezed at `t` by the backend switches: a wave from left to right.
fn ripple(t: f32, col: usize) -> f32 {
    SWITCHES.iter().map(|at| pulse(t, at + col as f32 * 0.035, 0.34)).sum()
}

#[derive(Debug)]
pub struct PoolScene;

impl Scene for PoolScene {
    fn duration(&self) -> Duration<'_> {
        Duration::Seconds(4.4)
    }

    fn audio(&self) -> AudioMap<'_> {
        let notes = ["plink-a.wav", "plink-b.wav", "plink-c.wav"];
        let mut tracks: Vec<_> = tiles()
            .enumerate()
            .map(|(i, (col, row))| sfx(notes[i % 3], spawn_at(col, row), -27.0))
            .collect();
        tracks.push(sfx("switch.wav", BACKEND_ON, -20.0));
        tracks.extend(SWITCHES.iter().map(|at| sfx("switch.wav", *at, -15.0)));
        AudioMap::from(tracks)
    }

    fn render_frame<'a>(&'a self, mut frame: Frame, ctx: &FFramesContext<'a, '_>) -> Svgr<'a> {
        let t = frame.seconds();
        let advance = mono_advance(&mut frame, ctx);

        let squeeze = |col: usize| {
            let r = ripple(t, col);
            (1.0 - 0.08 * r, POSE_TILE.stroke + 2.0 * r)
        };

        let mut boxes: Vec<Svgr> = tiles()
            .map(|(col, row)| {
                let start = spawn_at(col, row);
                let (rip, stroke) = squeeze(col);
                let mut pose = tile_pose(col, row);
                pose.stroke = stroke;
                let scale = lerp(0.55, 1.0, p(&frame, &SNAPPY, start)) * rip;
                scaled(pose.cx(), pose.cy(), scale, p(&frame, &OUT_03, start), sandbox_box(&pose, ACCENT, 1.0))
            })
            .collect();
        let mut center = POSE_SANDBOX.lerp(&tile_pose(CENTER.0, CENTER.1), p(&frame, &SNAPPY, 0.0));
        let (rip, stroke) = squeeze(CENTER.0);
        center.stroke += stroke - POSE_TILE.stroke;
        boxes.push(scaled(center.cx(), center.cy(), rip, 1.0, sandbox_box(&center, ACCENT, 1.0)));

        // The headline, shrunk if needed to stay inside the margins.
        let mut size = 60;
        while size > 40 && sans_width(&mut frame, ctx, size, 600, HEADLINE) > 1536.0 {
            size -= 2;
        }
        let headline_y = lerp(30.0, 0.0, p(&frame, &SOFT, 0.85));

        // Backend pills, centered; the highlight springs from one to the next.
        let char_w = advance * 0.30;
        let widths: Vec<f32> = BACKENDS.iter().map(|b| b.len() as f32 * char_w + 64.0).collect();
        let gap = 18.0;
        let total: f32 = widths.iter().sum::<f32>() + gap * 3.0;
        let mut xs = Vec::new();
        let mut x = 960.0 - total / 2.0;
        for w in &widths {
            xs.push(x);
            x += w + gap;
        }
        let mut hx = xs[0];
        let mut hw = widths[0];
        for (i, at) in SWITCHES.iter().enumerate() {
            let k = p(&frame, &SNAPPY, *at);
            hx += (xs[i + 1] - xs[i]) * k;
            hw += (widths[i + 1] - widths[i]) * k;
        }
        let active = SWITCHES.iter().filter(|at| t >= **at + 0.06).count();
        let pills: Vec<Svgr> = BACKENDS
            .iter()
            .enumerate()
            .map(|(i, name)| {
                let start = 1.1 + i as f32 * 0.06;
                let rise = lerp(20.0, 0.0, p(&frame, &SNAPPY, start));
                let on = t >= BACKEND_ON && i == active;
                let x = xs[i];
                let w = widths[i];
                fframes::svgr!(<g opacity={p(&frame, &OUT_03, start)} transform={format!("translate(0 {rise})")}>
                    <rect x={x} y="813" width={w} height="64" rx="32" fill="none" stroke={EDGE} stroke-width="2" />
                    <text x={x + w / 2.0} y="856" text-anchor="middle" font-family={MONO} font-size="30" font-weight="400"
                        fill={if on { FG } else { DIM }}>{*name}</text>
                </g>)
            })
            .collect();

        fframes::svgr!(<g>
            {boxes}
            <g opacity={visible(&frame, 0.85, POOL_EXIT)} transform={format!("translate(0 {headline_y})")}>
                <text x="960" y="200" text-anchor="middle" font-family={SANS} font-size={size} font-weight="600" letter-spacing="-1.5" fill={FG}>{HEADLINE}</text>
            </g>
            <g opacity={1.0 - p(&frame, &IN_025, POOL_EXIT + 0.05)}>
                <rect x={hx} y="813" width={hw} height="64" rx="32" fill={ACCENT_BG} stroke={ACCENT} stroke-width="2"
                    opacity={p(&frame, &OUT_03, BACKEND_ON)} />
                {pills}
            </g>
        </g>)
    }
}

// ---------------------------------------------------------------------------------------------
// 4. Outro: the TTL runs out and the reaper clears the pool; the last box becomes the mark.

const REAP_START: f32 = 0.1;
const MARK_AT: f32 = 0.75;
const MARK_BACK_AT: f32 = 1.05;
const WORDMARK_AT: f32 = 1.3;
const TAGLINE_AT: f32 = 1.95;
const MARK: f32 = 220.0;
const MARK_GAP: f32 = 48.0;
const MARK_CY: f32 = 440.0;
const TAGLINE: &str = "Sandbox orchestration for the agentic era.";

fn reap_at(col: usize, row: usize) -> f32 {
    let farthest = distance(0, 0).max(distance(COLS - 1, ROWS - 1));
    REAP_START + (farthest - distance(col, row)) * 0.13
}

#[derive(Debug)]
pub struct OutroScene;

impl Scene for OutroScene {
    fn duration(&self) -> Duration<'_> {
        Duration::Seconds(4.8)
    }

    fn audio(&self) -> AudioMap<'_> {
        // One tick per ring of the pool, not per box.
        let mut rings: Vec<f32> = tiles().map(|(col, row)| reap_at(col, row)).collect();
        rings.sort_by(f32::total_cmp);
        rings.dedup_by(|a, b| (*a - *b).abs() < 0.03);
        let mut tracks: Vec<_> = rings.iter().map(|at| sfx("reap.wav", *at + 0.1, -22.0)).collect();
        tracks.push(sfx("thump.wav", MARK_BACK_AT, -8.0));
        tracks.push(sfx("plink-b.wav", TAGLINE_AT, -24.0));
        AudioMap::from(tracks)
    }

    fn render_frame<'a>(&'a self, mut frame: Frame, ctx: &FFramesContext<'a, '_>) -> Svgr<'a> {
        let t = frame.seconds();

        // Expired boxes go grey, then shrink away, outermost ring first.
        let reaped: Vec<Svgr> = tiles()
            .map(|(col, row)| {
                let start = reap_at(col, row);
                let gone = p(&frame, &IN_025, start + 0.1);
                let color = if t >= start { DIM } else { ACCENT };
                let pose = tile_pose(col, row);
                scaled(pose.cx(), pose.cy(), lerp(1.0, 0.6, gone), 1.0 - gone, sandbox_box(&pose, color, 1.0))
            })
            .collect();

        // The mark of `website/static/favicon.svg` (a 32 unit square), next to the wordmark.
        let unit = MARK / 32.0;
        let wordmark_w = sans_width(&mut frame, ctx, 200, 600, "sbx") - 12.0;
        let left = 960.0 - (MARK + MARK_GAP + wordmark_w) / 2.0;
        let mark_cx = left + MARK / 2.0;
        let inner = 15.6 * unit;
        let logo = BoxPose {
            x: mark_cx - inner / 2.0,
            y: MARK_CY - inner / 2.0,
            w: inner,
            h: inner,
            rx: 2.2 * unit,
            stroke: 2.2 * unit,
            dash: 7.0 * unit,
            dash_w: 2.2 * unit,
        };
        let morph = p(&frame, &SOFT, MARK_AT);
        let pose = tile_pose(CENTER.0, CENTER.1).lerp(&logo, morph);
        let back = p(&frame, &SOFT, MARK_BACK_AT);
        let back_scale = lerp(0.82, 1.0, back);
        let back_opacity = p(&frame, &OUT_05, MARK_BACK_AT);

        let word_x = left + MARK + MARK_GAP;
        let word_shift = lerp(-90.0, 0.0, p(&frame, &SOFT, WORDMARK_AT));
        let tagline_y = lerp(24.0, 0.0, p(&frame, &SOFT, TAGLINE_AT));
        let url_y = lerp(24.0, 0.0, p(&frame, &SOFT, TAGLINE_AT + 0.25));

        fframes::svgr!(<g>
            <defs>
                <clipPath id="wordmark">
                    <rect x={word_x - 12.0} y="240" width="1200" height="400" />
                </clipPath>
            </defs>
            {reaped}
            {scaled(mark_cx, MARK_CY, back_scale, back_opacity, fframes::svgr!(
                <rect x={mark_cx - MARK / 2.0} y={MARK_CY - MARK / 2.0} width={MARK} height={MARK} rx={7.0 * unit}
                    fill={MARK_BG} stroke={EDGE} stroke-width="2" />
            ))}
            {sandbox_box(&pose, ACCENT, 1.0 - morph.clamp(0.0, 1.0))}
            <g clip-path="url(#wordmark)">
                <text x={word_x + word_shift} y={MARK_CY + 74.0} font-family={SANS} font-size="200" font-weight="600"
                    letter-spacing="-6" fill={FG} opacity={p(&frame, &OUT_05, WORDMARK_AT)}>"sbx"</text>
            </g>
            <text x="960" y={690.0 + tagline_y} text-anchor="middle" font-family={SANS} font-size="54" font-weight="500"
                letter-spacing="-1" fill={FG} opacity={p(&frame, &OUT_05, TAGLINE_AT)}>{TAGLINE}</text>
            <text x="960" y={758.0 + url_y} text-anchor="middle" font-family={MONO} font-size="30" font-weight="400"
                fill={DIM} opacity={p(&frame, &OUT_05, TAGLINE_AT + 0.25)}>"github.com/TorstenDittmann/sbx"</text>
        </g>)
    }
}
