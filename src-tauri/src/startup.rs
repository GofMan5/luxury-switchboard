//! The main window's creation and the shell's last word when it cannot start.
//!
//! A release build has no console. When Tauri builds a config window itself
//! (from inside the running event loop) and WebView2 refuses to start, the
//! error is only logged: the window disappears and the process lingers with
//! nothing on screen, which users saw as a white window that closed. So the
//! main window is declared with `create: false` and built here, on Windows
//! before the event loop runs, where the failure comes back as a value. A
//! refusal is retried within a short budget, because right after an update the
//! previous instance's browser can still be releasing the shared profile; what
//! still fails ends in a native dialog that names the reason.

use std::time::{Duration, Instant};

use tauri::{App, WebviewWindowBuilder};

/// The label the capabilities grant IPC to.
const MAIN_WINDOW: &str = "main";
const WINDOW_ATTEMPTS: u32 = 3;
const WINDOW_RETRY_DELAY: Duration = Duration::from_millis(1500);
/// WebView2 can take from seconds to most of a minute to report a refused
/// start; retries stop once this much time is spent so a runtime that stays
/// broken reaches the dialog instead of holding an empty window for minutes.
const WINDOW_RETRY_BUDGET: Duration = Duration::from_secs(30);
const PRODUCT: &str = "Luxury Switchboard";

pub fn create_main_window(app: &App) -> Result<(), String> {
    let config = app
        .config()
        .app
        .windows
        .iter()
        .find(|window| window.label == MAIN_WINDOW)
        .cloned()
        .ok_or_else(|| "the main window is missing from the app configuration".to_string())?;
    // A failed build leaves nothing registered under the label (the window is
    // attached only after its webview exists), so another attempt is clean.
    retry(
        WINDOW_ATTEMPTS,
        WINDOW_RETRY_DELAY,
        WINDOW_RETRY_BUDGET,
        || {
            WebviewWindowBuilder::from_config(app, &config)
                .and_then(|builder| builder.build())
                .map(drop)
                .map_err(|error| error.to_string())
        },
    )
}

fn retry<T>(
    attempts: u32,
    delay: Duration,
    budget: Duration,
    mut attempt: impl FnMut() -> Result<T, String>,
) -> Result<T, String> {
    let started = Instant::now();
    let mut round = 0;
    loop {
        round += 1;
        let error = match attempt() {
            Ok(value) => return Ok(value),
            Err(error) => error,
        };
        eprintln!("[startup] main window attempt {round}/{attempts} failed: {error}");
        if round >= attempts || started.elapsed() + delay > budget {
            return Err(error);
        }
        std::thread::sleep(delay);
    }
}

/// Shows why the app cannot start and ends the process.
pub fn fail(detail: &str) -> ! {
    show_error(
        &format!("{PRODUCT} could not start"),
        &format!(
            "{detail}\n\nClose other copies of {PRODUCT} and open it again. If it keeps failing, reinstall it from the release page; your settings are kept."
        ),
    );
    std::process::exit(1)
}

/// Windows release builds have no console, so a panic there gets a visible
/// sentence instead of a window that silently closes. The default hook keeps
/// its stderr report.
#[cfg(windows)]
pub fn install_panic_hook() {
    let default = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        default(info);
        show_error(
            &format!("{PRODUCT} hit an internal error"),
            &format!("{info}\n\nIf the window closed or stopped responding, open {PRODUCT} again."),
        );
    }));
}

/// On Windows the dialog runs on its own thread: a panic can arrive on the UI
/// thread in the middle of a window message, and a modal loop there would
/// re-enter the windowing code that just failed. macOS dialogs must run on the
/// main thread (rfd dispatches there), so elsewhere it is shown in place.
pub fn show_error(title: &str, description: &str) {
    let dialog = rfd::MessageDialog::new()
        .set_level(rfd::MessageLevel::Error)
        .set_title(title)
        .set_description(description)
        .set_buttons(rfd::MessageButtons::Ok);
    #[cfg(windows)]
    {
        let _ = std::thread::spawn(move || dialog.show()).join();
    }
    #[cfg(not(windows))]
    dialog.show();
}

#[cfg(test)]
mod tests {
    use super::*;

    const NO_LIMIT: Duration = Duration::from_secs(3600);

    #[test]
    fn a_transient_failure_is_retried() {
        let mut calls = 0;
        let result = retry(3, Duration::ZERO, NO_LIMIT, || {
            calls += 1;
            if calls < 2 {
                Err("busy".to_string())
            } else {
                Ok(calls)
            }
        });
        assert_eq!(result, Ok(2));
        assert_eq!(calls, 2);
    }

    #[test]
    fn a_persistent_failure_reports_the_last_reason() {
        let mut calls = 0;
        let result: Result<(), String> = retry(3, Duration::ZERO, NO_LIMIT, || {
            calls += 1;
            Err(format!("refused {calls}"))
        });
        assert_eq!(result, Err("refused 3".to_string()));
        assert_eq!(calls, 3);
    }

    #[test]
    fn a_slow_refusal_is_not_retried_past_the_budget() {
        // One refusal that already used the budget goes straight to the dialog.
        let mut calls = 0;
        let result: Result<(), String> = retry(
            3,
            Duration::from_millis(5),
            Duration::from_millis(1),
            || {
                calls += 1;
                std::thread::sleep(Duration::from_millis(2));
                Err("slow".to_string())
            },
        );
        assert_eq!(result, Err("slow".to_string()));
        assert_eq!(calls, 1);
    }

    #[test]
    fn only_this_module_creates_the_main_window() {
        // With create left on, Tauri would build the window first and this
        // module's attempt would fail on the duplicate label.
        let config: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        let main = config["app"]["windows"]
            .as_array()
            .unwrap()
            .iter()
            .find(|window| window["label"] == MAIN_WINDOW)
            .expect("the main window is configured");
        assert_eq!(main["create"], serde_json::Value::Bool(false));
        // The window paints the boot sentinel's color, not white, until the
        // page does.
        let color = main["backgroundColor"]
            .as_str()
            .expect("a background color");
        assert!(
            include_str!("../../frontend/public/boot.css")
                .contains(&format!("background: {color};"))
        );
    }
}
