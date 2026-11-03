//! Reads every golden file named in `types.txt` as the KaraokeMachine type it
//! lists, then writes it back and compares the two, so a field the queue
//! manager leaves out or adds shows up as well as one that fails to parse.

use std::path::PathBuf;

use km_api::dto::{
    AccessDto, AccessGrantDto, AddedToQueueDto, ErrorDto, PackagesDto, QueueDto, RequestDto,
    RequestsDto, SettingsDto, SingerDto, SingerIssuedDto, SingersDto, SongDto, StateDto,
};
use km_api::{Discovery, Event};
use serde::{Serialize, de::DeserializeOwned};
use serde_json::Value;

fn main() {
    let dir: PathBuf = std::env::args()
        .nth(1)
        .unwrap_or_else(|| "queue/kmapi/testdata/golden".to_owned())
        .into();
    let list = std::fs::read_to_string(dir.join("types.txt")).expect("types.txt");
    let mut failed = 0;
    for line in list.lines().filter(|l| !l.trim().is_empty()) {
        let mut parts = line.split_whitespace();
        let (file, ty) = (parts.next().unwrap(), parts.next().unwrap());
        let text = std::fs::read_to_string(dir.join(file)).expect(file);
        let docs: Vec<&str> = if file.ends_with(".ndjson") {
            text.lines().filter(|l| !l.trim().is_empty()).collect()
        } else {
            vec![text.trim_end()]
        };
        for doc in docs {
            let result = match ty {
                "Discovery" => check::<Discovery>(doc),
                "AccessDto" => check::<AccessDto>(doc),
                "AccessGrantDto" => check::<AccessGrantDto>(doc),
                "StateDto" => check::<StateDto>(doc),
                "QueueDto" => check::<QueueDto>(doc),
                "SingersDto" => check::<SingersDto>(doc),
                "SingerDto" => check::<SingerDto>(doc),
                "SingerIssuedDto" => check::<SingerIssuedDto>(doc),
                "RequestsDto" => check::<RequestsDto>(doc),
                "RequestDto" => check::<RequestDto>(doc),
                "PackagesDto" => check::<PackagesDto>(doc),
                "SongDto" => check::<SongDto>(doc),
                "AddedToQueueDto" => check::<AddedToQueueDto>(doc),
                "SettingsDto" => check::<SettingsDto>(doc),
                "ErrorDto" => check::<ErrorDto>(doc),
                "Event" => check::<Event>(doc),
                other => Err(format!("unknown type {other}")),
            };
            match result {
                Ok(()) => {}
                Err(problem) => {
                    failed += 1;
                    println!("FAIL {file} as {ty}: {problem}");
                }
            }
        }
        if file == "discover.json" {
            let d: Discovery = serde_json::from_str(&text).expect("discovery");
            if d.app != "karaokemachine" {
                failed += 1;
                println!("FAIL discover.json: app is {:?}", d.app);
            }
        }
        println!("ok   {file} as {ty}");
    }
    if failed > 0 {
        println!("{failed} problem(s)");
        std::process::exit(1);
    }
}

fn check<T: DeserializeOwned + Serialize>(doc: &str) -> Result<(), String> {
    let typed: T = serde_json::from_str(doc).map_err(|e| e.to_string())?;
    let ours: Value = serde_json::from_str(doc).map_err(|e| e.to_string())?;
    let theirs = serde_json::to_value(&typed).map_err(|e| e.to_string())?;
    let mut diffs = Vec::new();
    compare("", &ours, &theirs, &mut diffs);
    if diffs.is_empty() {
        Ok(())
    } else {
        Err(diffs.join("; "))
    }
}

/// Differences between what the queue manager sent and what KaraokeMachine
/// writes for the same value. Floats are compared loosely, since f32 rounds.
fn compare(at: &str, ours: &Value, theirs: &Value, diffs: &mut Vec<String>) {
    match (ours, theirs) {
        (Value::Object(a), Value::Object(b)) => {
            for (k, v) in a {
                match b.get(k) {
                    Some(w) => compare(&format!("{at}.{k}"), v, w, diffs),
                    None => diffs.push(format!("{at}.{k} is not a KaraokeMachine field")),
                }
            }
            for k in b.keys() {
                if !a.contains_key(k) {
                    diffs.push(format!("{at}.{k} is missing"));
                }
            }
        }
        (Value::Array(a), Value::Array(b)) if a.len() == b.len() => {
            for (i, (v, w)) in a.iter().zip(b).enumerate() {
                compare(&format!("{at}[{i}]"), v, w, diffs);
            }
        }
        (Value::Number(a), Value::Number(b)) => {
            let (x, y) = (a.as_f64().unwrap_or(0.0), b.as_f64().unwrap_or(0.0));
            if (x - y).abs() > 1e-6 * x.abs().max(1.0) {
                diffs.push(format!("{at}: {a} became {b}"));
            }
        }
        (a, b) if a == b => {}
        (a, b) => diffs.push(format!("{at}: {a} became {b}")),
    }
}
