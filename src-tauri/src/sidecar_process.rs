//! The process capability and the blocking stdin writer have separate owners.
//! Only the child we spawned can be terminated; no PID-based external kill.
use std::{
    io::{self, Write},
    process::{Child, ChildStderr, ChildStdout, Command, Stdio},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
        mpsc::{self, SyncSender},
    },
    thread,
    time::{Duration, Instant},
};

const WRITE_QUEUE_CAPACITY: usize = 8;

pub struct SidecarProcess {
    child: Mutex<Child>,
    writes: SyncSender<WriteRequest>,
    stopped: AtomicBool,
    exited: AtomicBool,
    pid: u32,
}

struct WriteRequest {
    bytes: Vec<u8>,
    deadline: Instant,
    reply: mpsc::Sender<Result<(), String>>,
}

impl SidecarProcess {
    pub fn spawn(mut command: Command) -> io::Result<(Arc<Self>, ChildStdout, ChildStderr)> {
        let mut child = command
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()?;
        let mut stdin = child.stdin.take().expect("piped stdin");
        let stdout = child.stdout.take().expect("piped stdout");
        let stderr = child.stderr.take().expect("piped stderr");
        let (writes, receiver) = mpsc::sync_channel::<WriteRequest>(WRITE_QUEUE_CAPACITY);
        let process = Arc::new(Self {
            pid: child.id(),
            child: Mutex::new(child),
            writes,
            stopped: AtomicBool::new(false),
            exited: AtomicBool::new(false),
        });
        let writer_process = Arc::downgrade(&process);
        thread::spawn(move || {
            while let Ok(request) = receiver.recv() {
                let Some(process) = writer_process.upgrade() else {
                    break;
                };
                if process.stopped.load(Ordering::Acquire) || Instant::now() >= request.deadline {
                    let _ = request
                        .reply
                        .send(Err("Sidecar command could not be sent".into()));
                    continue;
                }
                // No process/lifecycle mutex is held during this blocking I/O.
                let result = stdin
                    .write_all(&request.bytes)
                    .map_err(|_| "Sidecar command could not be sent".to_string());
                if result.is_err() {
                    let _ = process.stop();
                }
                let _ = request.reply.send(result);
            }
        });
        let reaper = process.clone();
        thread::spawn(move || {
            while !reaper.has_exited() {
                let finished = reaper
                    .child
                    .lock()
                    .map(|mut child| child.try_wait().is_ok_and(|status| status.is_some()))
                    .unwrap_or(true);
                if finished {
                    reaper.exited.store(true, Ordering::Release);
                    reaper.stopped.store(true, Ordering::Release);
                    break;
                }
                thread::sleep(Duration::from_millis(20));
            }
        });
        Ok((process, stdout, stderr))
    }

    pub fn pid(&self) -> u32 {
        self.pid
    }
    pub fn has_exited(&self) -> bool {
        self.exited.load(Ordering::Acquire)
    }

    pub fn queue_frame(
        &self,
        frame: &str,
        timeout: Duration,
    ) -> Result<mpsc::Receiver<Result<(), String>>, String> {
        if self.stopped.load(Ordering::Acquire) {
            return Err("Sidecar is not running".into());
        }
        let (reply, receiver) = mpsc::channel();
        let mut bytes = frame.as_bytes().to_vec();
        bytes.push(b'\n');
        self.writes
            .try_send(WriteRequest {
                bytes,
                deadline: Instant::now() + timeout,
                reply,
            })
            .map_err(|_| "Sidecar command queue is unavailable".to_string())?;
        Ok(receiver)
    }

    #[cfg(test)]
    pub fn write_frame(&self, frame: &str, timeout: Duration) -> Result<(), String> {
        self.wait_for_write(self.queue_frame(frame, timeout)?, timeout)
    }

    pub fn wait_for_write(
        &self,
        reply: mpsc::Receiver<Result<(), String>>,
        timeout: Duration,
    ) -> Result<(), String> {
        match reply.recv_timeout(timeout) {
            Ok(result) => result,
            Err(_) => {
                let _ = self.stop(); // Unblocks the pipe, without needing its writer.
                Err("Sidecar command send timed out".into())
            }
        }
    }

    pub fn stop(&self) -> Result<(), String> {
        self.stopped.store(true, Ordering::Release);
        let mut child = self
            .child
            .lock()
            .map_err(|_| "Sidecar state is unavailable".to_string())?;
        // try_wait caches a completed child; never signal a recycled process ID.
        if child
            .try_wait()
            .map_err(|_| "Sidecar could not be stopped".to_string())?
            .is_none()
        {
            child
                .kill()
                .map_err(|_| "Sidecar could not be stopped".to_string())?;
        }
        Ok(())
    }
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;
    use std::io::{BufRead, BufReader};

    pub(crate) struct StopOnDrop(pub Arc<SidecarProcess>);
    impl Drop for StopOnDrop {
        fn drop(&mut self) {
            let _ = self.0.stop();
        }
    }

    #[test]
    #[ignore] // Spawned only by the pipe regressions, never as an independent test.
    fn stalled_child() {
        println!("SIDECAR_READY");
        io::stdout().flush().unwrap();
        thread::sleep(Duration::from_secs(30));
    }

    pub(crate) fn stalled_process() -> Arc<SidecarProcess> {
        let mut command = Command::new(std::env::current_exe().unwrap());
        command.args([
            "--exact",
            "sidecar_process::tests::stalled_child",
            "--ignored",
            "--nocapture",
        ]);
        let (process, stdout, _stderr) = SidecarProcess::spawn(command).unwrap();
        for line in BufReader::new(stdout).lines() {
            if line.unwrap().contains("SIDECAR_READY") {
                return process;
            }
        }
        panic!("test child never became ready");
    }

    #[test]
    fn blocked_pipe_send_has_a_deadline_and_reaps_its_child() {
        let process = stalled_process();
        let _cleanup = StopOnDrop(process.clone());
        let started = Instant::now();
        assert!(
            process
                .write_frame(&"x".repeat(1024 * 1024), Duration::from_millis(100))
                .is_err()
        );
        assert!(started.elapsed() < Duration::from_secs(2));
        wait_for_exit(&process);
    }

    #[test]
    fn watchdog_can_stop_a_process_while_its_writer_is_blocked() {
        let process = stalled_process();
        let _cleanup = StopOnDrop(process.clone());
        let writer = process.clone();
        let pending = thread::spawn(move || {
            writer.write_frame(&"x".repeat(1024 * 1024), Duration::from_secs(5))
        });
        thread::sleep(Duration::from_millis(50));
        let started = Instant::now();
        process.stop().unwrap();
        assert!(started.elapsed() < Duration::from_secs(1));
        assert!(pending.join().unwrap().is_err());
        wait_for_exit(&process);
        process.stop().unwrap(); // Completed capabilities cannot kill another PID.
    }

    fn wait_for_exit(process: &SidecarProcess) {
        let deadline = Instant::now() + Duration::from_secs(2);
        while !process.has_exited() && Instant::now() < deadline {
            thread::sleep(Duration::from_millis(10));
        }
        assert!(
            process.has_exited(),
            "child was not reaped after termination"
        );
    }
}
