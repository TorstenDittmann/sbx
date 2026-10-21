//! Palette, motion curves and the pieces every scene draws. The colors are the dark theme of
//! the sbx website (`website/src/app.css`).

use std::sync::{LazyLock, OnceLock};

use fframes::{
    AnimateRuntimeInput, FFramesContext, FontQuery, Frame, Svgr,
    animation::{AnimationRuntime, Easing},
};

pub const BG: &str = "#0b0c0c";
pub const PANEL: &str = "#131515";
pub const MARK_BG: &str = "#111313";
pub const FG: &str = "#ececec";
pub const DIM: &str = "#8a8f91";
pub const EDGE: &str = "#33393a";
pub const LINE: &str = "#1f2223";
pub const ACCENT: &str = "#43b6a9";
pub const ACCENT_BG: &str = "#0f2e2b";
pub const KEYWORD: &str = "#43b6a9";
pub const STRING: &str = "#e3a86b";
pub const FUNCTION: &str = "#c4a5ff";
pub const CONSTANT: &str = "#7fa8ff";

/// Added to every track: the levels below are relative, this sets the loudness of the mix.
pub const MIX_GAIN_DB: f32 = 9.0;

pub const SANS: &str = "Geist";
pub const MONO: &str = "Geist Mono";

/// The box is one object through every cut; these are its resting places.
pub struct BoxPose {
    pub x: f32,
    pub y: f32,
    pub w: f32,
    pub h: f32,
    pub rx: f32,
    pub stroke: f32,
    /// Length and thickness of the dash in the middle (the dash of the sbx mark).
    pub dash: f32,
    pub dash_w: f32,
}

/// Around the code panel at the end of `CodeScene`.
pub const POSE_PANEL: BoxPose = BoxPose { x: 400.0, y: 300.0, w: 1120.0, h: 460.0, rx: 24.0, stroke: 3.0, dash: 0.5, dash_w: 12.0 };
/// The sandbox in `SandboxScene`.
pub const POSE_SANDBOX: BoxPose = BoxPose { x: 312.0, y: 270.0, w: 540.0, h: 540.0, rx: 24.0, stroke: 3.0, dash: 120.0, dash_w: 12.0 };
/// The middle tile of the pool in `PoolScene`.
pub const POSE_TILE: BoxPose = BoxPose { x: 900.0, y: 440.0, w: 120.0, h: 120.0, rx: 18.0, stroke: 2.5, dash: 27.0, dash_w: 4.0 };

pub fn lerp(a: f32, b: f32, t: f32) -> f32 {
    a + (b - a) * t
}

impl BoxPose {
    pub fn lerp(&self, to: &BoxPose, t: f32) -> BoxPose {
        BoxPose {
            x: lerp(self.x, to.x, t),
            y: lerp(self.y, to.y, t),
            w: lerp(self.w, to.w, t),
            h: lerp(self.h, to.h, t),
            rx: lerp(self.rx, to.rx, t),
            stroke: lerp(self.stroke, to.stroke, t),
            dash: lerp(self.dash, to.dash, t),
            dash_w: lerp(self.dash_w, to.dash_w, t),
        }
    }

    pub fn cx(&self) -> f32 {
        self.x + self.w / 2.0
    }

    pub fn cy(&self) -> f32 {
        self.y + self.h / 2.0
    }
}

/// The box with its dash. `fill_opacity` fades the panel color inside it.
pub fn sandbox_box(pose: &BoxPose, stroke: &str, fill_opacity: f32) -> Svgr<'static> {
    let (cx, cy) = (pose.cx(), pose.cy());
    let dash = pose.dash.max(0.5);
    let d = format!("M{} {cy} H{}", cx - dash / 2.0, cx + dash / 2.0);
    fframes::svgr!(<g>
        <rect x={pose.x} y={pose.y} width={pose.w.max(0.5)} height={pose.h.max(0.5)} rx={pose.rx}
            fill={PANEL} fill-opacity={fill_opacity} stroke={stroke.to_owned()} stroke-width={pose.stroke} />
        <path d={d} stroke={FG} stroke-width={pose.dash_w} stroke-linecap="round" />
    </g>)
}

// Motion curves, built once. Springs stop at their own settle time; the duration only caps them.
pub static OUT_03: LazyLock<AnimationRuntime> = LazyLock::new(|| AnimationRuntime::new(0.3, &Easing::EaseOut));
pub static OUT_05: LazyLock<AnimationRuntime> = LazyLock::new(|| AnimationRuntime::new(0.5, &Easing::EaseOut));
pub static IN_025: LazyLock<AnimationRuntime> = LazyLock::new(|| AnimationRuntime::new(0.25, &Easing::EaseIn));
pub static INOUT_075: LazyLock<AnimationRuntime> = LazyLock::new(|| AnimationRuntime::new(0.75, &Easing::EaseInOut));
pub static SNAPPY: LazyLock<AnimationRuntime> = LazyLock::new(|| {
    AnimationRuntime::new(2.0, &Easing::Spring { mass: 1.0, stiffness: 300.0, damping: 26.0 })
});
pub static SOFT: LazyLock<AnimationRuntime> = LazyLock::new(|| {
    AnimationRuntime::new(2.5, &Easing::Spring { mass: 1.0, stiffness: 150.0, damping: 18.0 })
});

/// 0 to 1 on `curve`, starting `start` seconds into the scene.
pub fn p(frame: &Frame, curve: &AnimationRuntime, start: f32) -> f32 {
    frame.animate_runtime(AnimateRuntimeInput { on_second: start, from: 0.0, to: 1.0, animation_runtime: curve })
}

/// Enter (ease-out from `start`) and leave (ease-in from `end`) as one opacity.
pub fn visible(frame: &Frame, start: f32, end: f32) -> f32 {
    p(frame, &OUT_05, start) * (1.0 - p(frame, &IN_025, end))
}

/// Width of one Geist Mono character at 100 px; the code is laid out on a character grid.
pub fn mono_advance(frame: &mut Frame, ctx: &FFramesContext) -> f32 {
    static ADVANCE: OnceLock<f32> = OnceLock::new();
    if let Some(a) = ADVANCE.get() {
        return *a;
    }
    let font = FontQuery { family: MONO, size: 100, weight: 400, ..Default::default() };
    match frame.text_width(ctx, font, "0000000000") {
        Some(w) => *ADVANCE.get_or_init(|| w as f32 / 10.0),
        None => 60.0,
    }
}

/// Width of a Geist string, measured once per (size, weight, text).
pub fn sans_width(frame: &mut Frame, ctx: &FFramesContext, size: usize, weight: u16, text: &'static str) -> f32 {
    use std::collections::HashMap;
    use std::sync::Mutex;
    static WIDTHS: LazyLock<Mutex<HashMap<(usize, u16, &'static str), f32>>> = LazyLock::new(Default::default);
    if let Some(w) = WIDTHS.lock().unwrap().get(&(size, weight, text)) {
        return *w;
    }
    let font = FontQuery { family: SANS, size, weight, ..Default::default() };
    match frame.text_width(ctx, font, text) {
        Some(w) => {
            WIDTHS.lock().unwrap().insert((size, weight, text), w as f32);
            w as f32
        }
        None => text.len() as f32 * size as f32 * 0.55,
    }
}
