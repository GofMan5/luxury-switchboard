use std::sync::Mutex;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Emitter, Manager, State};
use tauri_plugin_shell::{
    ShellExt,
    process::{CommandChild, CommandEvent},
};

const MAX_FRAME_BYTES: usize = 256 * 1024;
const FRAME_EVENT: &str = "sidecar-frame";
const LIFECYCLE_EVENT: &str = "sidecar-lifecycle";

#[derive(Default)]
pub struct SidecarState {
    child: Mutex<Option<CommandChild>>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct StartResult {
    pid: u32,
}

#[derive(Clone, Serialize)]
#[serde(rename_all = "camelCase")]
struct LifecycleEvent {
    state: &'static str,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct OutgoingEnvelope {
    v: u8,
    id: String,
    #[serde(rename = "type")]
    kind: String,
    method: String,
    #[serde(default, rename = "payload")]
    _payload: Option<serde_json::Value>,
}

#[tauri::command]
pub fn sidecar_start(
    app: AppHandle,
    state: State<'_, SidecarState>,
) -> Result<StartResult, String> {
    let mut guard = state
        .child
        .lock()
        .map_err(|_| "Sidecar state is unavailable".to_string())?;
    if let Some(child) = guard.as_ref() {
        return Ok(StartResult { pid: child.pid() });
    }

    let command = app
        .shell()
        .sidecar("switchboard-sidecar")
        .map_err(|_| "Sidecar executable is unavailable".to_string())?
        .set_raw_out(true);
    let command = if cfg!(debug_assertions) {
        command.env("SWITCHBOARD_PORT", "18798")
    } else {
        command
    };
    let (receiver, child) = command
        .spawn()
        .map_err(|_| "Sidecar could not start".to_string())?;
    let pid = child.pid();
    *guard = Some(child);
    drop(guard);

    let event_app = app.clone();
    tauri::async_runtime::spawn(async move {
        forward_events(event_app, receiver).await;
    });
    Ok(StartResult { pid })
}

#[tauri::command]
pub fn sidecar_write(state: State<'_, SidecarState>, frame: String) -> Result<(), String> {
    validate_outgoing(&frame)?;
    let mut guard = state
        .child
        .lock()
        .map_err(|_| "Sidecar state is unavailable".to_string())?;
    let child = guard
        .as_mut()
        .ok_or_else(|| "Sidecar is not running".to_string())?;
    child
        .write(format!("{frame}\n").as_bytes())
        .map_err(|_| "Sidecar command could not be sent".to_string())
}

#[tauri::command]
pub fn sidecar_stop(state: State<'_, SidecarState>) -> Result<(), String> {
    let child = state
        .child
        .lock()
        .map_err(|_| "Sidecar state is unavailable".to_string())?
        .take();
    if let Some(child) = child {
        child
            .kill()
            .map_err(|_| "Sidecar could not be stopped".to_string())?;
    }
    Ok(())
}

fn validate_outgoing(frame: &str) -> Result<(), String> {
    if frame.is_empty()
        || frame.len() > MAX_FRAME_BYTES
        || frame
            .bytes()
            .any(|byte| matches!(byte, b'\r' | b'\n' | b'\0'))
    {
        return Err("Invalid sidecar protocol frame".to_string());
    }
    let envelope: OutgoingEnvelope =
        serde_json::from_str(frame).map_err(|_| "Invalid sidecar protocol frame".to_string())?;
    if envelope.v != 1
        || envelope.kind != "command"
        || envelope.id.is_empty()
        || !allowed_method(&envelope.method)
    {
        return Err("Sidecar command is not allowed".to_string());
    }
    Ok(())
}

fn allowed_method(method: &str) -> bool {
    matches!(
        method,
        "system.handshake"
            | "system.cancel"
            | "relay.status"
            | "relay.start"
            | "relay.stop"
            | "providers.list"
            | "providers.activate"
            | "providers.add"
            | "providers.update"
            | "providers.delete"
            | "keys.list"
            | "keys.add"
            | "keys.update"
            | "keys.remove"
            | "keys.move"
            | "keys.reset"
            | "activity.list"
            | "activity.summary"
            | "history.recent"
            | "history.stats"
            | "settings.get"
            | "settings.update"
            | "routes.list"
            | "routes.upsert"
            | "routes.upsertMany"
            | "routes.delete"
            | "models.discover"
            | "models.test"
            | "tunnel.get"
            | "tunnel.configure"
            | "tunnel.start"
            | "tunnel.stop"
            | "tunnel.rotate"
            | "tunnel.reveal"
            | "clients.list"
            | "clients.events"
            | "shared.list"
            | "shared.control"
    )
}

async fn forward_events(
    app: AppHandle,
    mut receiver: tauri::async_runtime::Receiver<CommandEvent>,
) {
    let mut buffer = Vec::with_capacity(8 * 1024);
    while let Some(event) = receiver.recv().await {
        match event {
            CommandEvent::Stdout(bytes) => {
                if append_frames(&app, &mut buffer, &bytes).is_err() {
                    let _ = app.emit(
                        LIFECYCLE_EVENT,
                        LifecycleEvent {
                            state: "protocol-error",
                        },
                    );
                    break;
                }
            }
            CommandEvent::Terminated(_) => {
                let _ = app.emit(LIFECYCLE_EVENT, LifecycleEvent { state: "stopped" });
                break;
            }
            CommandEvent::Error(_) => {
                let _ = app.emit(LIFECYCLE_EVENT, LifecycleEvent { state: "error" });
                break;
            }
            CommandEvent::Stderr(_) => {
                // Diagnostics are intentionally not forwarded to the WebView.
            }
            _ => {}
        }
    }
    if let Ok(mut child) = app.state::<SidecarState>().child.lock()
        && let Some(child) = child.take()
    {
        let _ = child.kill();
    }
}

fn append_frames(app: &AppHandle, buffer: &mut Vec<u8>, bytes: &[u8]) -> Result<(), ()> {
    if buffer.len().saturating_add(bytes.len()) > MAX_FRAME_BYTES {
        return Err(());
    }
    buffer.extend_from_slice(bytes);
    while let Some(newline) = buffer.iter().position(|byte| *byte == b'\n') {
        let mut frame = buffer.drain(..=newline).collect::<Vec<_>>();
        frame.pop();
        if frame.last() == Some(&b'\r') {
            frame.pop();
        }
        if frame.is_empty() || frame.len() > MAX_FRAME_BYTES {
            return Err(());
        }
        let text = String::from_utf8(frame).map_err(|_| ())?;
        let value: serde_json::Value = serde_json::from_str(&text).map_err(|_| ())?;
        if value.get("v").and_then(serde_json::Value::as_u64) != Some(1) {
            return Err(());
        }
        app.emit(FRAME_EVENT, text).map_err(|_| ())?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_newline_and_unlisted_methods() {
        assert!(validate_outgoing("{}\n").is_err());
        assert!(
            validate_outgoing(r#"{"v":1,"id":"x","type":"command","method":"shell.run"}"#).is_err()
        );
    }

    #[test]
    fn accepts_allowlisted_command() {
        assert!(
            validate_outgoing(r#"{"v":1,"id":"x","type":"command","method":"relay.status"}"#)
                .is_ok()
        );
    }
}
