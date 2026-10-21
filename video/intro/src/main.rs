use fframes::cli::clap; // the derive below expands to `clap::...`
use fframes::{EncoderOptions, RenderOptions, StaticMediaProvider, cli};
use sbx_intro::{SbxIntroMedia, SbxIntroVideo};
use std::process::ExitCode;

/// No flags of its own; `cargo run --release -- --help` lists the standard ones of
/// `fframes::cli` (render, frame, strip, inspect, audio, ...).
#[derive(Debug, clap::Args)]
struct VideoArgs {}

fn main() -> ExitCode {
    let args = cli::parse::<VideoArgs>();
    let media = SbxIntroMedia::prepare().expect("media");
    let video = SbxIntroVideo::new(&media);

    cli::new(
        &video,
        RenderOptions {
            media: Some(&media),
            default_font: "Geist",
            video_encoder_options: EncoderOptions {
                preferred_encoder: Some("libx264"),
                codec_params: Some(&[("crf", "18"), ("preset", "slow"), ("tune", "animation")]),
                ..Default::default()
            },
            ..Default::default()
        },
    )
    .args(args)
    .run()
}
