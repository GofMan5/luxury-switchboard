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
