use serde_json::Value;
use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixStream;
use std::process::Command;
use std::time::Duration;

/// Path of the tft-monitor control socket (same rule as the Go daemon).
fn ctl_path() -> String {
    let dir = std::env::var("XDG_RUNTIME_DIR").unwrap_or_else(|_| {
        // SAFETY-free fallback: /run/user/<uid>
        let uid = std::fs::metadata("/proc/self").map(|m| {
            use std::os::unix::fs::MetadataExt;
            m.uid()
        });
        format!("/run/user/{}", uid.unwrap_or(1000))
    });
    format!("{dir}/tft-monitor.ctl")
}

/// Send one JSON request to the daemon and return its `data` (or its error).
#[tauri::command]
fn ctl(req: Value) -> Result<Value, String> {
    let mut s = UnixStream::connect(ctl_path()).map_err(|e| format!("daemon not reachable: {e}"))?;
    s.set_read_timeout(Some(Duration::from_secs(3))).ok();
    s.set_write_timeout(Some(Duration::from_secs(3))).ok();
    let mut line = req.to_string();
    line.push('\n');
    s.write_all(line.as_bytes()).map_err(|e| e.to_string())?;
    let mut reply = String::new();
    BufReader::new(s).read_line(&mut reply).map_err(|e| e.to_string())?;
    let v: Value = serde_json::from_str(&reply).map_err(|e| e.to_string())?;
    if v["ok"].as_bool() == Some(true) {
        Ok(v.get("data").cloned().unwrap_or(Value::Null))
    } else {
        Err(v["error"].as_str().unwrap_or("daemon error").to_string())
    }
}

fn run_cmd(prog: &str, args: &[&str]) -> Result<String, String> {
    let out = Command::new(prog).args(args).output().map_err(|e| e.to_string())?;
    let mut text = String::from_utf8_lossy(&out.stdout).to_string();
    text.push_str(&String::from_utf8_lossy(&out.stderr));
    Ok(text.trim().to_string())
}

/// start | stop | restart | status for the tft-monitor user service.
#[tauri::command]
fn service(action: String) -> Result<String, String> {
    match action.as_str() {
        "start" | "stop" | "restart" => {
            let out = run_cmd("systemctl", &["--user", &action, "tft-monitor"])?;
            Ok(out)
        }
        "status" => run_cmd(
            "systemctl",
            &["--user", "show", "tft-monitor", "-p", "ActiveState,SubState,MainPID,ExecMainStartTimestamp,ExecStart", "--no-pager"],
        ),
        _ => Err("unknown action".into()),
    }
}

#[tauri::command]
fn logs(lines: Option<u32>) -> Result<String, String> {
    let n = lines.unwrap_or(150).min(1000).to_string();
    run_cmd("journalctl", &["--user", "-u", "tft-monitor", "-n", &n, "--no-pager", "-o", "short"])
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![ctl, service, logs])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
