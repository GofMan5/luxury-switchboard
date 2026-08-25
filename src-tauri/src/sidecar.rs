use std::{
    sync::{
        Mutex, MutexGuard,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    thread,
    time::Duration,
};

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
    generation: AtomicU64,
    exiting: AtomicBool,
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
    let mut guard = lock_start(state.inner())?;
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
    let generation = state.generation.fetch_add(1, Ordering::AcqRel) + 1;
    *guard = Some(child);
    drop(guard);

    let event_app = app.clone();
    tauri::async_runtime::spawn(async move {
        forward_events(event_app, receiver, pid, generation).await;
    });
    Ok(StartResult { pid })
}

fn lock_start(state: &SidecarState) -> Result<MutexGuard<'_, Option<CommandChild>>, String> {
    let guard = state
        .child
        .lock()
        .map_err(|_| "Sidecar state is unavailable".to_string())?;
    if state.exiting.load(Ordering::Acquire) {
        return Err("Application is exiting".to_string());
    }
    Ok(guard)
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

pub fn stop_on_exit(app: &AppHandle) {
    if let Ok(mut guard) = app.state::<SidecarState>().child.lock()
        && let Some(child) = guard.take()
    {
        let _ = child.kill();
    }
}

pub fn begin_graceful_exit(app: &AppHandle) -> bool {
    let state = app.state::<SidecarState>();
    if state.exiting.swap(true, Ordering::AcqRel) {
        return false;
    }
    if let Ok(mut guard) = state.child.lock()
        && let Some(child) = guard.as_mut()
    {
        let _ = child.write(
            b"{\"v\":1,\"id\":\"native_shutdown\",\"type\":\"command\",\"method\":\"system.shutdown\"}\n",
        );
    }
    let app = app.clone();
    thread::spawn(move || {
        for _ in 0..200 {
            if app
                .state::<SidecarState>()
                .child
                .lock()
                .map(|child| child.is_none())
                .unwrap_or(true)
            {
                app.exit(0);
                return;
            }
            thread::sleep(Duration::from_millis(50));
        }
        stop_on_exit(&app);
        app.exit(0);
    });
    true
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
        || !valid_request_id(&envelope.id)
        || !allowed_method(&envelope.method)
    {
        return Err("Sidecar command is not allowed".to_string());
    }
    Ok(())
}

fn valid_request_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 80
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-'))
}

fn allowed_method(method: &str) -> bool {
    // One list for both editions, on purpose. The shell only decides which frames
    // are well formed enough to forward; whether a command exists is the sidecar's
    // answer, and the public sidecar is compiled without those handlers, so an
    // owner method reaches it and comes back `method_not_found`. Splitting this
    // list per edition would buy a different error string, not a boundary.
    matches!(
        method,
        "system.handshake"
            | "system.cancel"
            | "system.shutdown"
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
            | "keys.addMany"
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
            | "guardrails.status"
            | "guardrails.findings"
            | "guardrails.clear"
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
            | "tunnel.privacy_test"
            | "clients.list"
            | "clients.events"
            | "clients.profile"
            | "shared.list"
            | "shared.control"
    )
}

async fn forward_events(
    app: AppHandle,
    mut receiver: tauri::async_runtime::Receiver<CommandEvent>,
    pid: u32,
    generation: u64,
) {
    let mut buffer = Vec::with_capacity(8 * 1024);
    let mut lifecycle_state = "stopped";
    while let Some(event) = receiver.recv().await {
        match event {
            CommandEvent::Stdout(bytes) => {
                if append_frames(&app, &mut buffer, &bytes).is_err() {
                    lifecycle_state = "protocol-error";
                    break;
                }
            }
            CommandEvent::Terminated(_) => {
                break;
            }
            CommandEvent::Error(_) => {
                lifecycle_state = "error";
                break;
            }
            CommandEvent::Stderr(_) => {
                // Diagnostics are intentionally not forwarded to the WebView.
            }
            _ => {}
        }
    }
    let state = app.state::<SidecarState>();
    let mut lifecycle = None;
    if let Ok(mut child) = state.child.lock() {
        let active_generation = state.generation.load(Ordering::Acquire);
        lifecycle = lifecycle_for_process(
            child.as_ref().map(CommandChild::pid),
            active_generation,
            pid,
            generation,
            lifecycle_state,
        );
        if lifecycle.is_some()
            && let Some(child) = child.take()
        {
            let _ = child.kill();
        }
    }
    if let Some(state) = lifecycle {
        let _ = app.emit(LIFECYCLE_EVENT, LifecycleEvent { state });
    }
}

fn lifecycle_for_process(
    active_pid: Option<u32>,
    active_generation: u64,
    completed_pid: u32,
    completed_generation: u64,
    state: &'static str,
) -> Option<&'static str> {
    matching_process(
        active_pid,
        active_generation,
        completed_pid,
        completed_generation,
    )
    .then_some(state)
}

fn matching_process(
    active_pid: Option<u32>,
    active_generation: u64,
    completed_pid: u32,
    completed_generation: u64,
) -> bool {
    active_pid == Some(completed_pid) && active_generation == completed_generation
}

fn append_frames(app: &AppHandle, buffer: &mut Vec<u8>, bytes: &[u8]) -> Result<(), ()> {
    decode_frames(buffer, bytes, |text| {
        app.emit(FRAME_EVENT, text).map_err(|_| ())
    })
}

fn decode_frames(
    buffer: &mut Vec<u8>,
    bytes: &[u8],
    mut emit: impl FnMut(String) -> Result<(), ()>,
) -> Result<(), ()> {
    for &byte in bytes {
        if byte != b'\n' {
            if buffer.len() == MAX_FRAME_BYTES {
                return Err(());
            }
            buffer.push(byte);
            continue;
        }
        if buffer.last() == Some(&b'\r') {
            buffer.pop();
        }
        if buffer.is_empty() {
            return Err(());
        }
        let frame = std::mem::replace(buffer, Vec::with_capacity(8 * 1024));
        let text = String::from_utf8(frame).map_err(|_| ())?;
        let value: serde_json::Value = serde_json::from_str(&text).map_err(|_| ())?;
        if value.get("v").and_then(serde_json::Value::as_u64) != Some(1) {
            return Err(());
        }
        emit(text)?;
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
        assert!(
            validate_outgoing(r#"{"v":1,"id":"bad id","type":"command","method":"relay.status"}"#)
                .is_err()
        );
    }

    #[test]
    fn accepts_allowlisted_command() {
        assert!(
            validate_outgoing(r#"{"v":1,"id":"x","type":"command","method":"relay.status"}"#)
                .is_ok()
        );
        assert!(
            validate_outgoing(r#"{"v":1,"id":"x","type":"command","method":"system.shutdown"}"#)
                .is_ok()
        );
        assert!(
            validate_outgoing(
                r#"{"v":1,"id":"x","type":"command","method":"tunnel.privacy_test"}"#
            )
            .is_ok()
        );
        assert!(
            validate_outgoing(r#"{"v":1,"id":"x","type":"command","method":"keys.addMany"}"#)
                .is_ok()
        );
    }

    #[test]
    fn accepts_many_frames_in_one_chunk_larger_than_one_frame_limit() {
        let frame = b"{\"v\":1,\"type\":\"event\",\"topic\":\"x\",\"seq\":1}\n";
        let bytes = frame.repeat(MAX_FRAME_BYTES / frame.len() + 2);
        let mut buffer = Vec::new();
        let mut count = 0;
        decode_frames(&mut buffer, &bytes, |_| {
            count += 1;
            Ok(())
        })
        .unwrap();
        assert!(bytes.len() > MAX_FRAME_BYTES);
        assert_eq!(count, bytes.len() / frame.len());
        assert!(buffer.is_empty());
    }

    #[test]
    fn stale_reader_cannot_claim_a_restarted_sidecar() {
        assert!(matching_process(Some(10), 1, 10, 1));
        assert!(!matching_process(Some(10), 2, 10, 1));
        assert!(!matching_process(Some(11), 1, 10, 1));
        assert!(!matching_process(None, 1, 10, 1));
        assert_eq!(
            lifecycle_for_process(Some(10), 1, 10, 1, "protocol-error"),
            Some("protocol-error")
        );
        assert_eq!(lifecycle_for_process(Some(10), 2, 10, 1, "stopped"), None);
    }

    #[test]
    fn sidecar_cannot_start_after_exit_begins() {
        let state = SidecarState::default();
        assert!(lock_start(&state).is_ok());
        state.exiting.store(true, Ordering::Release);
        assert!(lock_start(&state).is_err());
    }
}
