mod sidecar;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_shell::init())
        .manage(sidecar::SidecarState::default())
        .invoke_handler(tauri::generate_handler![
            sidecar::sidecar_start,
            sidecar::sidecar_write,
            sidecar::sidecar_stop,
        ])
        .build(tauri::generate_context!())
        .expect("error while running tauri application");
    app.run(|app, event| {
        if matches!(event, tauri::RunEvent::Exit) {
            sidecar::stop_on_exit(app);
        }
    });
}
