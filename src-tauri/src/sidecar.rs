use std::{
    io::{self, Read},
    process::ChildStdout,
    sync::{
        Arc, Mutex, MutexGuard,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    thread,
    time::Duration,
};

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Emitter, Manager, State};
use tauri_plugin_shell::ShellExt;

use crate::sidecar_process::SidecarProcess;

const MAX_FRAME_BYTES: usize = 256 * 1024;
const FRAME_EVENT: &str = "sidecar-frame";
const LIFECYCLE_EVENT: &str = "sidecar-lifecycle";
const WRITE_TIMEOUT: Duration = Duration::from_secs(2);
const EXIT_TIMEOUT: Duration = Duration::from_secs(10);
const SHUTDOWN_FRAME: &str =
    r#"{"v":1,"id":"native_shutdown","type":"command","method":"system.shutdown"}"#;

#[derive(Default)]
pub struct SidecarState {
    child: Mutex<Option<Arc<SidecarProcess>>>,
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
    if let Some(child) = guard.as_ref()
        && !child.has_exited()
    {
        return Ok(StartResult { pid: child.pid() });
    }

    // Keep the plugin's fixed executable resolution, environment and Windows
    // CREATE_NO_WINDOW flags. Only pipe/lifecycle ownership is replaced.
    let command = app
        .shell()
        .sidecar("switchboard-sidecar")
        .map_err(|_| "Sidecar executable is unavailable".to_string())?;
    let command = if cfg!(debug_assertions) {
        command.env("SWITCHBOARD_PORT", "18798")
    } else {
        command
    };
    let (process, stdout, stderr) =
        SidecarProcess::spawn(command.into()).map_err(|_| "Sidecar could not start".to_string())?;
    let pid = process.pid();
    let generation = state.generation.fetch_add(1, Ordering::AcqRel) + 1;
    *guard = Some(process.clone());

    let event_app = app.clone();
    let event_process = process.clone();
    thread::spawn(move || forward_events(event_app, stdout, event_process, generation));
    thread::spawn(move || {
        // Drain raw diagnostics in fixed-size chunks; never retain or forward
        // provider metadata, and never wait for a stderr newline.
        if drain_diagnostics(stderr).is_err() {
            finish_process(&app, &process, generation, "error");
        }
    });
    Ok(StartResult { pid })
}

fn lock_start(state: &SidecarState) -> Result<MutexGuard<'_, Option<Arc<SidecarProcess>>>, String> {
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
pub async fn sidecar_write(state: State<'_, SidecarState>, frame: String) -> Result<(), String> {
    validate_outgoing(&frame)?;
    let (process, reply) = {
        let guard = lock_start(state.inner())?;
        let process = guard
            .as_ref()
            .ok_or_else(|| "Sidecar is not running".to_string())?
            .clone();
        // Queue admission is nonblocking and serialized with start/stop. No
        // lifecycle mutex survives a write to the child's full stdin pipe.
        let reply = process.queue_frame(&frame, WRITE_TIMEOUT)?;
        (process, reply)
    };
    tauri::async_runtime::spawn_blocking(move || process.wait_for_write(reply, WRITE_TIMEOUT))
        .await
        .map_err(|_| "Sidecar command could not be sent".to_string())?
}

#[tauri::command]
pub fn sidecar_stop(state: State<'_, SidecarState>) -> Result<(), String> {
    let child = state
        .child
        .lock()
        .map_err(|_| "Sidecar state is unavailable".to_string())?
        .take();
    if let Some(child) = child {
        child.stop()?;
    }
    Ok(())
}

pub fn stop_on_exit(app: &AppHandle) {
    if let Ok(mut guard) = app.state::<SidecarState>().child.lock()
        && let Some(child) = guard.take()
    {
        let _ = child.stop();
    }
}

pub fn begin_graceful_exit(app: &AppHandle) -> bool {
    let state = app.state::<SidecarState>();
    if state.exiting.swap(true, Ordering::AcqRel) {
        return false;
    }
    let process = state.child.lock().ok().and_then(|child| child.clone());
    let app = app.clone();
    thread::spawn(move || {
        if let Some(process) = process {
            graceful_shutdown(&process, EXIT_TIMEOUT);
        }
        app.exit(0);
    });
    true
}

fn graceful_shutdown(process: &SidecarProcess, timeout: Duration) {
    // The exit deadline belongs to the owned Child, not the writer. Even a full
    // queue or blocked write cannot keep this thread from force-stopping it.
    let _ = process.queue_frame(SHUTDOWN_FRAME, timeout);
    let deadline = std::time::Instant::now() + timeout;
    while !process.has_exited() && std::time::Instant::now() < deadline {
        thread::sleep(Duration::from_millis(20));
    }
    let _ = process.stop();
}

fn forward_events(
    app: AppHandle,
    stdout: ChildStdout,
    process: Arc<SidecarProcess>,
    generation: u64,
) {
    let result = read_frames(stdout, |text| {
        let state = app.state::<SidecarState>();
        let child = state.child.lock().map_err(|_| ())?;
        if !matching_process(
            child.as_ref().map(|child| child.pid()),
            state.generation.load(Ordering::Acquire),
            process.pid(),
            generation,
        ) {
            return Err(());
        }
        app.emit(FRAME_EVENT, text).map_err(|_| ())
    });
    let lifecycle = match result {
        Ok(()) => "stopped",
        Err(ReadFailure::Protocol) => "protocol-error",
        Err(ReadFailure::Io) => "error",
    };
    finish_process(&app, &process, generation, lifecycle);
}

fn finish_process(
    app: &AppHandle,
    process: &SidecarProcess,
    generation: u64,
    lifecycle: &'static str,
) {
    let state = app.state::<SidecarState>();
    if let Ok(mut child) = state.child.lock()
        && let Some(lifecycle) = lifecycle_for_process(
            child.as_ref().map(|child| child.pid()),
            state.generation.load(Ordering::Acquire),
            process.pid(),
            generation,
            lifecycle,
        )
    {
        child.take();
        let _ = process.stop();
        // Serialize removal and lifecycle emission with restart admission, so
        // an old reader never disconnects a newly started generation.
        let _ = app.emit(LIFECYCLE_EVENT, LifecycleEvent { state: lifecycle });
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

#[derive(Debug)]
enum ReadFailure {
    Io,
    Protocol,
}

fn read_frames(
    mut reader: impl Read,
    mut emit: impl FnMut(String) -> Result<(), ()>,
) -> Result<(), ReadFailure> {
    let mut buffer = Vec::with_capacity(8 * 1024);
    let mut chunk = [0; 8 * 1024];
    loop {
        match reader.read(&mut chunk) {
            Ok(0) => {
                return if buffer.is_empty() {
                    Ok(())
                } else {
                    Err(ReadFailure::Protocol)
                };
            }
            Ok(count) => decode_frames(&mut buffer, &chunk[..count], &mut emit)
                .map_err(|_| ReadFailure::Protocol)?,
            Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
            Err(_) => return Err(ReadFailure::Io),
        }
    }
}

fn drain_diagnostics(mut reader: impl Read) -> io::Result<()> {
    let mut chunk = [0; 8 * 1024];
    loop {
        match reader.read(&mut chunk) {
            Ok(0) => return Ok(()),
            Ok(_) => {}
            Err(error) if error.kind() == io::ErrorKind::Interrupted => continue,
            Err(error) => return Err(error),
        }
    }
}

fn decode_frames(
    buffer: &mut Vec<u8>,
    bytes: &[u8],
    mut emit: impl FnMut(String) -> Result<(), ()>,
) -> Result<(), ()> {
    for &byte in bytes {
        if byte != b'\n' {
            // A cap-byte payload may have a CRLF delimiter, but no extra byte.
            if buffer.len() >= MAX_FRAME_BYTES
                && !(buffer.len() == MAX_FRAME_BYTES && byte == b'\r')
            {
                return Err(());
            }
            buffer.push(byte);
            continue;
        }
        if buffer.last() == Some(&b'\r') {
            buffer.pop();
        }
        if buffer.is_empty() || buffer.len() > MAX_FRAME_BYTES {
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
    // The shell validates frame shape, not handler existence. Removed methods
    // stay allowed for compatibility; the sidecar returns method_not_found.
    matches!(
        method,
        "system.handshake"
            | "system.cancel"
            | "system.shutdown"
            | "updates.check"
            | "updates.status"
            | "relay.status"
            | "relay.start"
            | "relay.stop"
            | "providers.list"
            | "providers.activate"
            | "providers.add"
            | "providers.update"
            | "providers.delete"
            | "providers.health"
            | "keys.list"
            | "keys.add"
            | "keys.addMany"
            | "keys.update"
            | "keys.remove"
            | "keys.move"
            | "keys.reset"
            | "keys.check"
            | "activity.list"
            | "activity.summary"
            | "history.recent"
            | "history.stats"
            | "analytics.report"
            | "analytics.prices.get"
            | "analytics.prices.set"
            | "analytics.prices.remove"
            | "settings.get"
            | "settings.update"
            | "appearance.get"
            | "appearance.update"
            | "plugins.list"
            | "plugins.get"
            | "plugins.install"
            | "plugins.update"
            | "plugins.remove"
            | "plugins.export"
            | "plugins.storage.get"
            | "plugins.storage.set"
            | "plugins.storage.update"
            | "plugins.secret.set"
            | "plugins.fetch"
            | "plugins.collection.get"
            | "plugins.collection.put"
            | "plugins.collection.delete"
            | "plugins.collection.list"
            | "plugins.collection.clear"
            | "plugins.collection.stats"
            | "plugins.dev.link"
            | "plugins.dev.unlink"
            | "plugins.download"
            | "guardrails.status"
            | "guardrails.findings"
            | "guardrails.clear"
            | "notifications.list"
            | "notifications.clear"
            | "backup.export"
            | "backup.import"
            | "routes.list"
            | "routes.upsert"
            | "routes.upsertMany"
            | "routes.delete"
            | "routes.remove"
            | "models.discover"
            | "models.test"
            | "analytics.prices.setCurrency"
            | "updates.install"
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
            | "codex.login.start"
            | "codex.login.status"
            | "codex.login.cancel"
            | "codex.login.device.start"
            | "codex.import.json"
            | "codex.import.files"
            | "codex.status"
            | "codex.quota"
            | "codex.quota.all"
            | "codex.wake"
            | "relayhooks.register"
            | "relayhooks.unregister"
            | "relayhooks.reset"
            | "relayhooks.respond"
            | "rules.list"
            | "rules.saveRequestRule"
            | "rules.deleteRequestRule"
            | "rules.reorderRequestRules"
            | "rules.saveRetryRule"
            | "rules.deleteRetryRule"
            | "rules.reorderRetryRules"
            | "rules.stats"
            | "rules.test"
            | "rules.recentErrors"
            | "codex.logout" // Removed commands stay listed until every shipped sidecar is
                             // newer than every shipped shell: an old shell's call gets a clean
                             // method_not_found, not a frame error.
    )
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
    fn a_plugin_install_frame_may_use_the_whole_frame_budget() {
        // A plugin package rides one command frame: the shell carries it up to
        // the shared byte cap, envelope and JSON escaping included, and refuses
        // one byte more before it can reach the sidecar.
        let prefix = r#"{"v":1,"id":"plugin_install_1","type":"command","method":"plugins.install","payload":{"package":{"schema":1,"id":"fixture.plugin","name":"Fixture","version":"1.0.0","permissions":["ui:pages"],"script":""#;
        let suffix = r#""},"granted":["ui:pages"]}}"#;
        for size in [MAX_FRAME_BYTES, MAX_FRAME_BYTES + 1] {
            let frame = format!(
                "{prefix}{}{suffix}",
                "\\n".repeat((size - prefix.len() - suffix.len()) / 2)
                    + &"x".repeat((size - prefix.len() - suffix.len()) % 2)
            );
            assert_eq!(frame.len(), size);
            assert_eq!(validate_outgoing(&frame).is_ok(), size <= MAX_FRAME_BYTES);
        }
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
    fn native_exit_deadline_survives_a_full_write_queue() {
        let process = crate::sidecar_process::tests::stalled_process();
        let _cleanup = crate::sidecar_process::tests::StopOnDrop(process.clone());
        let writer = process.clone();
        let pending = thread::spawn(move || {
            writer.write_frame(&"x".repeat(1024 * 1024), Duration::from_secs(5))
        });
        thread::sleep(Duration::from_millis(50));
        for _ in 0..8 {
            process
                .queue_frame("queued", Duration::from_secs(5))
                .unwrap();
        }
        assert!(
            process
                .queue_frame("overflow", Duration::from_secs(5))
                .is_err()
        );
        let started = std::time::Instant::now();
        graceful_shutdown(&process, Duration::from_millis(100));
        assert!(started.elapsed() < Duration::from_secs(1));
        assert!(pending.join().unwrap().is_err());
        let deadline = std::time::Instant::now() + Duration::from_secs(2);
        while !process.has_exited() && std::time::Instant::now() < deadline {
            thread::sleep(Duration::from_millis(10));
        }
        assert!(process.has_exited());
        assert!(process.queue_frame("late", WRITE_TIMEOUT).is_err());
    }

    #[test]
    fn byte_frames_survive_utf8_chunk_splits_at_the_exact_limit() {
        let prefix = r#"{"v":1,"id":"x","type":"command","method":"relay.status","payload":""#;
        let suffix = "\"}";
        for size in [MAX_FRAME_BYTES - 1, MAX_FRAME_BYTES, MAX_FRAME_BYTES + 1] {
            let padding = size - prefix.len() - suffix.len();
            let frame = format!(
                "{prefix}{}{suffix}",
                "é".repeat(padding / 2) + &"x".repeat(padding % 2)
            );
            assert_eq!(frame.len(), size);
            assert_eq!(validate_outgoing(&frame).is_ok(), size <= MAX_FRAME_BYTES);
            for delimiter in ["\n", "\r\n"] {
                let bytes = (frame.clone() + delimiter).into_bytes();
                let mut buffer = Vec::new();
                let mut delivered = Vec::new();
                let result = bytes.chunks(7).try_for_each(|chunk| {
                    decode_frames(&mut buffer, chunk, |text| {
                        delivered.push(text);
                        Ok(())
                    })
                });
                assert_eq!(result.is_ok(), size <= MAX_FRAME_BYTES);
                assert!(buffer.len() <= MAX_FRAME_BYTES + 1);
                if size <= MAX_FRAME_BYTES {
                    assert_eq!(delivered, [frame.clone()]);
                } else {
                    assert!(delivered.is_empty());
                }
            }
        }
    }

    #[test]
    fn malformed_partial_or_oversized_output_never_reaches_the_webview() {
        for bytes in [
            b"\xff\n".to_vec(),
            b"{}\n".to_vec(),
            b"{\"v\":2}\n".to_vec(),
            b"{\"v\":1}".to_vec(),
            b"\n".to_vec(),
            vec![b'x'; MAX_FRAME_BYTES + 1],
        ] {
            let mut count = 0;
            assert!(matches!(
                read_frames(bytes.as_slice(), |_| {
                    count += 1;
                    Ok(())
                }),
                Err(ReadFailure::Protocol)
            ));
            assert_eq!(count, 0);
        }
        assert!(read_frames(b"{\"v\":1}\n".as_slice(), |_| Err(())).is_err());
    }

    #[test]
    fn stderr_without_newlines_is_drained_with_bounded_reads() {
        struct Diagnostics {
            remaining: usize,
        }
        impl Read for Diagnostics {
            fn read(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
                assert!(bytes.len() <= 8 * 1024);
                let count = bytes.len().min(self.remaining);
                self.remaining -= count;
                bytes[..count].fill(0xff);
                Ok(count)
            }
        }
        drain_diagnostics(Diagnostics {
            remaining: 1024 * 1024,
        })
        .unwrap();
    }

    #[test]
    fn sidecar_cannot_start_after_exit_begins() {
        let state = SidecarState::default();
        assert!(lock_start(&state).is_ok());
        state.exiting.store(true, Ordering::Release);
        assert!(lock_start(&state).is_err());
    }
}
