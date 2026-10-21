//! The sbx intro: a line of SDK code becomes a sandbox, the sandbox becomes a pool on four
//! backends, the reaper clears the pool, and the last box becomes the sbx mark.
//!
//! Each scene ends on exactly the frame the next one starts from, so the box is one object
//! across every cut. `cargo run --release -- timeline` lists the scenes.

mod scenes;
mod ui;

use fframes::{
    AudioMap, AudioTimestamp::*, AudioTrack, Color, Duration, FFramesContext, Frame, Scene, Scenes,
    Svgr, Video, include_media_dir,
};

pub use scenes::{CodeScene, OutroScene, PoolScene, SandboxScene};
use ui::*;

// Fonts (Geist, Geist Mono) and the synthesized sound effects from `scripts/sfx.py`.
include_media_dir!(pub struct SbxIntroMedia, "media");

pub const WIDTH: usize = 1920;
pub const HEIGHT: usize = 1080;

pub struct SbxIntroVideo<'a> {
    pub media: &'a SbxIntroMedia,
}

impl<'a> SbxIntroVideo<'a> {
    pub fn new(media: &'a SbxIntroMedia) -> Self {
        Self { media }
    }
}

impl std::fmt::Debug for SbxIntroVideo<'_> {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("SbxIntroVideo").finish()
    }
}

impl Video for SbxIntroVideo<'_> {
    const FPS: usize = 60;
    const WIDTH: usize = WIDTH;
    const HEIGHT: usize = HEIGHT;
    const BACKGROUND_COLOR: Color = Color::BLACK;

    fn duration(&self) -> Duration<'_> {
        Duration::Auto
    }

    fn audio(&self) -> AudioMap<'_> {
        // A quiet bed under everything; the scenes place their own effects.
        AudioMap::from([AudioTrack::new("pad.wav", Second(0.0)..Eof).gain_db(-21.0 + MIX_GAIN_DB)])
    }

    fn define_scenes(&self) -> Scenes<'_> {
        Scenes::from(vec![
            &CodeScene as &dyn Scene,
            &SandboxScene,
            &PoolScene,
            &OutroScene,
        ])
    }

    fn render_frame<'a>(&'a self, frame: Frame, ctx: &FFramesContext<'a, '_>) -> Svgr<'a> {
        // A teal glow drifts slowly behind everything, so holds never freeze.
        let t = frame.seconds();
        let glow_x = 960.0 + 260.0 * (t * 0.33).sin();
        let glow_y = 420.0 + 90.0 * (t * 0.27).cos();
        fframes::svgr!(
            <svg xmlns="http://www.w3.org/2000/svg" width={WIDTH} height={HEIGHT} viewBox="0 0 1920 1080">
                <defs>
                    <pattern id="dots" width="48" height="48" patternUnits="userSpaceOnUse">
                        <circle cx="24" cy="24" r="1.6" fill="#1b1e1f" />
                    </pattern>
                    <radialGradient id="glow" gradientUnits="userSpaceOnUse" cx={glow_x} cy={glow_y} r="980">
                        <stop offset="0" stop-color="#43b6a9" stop-opacity="0.085" />
                        <stop offset="1" stop-color="#43b6a9" stop-opacity="0" />
                    </radialGradient>
                    <radialGradient id="vignette" cx="0.5" cy="0.5" r="0.75">
                        <stop offset="0.55" stop-color="#000000" stop-opacity="0" />
                        <stop offset="1" stop-color="#000000" stop-opacity="0.55" />
                    </radialGradient>
                </defs>
                <rect width="1920" height="1080" fill={BG} />
                <rect width="1920" height="1080" fill="url(#dots)" />
                <rect width="1920" height="1080" fill="url(#glow)" />
                {ctx.render_scenes(&frame)}
                <rect width="1920" height="1080" fill="url(#vignette)" />
            </svg>
        )
    }
}
