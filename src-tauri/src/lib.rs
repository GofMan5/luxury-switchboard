mod sidecar;

use std::path::{Path, PathBuf};

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
    app.shell()
        .open(url, None)
        .map_err(|error| error.to_string())
}

/// Runs a verified update installer. The sidecar downloaded the file, hashed
/// every byte against the release's own checksum and named the path; this side
/// re-checks the location (the app's own update directory, nothing the caller
/// can aim elsewhere) and the name shape (the installer the release ships for
/// this platform, not another OS's or another architecture's) before anything
/// executes. `exit` closes the app so the replacement can take its place: a
/// running program cannot be overwritten out from under itself.
///
/// What "running the installer" means is per platform and stays in this thin
/// shell: Windows hands the setup to the OS, Linux swaps the AppImage the
/// process runs from, macOS mounts the dmg and swaps the app bundle. Which
/// asset to fetch and whether it is current is the sidecar's domain; this is
/// only the lifecycle around it.
#[tauri::command]
fn run_installer(app: tauri::AppHandle, path: String, exit: bool) -> Result<(), String> {
    let name = Path::new(&path)
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or_else(|| "the installer path has no file name".to_string())?;
    if !installer_name_is_ours(name) {
        return Err("not a Switchboard installer for this platform".into());
    }
    let update_dir = update_directory()?;
    let update_dir = Path::new(&update_dir)
        .canonicalize()
        .map_err(|error| error.to_string())?;
    let installer = Path::new(&path)
        .canonicalize()
        .map_err(|_| "the installer file does not exist".to_string())?;
    if !installer.starts_with(&update_dir) {
        return Err("the installer is not in the update directory".into());
    }
    #[cfg(target_os = "macos")]
    let launched = launch_verified_installer(&app, &installer, &update_dir, exit);
    #[cfg(not(target_os = "macos"))]
    let launched = launch_verified_installer(&app, &installer, exit);
    launched
}

/// The installer file name the release ships for this platform. The shape is
/// domain.InstallerSuffix's contract (backend/internal/slices/updates); the
/// shell repeats it so a name from another OS or another architecture is
/// refused before anything runs.
const INSTALLER_NAME_PREFIX: &str = "Luxury-Switchboard-";

#[cfg(all(windows, target_arch = "x86_64"))]
const INSTALLER_NAME_SUFFIX: &str = "-windows-x64-setup.exe";
#[cfg(all(windows, target_arch = "aarch64"))]
const INSTALLER_NAME_SUFFIX: &str = "-windows-arm64-setup.exe";
#[cfg(all(target_os = "linux", target_arch = "x86_64"))]
const INSTALLER_NAME_SUFFIX: &str = "-linux-x86_64.AppImage";
#[cfg(all(target_os = "linux", target_arch = "aarch64"))]
const INSTALLER_NAME_SUFFIX: &str = "-linux-aarch64.AppImage";
#[cfg(all(target_os = "macos", target_arch = "x86_64"))]
const INSTALLER_NAME_SUFFIX: &str = "-macos-x64.dmg";
#[cfg(all(target_os = "macos", target_arch = "aarch64"))]
const INSTALLER_NAME_SUFFIX: &str = "-macos-arm64.dmg";
#[cfg(not(any(
    all(windows, target_arch = "x86_64"),
    all(windows, target_arch = "aarch64"),
    all(target_os = "linux", target_arch = "x86_64"),
    all(target_os = "linux", target_arch = "aarch64"),
    all(target_os = "macos", target_arch = "x86_64"),
    all(target_os = "macos", target_arch = "aarch64"),
)))]
const INSTALLER_NAME_SUFFIX: &str = "-no-installer-for-this-platform";

fn installer_name_is_ours(name: &str) -> bool {
    name.starts_with(INSTALLER_NAME_PREFIX)
        && name.ends_with(INSTALLER_NAME_SUFFIX)
        && !name.contains("..")
        && !name.contains('/')
        && !name.contains('\\')
}

/// The app's update staging directory, the only place run_installer executes
/// from. It lives under the same user data root the Go sidecar writes to
/// (backend/internal/platform/appdata): Windows %LOCALAPPDATA%\ProviderSwitchboard,
/// elsewhere the user config dir plus provider-switchboard. The two sides must
/// agree, or the shell would refuse a file the sidecar just verified.
#[cfg(windows)]
fn update_directory() -> Result<String, String> {
    let local = std::env::var_os("LOCALAPPDATA")
        .filter(|value| !value.is_empty())
        .ok_or_else(|| "user data directory is unavailable".to_string())?;
    let update = PathBuf::from(local)
        .join("ProviderSwitchboard")
        .join("update");
    if !update.is_absolute() {
        return Err("user data directory is unavailable".to_string());
    }
    Ok(update.to_string_lossy().into_owned())
}

/// Mirrors Go's os.UserConfigDir on Linux: XDG_CONFIG_HOME wins when set,
/// otherwise ~/.config; the joined root must be absolute or the user data
/// directory is unavailable, the same words the Go side uses.
#[cfg(target_os = "linux")]
fn update_directory() -> Result<String, String> {
    let config = match std::env::var_os("XDG_CONFIG_HOME") {
        Some(value) if !value.is_empty() => PathBuf::from(value),
        _ => {
            let home = std::env::var_os("HOME")
                .filter(|value| !value.is_empty())
                .ok_or_else(|| "user data directory is unavailable".to_string())?;
            PathBuf::from(home).join(".config")
        }
    };
    let update = config.join("provider-switchboard").join("update");
    if !update.is_absolute() {
        return Err("user data directory is unavailable".to_string());
    }
    Ok(update.to_string_lossy().into_owned())
}

/// Mirrors Go's os.UserConfigDir on macOS: HOME plus
/// "Library/Application Support".
#[cfg(target_os = "macos")]
fn update_directory() -> Result<String, String> {
    let home = std::env::var_os("HOME")
        .filter(|value| !value.is_empty())
        .ok_or_else(|| "user data directory is unavailable".to_string())?;
    let update = PathBuf::from(home)
        .join("Library")
        .join("Application Support")
        .join("provider-switchboard")
        .join("update");
    if !update.is_absolute() {
        return Err("user data directory is unavailable".to_string());
    }
    Ok(update.to_string_lossy().into_owned())
}

#[cfg(not(any(windows, target_os = "linux", target_os = "macos")))]
fn update_directory() -> Result<String, String> {
    Err("self-update is not supported on this platform".to_string())
}

/// Windows: the setup is its own program; the OS takes it from here and the
/// app steps out of the way.
#[cfg(windows)]
fn launch_verified_installer(
    app: &tauri::AppHandle,
    installer: &Path,
    exit: bool,
) -> Result<(), String> {
    std::process::Command::new(installer)
        .spawn()
        .map_err(|error| format!("the installer could not start: {error}"))?;
    if exit {
        app.exit(0);
    }
    Ok(())
}

/// Linux: the app runs from an AppImage, so the update is that image. The new
/// one is staged next to the running file and swapped in with one rename on
/// the same filesystem: the running process keeps its inode, the path already
/// points at the new image. An install without $APPIMAGE (a deb, a bare
/// binary) is refused honestly; the release page is the path for it.
#[cfg(target_os = "linux")]
fn launch_verified_installer(
    app: &tauri::AppHandle,
    installer: &Path,
    exit: bool,
) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    // An AppImage knows where it runs from: the runtime exports APPIMAGE.
    let running = std::env::var_os("APPIMAGE")
        .map(PathBuf::from)
        .filter(|value| value.is_absolute())
        .ok_or_else(|| {
            "this install was not started from an AppImage; the release page is the path"
                .to_string()
        })?;
    let running = running
        .canonicalize()
        .map_err(|error| format!("the running AppImage could not be located: {error}"))?;
    let staged = replacement_sibling(&running);
    std::fs::remove_file(&staged).ok();
    std::fs::copy(installer, &staged)
        .map_err(|error| format!("the update could not be staged: {error}"))?;
    if let Err(error) = std::fs::set_permissions(&staged, std::fs::Permissions::from_mode(0o755)) {
        std::fs::remove_file(&staged).ok();
        return Err(format!("the update could not be made executable: {error}"));
    }
    if let Err(error) = std::fs::rename(&staged, &running) {
        std::fs::remove_file(&staged).ok();
        return Err(format!(
            "the running AppImage could not be replaced: {error}"
        ));
    }
    spawn_after_exit(&running)?;
    if exit {
        app.exit(0);
    }
    Ok(())
}

#[cfg(target_os = "linux")]
fn replacement_sibling(running: &Path) -> PathBuf {
    let mut sibling = running.as_os_str().to_os_string();
    sibling.push(".new");
    PathBuf::from(sibling)
}

/// The bundle the dmg carries, exactly as the release builds it.
#[cfg(target_os = "macos")]
const APP_BUNDLE_NAME: &str = "Luxury Switchboard.app";

/// macOS: the update is a dmg. It is mounted read-only at our own mount
/// point, the bundle inside is copied out with ditto (a bundle is a directory
/// tree with metadata), and the volume is detached before anything is
/// replaced, so a failed swap leaves the running app untouched and no mount
/// behind. The new bundle is staged next to the old one and swapped with a
/// rename; a failed second rename puts the old bundle back.
#[cfg(target_os = "macos")]
fn launch_verified_installer(
    app: &tauri::AppHandle,
    installer: &Path,
    update_dir: &Path,
    exit: bool,
) -> Result<(), String> {
    use std::ffi::OsStr;
    let exe = std::env::current_exe()
        .map_err(|error| format!("the running app could not be located: {error}"))?;
    // A bare binary someone ran from a dev build has no bundle to swap; the
    // dmg path is the honest answer then, not a guess.
    let bundle = app_bundle_of(&exe).ok_or_else(|| {
        "this install was not started from the app bundle; the release page is the path".to_string()
    })?;
    let install_root = bundle
        .parent()
        .ok_or_else(|| "the app bundle has no parent directory".to_string())?;

    let mount = update_dir.join("dmg-mount");
    std::fs::remove_dir_all(&mount).ok();
    std::fs::create_dir_all(&mount)
        .map_err(|error| format!("the mount point could not be prepared: {error}"))?;
    run_tool(
        "hdiutil",
        &[
            OsStr::new("attach"),
            installer.as_os_str(),
            OsStr::new("-readonly"),
            OsStr::new("-nobrowse"),
            OsStr::new("-mountpoint"),
            mount.as_os_str(),
        ],
    )
    .or_else(|error| {
        detach_mount(&mount);
        Err(error)
    })?;
    let source = mount.join(APP_BUNDLE_NAME);
    if !source.is_dir() {
        detach_mount(&mount);
        return Err(format!("the downloaded dmg carries no {APP_BUNDLE_NAME}"));
    }
    // Stage next to the running bundle so the swap is one rename on the same
    // volume: the data volume behind App Support and /Applications is not
    // guaranteed to be the one the bundle lives on.
    let staged = install_root.join(format!("{}.new", APP_BUNDLE_NAME));
    let backup = install_root.join(format!("{}.old", APP_BUNDLE_NAME));
    std::fs::remove_dir_all(&staged).ok();
    if let Err(error) = run_tool("ditto", &[source.as_os_str(), staged.as_os_str()]) {
        std::fs::remove_dir_all(&staged).ok();
        detach_mount(&mount);
        return Err(error);
    }
    detach_mount(&mount);
    std::fs::remove_dir_all(&backup).ok();
    if let Err(error) = std::fs::rename(bundle, &backup) {
        std::fs::remove_dir_all(&staged).ok();
        return Err(format!("the app bundle could not be replaced: {error}"));
    }
    if let Err(error) = std::fs::rename(&staged, bundle) {
        // The old bundle goes back where it was; the running app never
        // notices the attempt.
        std::fs::rename(&backup, bundle).ok();
        return Err(format!("the app bundle could not be replaced: {error}"));
    }
    std::fs::remove_dir_all(&backup).ok();
    spawn_after_exit(bundle)?;
    if exit {
        app.exit(0);
    }
    Ok(())
}

/// <bundle>/Contents/MacOS/<binary>: three levels up, and the middle level
/// must literally be Contents, or this is not a bundle we can swap.
#[cfg(target_os = "macos")]
fn app_bundle_of(exe: &Path) -> Option<&Path> {
    let bundle = exe.ancestors().nth(3)?;
    if exe.ancestors().nth(2)?.file_name()? != "Contents" {
        return None;
    }
    if !bundle.file_name()?.to_str()?.ends_with(".app") {
        return None;
    }
    Some(bundle)
}

#[cfg(target_os = "macos")]
fn detach_mount(mount: &Path) {
    use std::ffi::OsStr;
    // Best effort: the volume is read-only and -nobrowse, and the staged copy
    // is already complete; a mount that refuses to detach is clutter, not a
    // half-done update.
    if run_tool("hdiutil", &[OsStr::new("detach"), mount.as_os_str()]).is_ok() {
        std::fs::remove_dir_all(mount).ok();
    }
}

#[cfg(target_os = "macos")]
fn run_tool(program: &str, args: &[&std::ffi::OsStr]) -> Result<(), String> {
    let output = std::process::Command::new(program)
        .args(args)
        .output()
        .map_err(|error| format!("{program} could not run: {error}"))?;
    if !output.status.success() {
        let detail = String::from_utf8_lossy(&output.stderr).trim().to_string();
        return Err(format!("{program} failed: {detail}"));
    }
    Ok(())
}

#[cfg(not(any(windows, target_os = "linux", target_os = "macos")))]
fn launch_verified_installer(
    _app: &tauri::AppHandle,
    _installer: &Path,
    _exit: bool,
) -> Result<(), String> {
    Err("self-update is not supported on this platform".to_string())
}

/// Spawns the updated app through a shell that first waits for this process
/// to disappear: the relay listener the dying sidecar still holds has to be
/// released before the new instance asks for its port. The wait is bounded
/// (ten seconds), so a wedged shutdown still relaunches.
#[cfg(any(target_os = "linux", target_os = "macos"))]
fn spawn_after_exit(target: &Path) -> Result<(), String> {
    let pid = std::process::id();
    let action = if cfg!(target_os = "macos") {
        // open -n: Launch Services would otherwise just focus the exiting
        // instance instead of starting the new bundle.
        "open -n"
    } else {
        "exec"
    };
    let script = format!(
        "i=0; while kill -0 {pid} 2>/dev/null && [ \"$i\" -lt 100 ]; do sleep 0.1; i=$((i+1)); done; {action} \"$1\""
    );
    std::process::Command::new("/bin/sh")
        .arg("-c")
        .arg(&script)
        .arg("switchboard-relaunch")
        .arg(target)
        .spawn()
        .map_err(|error| format!("the updated app could not start: {error}"))?;
    Ok(())
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_installer_name_matches_the_release_contract() {
        let ours = format!("Luxury-Switchboard-1.0.45{INSTALLER_NAME_SUFFIX}");
        assert!(installer_name_is_ours(&ours));
    }

    #[test]
    fn names_from_other_platforms_are_refused() {
        #[cfg(windows)]
        {
            assert!(!installer_name_is_ours(
                "Luxury-Switchboard-1.0.45-linux-x86_64.AppImage"
            ));
            assert!(!installer_name_is_ours(
                "Luxury-Switchboard-1.0.45-macos-arm64.dmg"
            ));
        }
        #[cfg(target_os = "linux")]
        {
            assert!(!installer_name_is_ours(
                "Luxury-Switchboard-1.0.45-windows-x64-setup.exe"
            ));
            assert!(!installer_name_is_ours(
                "Luxury-Switchboard-1.0.45-macos-arm64.dmg"
            ));
        }
        #[cfg(target_os = "macos")]
        {
            assert!(!installer_name_is_ours(
                "Luxury-Switchboard-1.0.45-windows-x64-setup.exe"
            ));
            assert!(!installer_name_is_ours(
                "Luxury-Switchboard-1.0.45-linux-x86_64.AppImage"
            ));
        }
        // a wrong-architecture build of the same platform is equally foreign
        #[cfg(all(windows, target_arch = "x86_64"))]
        assert!(!installer_name_is_ours(
            "Luxury-Switchboard-1.0.45-windows-arm64-setup.exe"
        ));
        #[cfg(all(windows, target_arch = "aarch64"))]
        assert!(!installer_name_is_ours(
            "Luxury-Switchboard-1.0.45-windows-x64-setup.exe"
        ));
        #[cfg(all(target_os = "linux", target_arch = "x86_64"))]
        assert!(!installer_name_is_ours(
            "Luxury-Switchboard-1.0.45-linux-aarch64.AppImage"
        ));
        #[cfg(all(target_os = "linux", target_arch = "aarch64"))]
        assert!(!installer_name_is_ours(
            "Luxury-Switchboard-1.0.45-linux-x86_64.AppImage"
        ));
        #[cfg(all(target_os = "macos", target_arch = "x86_64"))]
        assert!(!installer_name_is_ours(
            "Luxury-Switchboard-1.0.45-macos-arm64.dmg"
        ));
        #[cfg(all(target_os = "macos", target_arch = "aarch64"))]
        assert!(!installer_name_is_ours(
            "Luxury-Switchboard-1.0.45-macos-x64.dmg"
        ));
    }

    #[test]
    fn path_shaped_names_are_refused() {
        let base = format!("Luxury-Switchboard-1.0.45{INSTALLER_NAME_SUFFIX}");
        assert!(!installer_name_is_ours(&format!("dir/{base}")));
        assert!(!installer_name_is_ours(&format!("dir\\{base}")));
        assert!(!installer_name_is_ours(&format!("..{base}")));
    }

    #[cfg(target_os = "linux")]
    #[test]
    fn the_replacement_sits_next_to_the_running_image() {
        let running = PathBuf::from("/home/u/Apps/Luxury-Switchboard-1.0.45-linux-x86_64.AppImage");
        assert_eq!(
            replacement_sibling(&running),
            PathBuf::from("/home/u/Apps/Luxury-Switchboard-1.0.45-linux-x86_64.AppImage.new")
        );
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn the_bundle_is_located_from_the_running_binary() {
        let exe =
            Path::new("/Applications/Luxury Switchboard.app/Contents/MacOS/switchboard-desktop");
        assert_eq!(
            app_bundle_of(exe),
            Some(Path::new("/Applications/Luxury Switchboard.app"))
        );
        // a bare binary is not an install we can swap
        assert!(app_bundle_of(Path::new("/usr/local/bin/switchboard-desktop")).is_none());
        // Contents must be the middle level, not a directory that merely ends in .app
        assert!(
            app_bundle_of(Path::new(
                "/Applications/fake.app/MacOS/switchboard-desktop"
            ))
            .is_none()
        );
    }

    #[cfg(any(windows, target_os = "linux", target_os = "macos"))]
    #[test]
    fn the_update_directory_sits_under_the_sidecars_data_root() {
        let dir = update_directory().expect("a desktop session always has a data root");
        #[cfg(windows)]
        assert!(dir.ends_with(r"\ProviderSwitchboard\update"));
        #[cfg(target_os = "linux")]
        assert!(dir.ends_with("/provider-switchboard/update"));
        #[cfg(target_os = "macos")]
        assert!(dir.ends_with("Library/Application Support/provider-switchboard/update"));
    }
}
