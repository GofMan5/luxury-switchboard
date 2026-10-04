mod sidecar;

#[tauri::command]
fn restart_app(app: tauri::AppHandle) {
    app.request_restart();
}

/// Opens a link in the system browser. Only https leaves the app: a link the
/// update check serves is the project's release page, not a scheme the caller
/// invented.
#[tauri::command]
#[allow(deprecated)] // shell::open is what the tree already carries; the opener
// plugin would be a second dependency for one call.
fn open_url(app: tauri::AppHandle, url: String) -> Result<(), String> {
    use tauri_plugin_shell::ShellExt;
    if !url.starts_with("https://") {
        return Err("only https links leave the app".into());
    }
    app.shell().open(url, None).map_err(|error| error.to_string())
}

/// Runs a verified update installer. The sidecar downloaded the file, hashed
/// every byte against the release's own checksum and named the path; this side
/// re-checks the location (the app's own update directory, nothing the caller
/// can aim elsewhere) and the name shape (a Switchboard setup, nothing else)
/// before anything executes. `exit` closes the app so the installer can
/// replace it: a running binary cannot be overwritten, and the installer
/// would only ask again.
#[tauri::command]
fn run_installer(app: tauri::AppHandle, path: String, exit: bool) -> Result<(), String> {
    let name = std::path::Path::new(&path)
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or_else(|| "the installer path has no file name".to_string())?;
    let looks_like_setup = name.starts_with("Luxury-Switchboard-") && name.ends_with("-setup.exe")
        && !name.contains("..") && !name.contains('/') && !name.contains('\\');
    if !looks_like_setup {
        return Err("not a Switchboard installer".into());
    }
    let update_dir = update_directory()?;
    let update_dir = std::path::Path::new(&update_dir)
        .canonicalize()
        .map_err(|error| error.to_string())?;
    let installer = std::path::Path::new(&path)
        .canonicalize()
        .map_err(|_| "the installer file does not exist".to_string())?;
    if !installer.starts_with(&update_dir) {
        return Err("the installer is not in the update directory".into());
    }
    std::process::Command::new(&installer)
        .spawn()
        .map_err(|error| format!("the installer could not start: {error}"))?;
    if exit {
        app.exit(0);
    }
    Ok(())
}

/// The app's update staging directory, the only place run_installer executes
/// from. It lives under the same user data root the Go sidecar writes to.
#[cfg(windows)]
fn update_directory() -> Result<String, String> {
    let local = std::env::var("LOCALAPPDATA").map_err(|_| "user data directory is unavailable".to_string())?;
    Ok(format!(r"{local}\ProviderSwitchboard\update"))
}

#[cfg(not(windows))]
fn update_directory() -> Result<String, String> {
    Err("self-update is Windows-only in this build".to_string())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(sidecar::SidecarState::default())
        .invoke_handler(tauri::generate_handler![
            sidecar::sidecar_start,
            sidecar::sidecar_write,
            sidecar::sidecar_stop,
            restart_app,
            open_url,
            run_installer,
        ])
        .build(tauri::generate_context!())
        .expect("error while running tauri application");
    app.run(|app, event| match event {
        tauri::RunEvent::ExitRequested { api, .. } => {
            if sidecar::begin_graceful_exit(app) {
                api.prevent_exit();
            }
        }
        tauri::RunEvent::Exit => sidecar::stop_on_exit(app),
        _ => {}
    });
}
