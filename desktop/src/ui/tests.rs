//! Headless runs of the real view: press what a person would press, then look for keyed elements.

use rune::{Env, Runtime};

use crate::app::{App, init, sample_scene};
use crate::data::{Gc, Jvm};

fn runtime(state: App) -> Runtime<App> {
    let env = Env {
        system_dark: true,
        scale: 2.0,
    };
    let mut rt = Runtime::new(state, super::view, (980.0, 640.0), env);
    rt.frame(0.0);
    rt
}

fn sample() -> Runtime<App> {
    let mut s = init();
    sample_scene(&mut s);
    runtime(s)
}

fn settle(rt: &mut Runtime<App>) {
    for _ in 0..30 {
        rt.frame(1.0 / 60.0);
    }
}

fn press(rt: &mut Runtime<App>, key: &str) {
    let (x, y) = rt
        .find(key)
        .unwrap_or_else(|| panic!("nothing keyed {key} on screen"));
    rt.pointer_down(x, y);
    rt.pointer_up(x, y);
    settle(rt);
}

fn text_of(rt: &Runtime<App>, key: &str) -> Option<String> {
    rt.elements()
        .into_iter()
        .find(|e| e.key.as_deref() == Some(key))
        .and_then(|e| e.text)
}

#[test]
fn a_sidebar_jvm_opens_its_page_and_all_jvms_goes_home() {
    let mut rt = sample();
    assert!(rt.find("jd-detail").is_none(), "the home lists findings");
    press(&mut rt, "jvm-4101");
    assert_eq!(rt.state.selected, Some(4101));
    for key in [
        "jd-header",
        "jd-tile-heap",
        "jd-tile-gc-time",
        "jd-lane-heap",
        "jd-lane-old",
        "jd-lane-gc",
        "jd-lane-threads",
        "jd-args",
    ] {
        assert!(rt.find(key).is_some(), "the detail page shows {key}");
    }
    assert_eq!(
        text_of(&rt, "jd-verdict").as_deref(),
        Some("Heap 83% full"),
        "the verdict is its worst finding"
    );
    assert!(
        rt.find("jd-tile-heap-since").is_some(),
        "a pinned baseline shows deltas"
    );
    assert!(
        rt.find("subject-4101-jvm-heap-pressure").is_some(),
        "and that JVM's findings"
    );
    assert!(
        rt.find("subject-4102-jvm-heap-pressure").is_none(),
        "but no other JVM's"
    );

    press(&mut rt, "all");
    assert_eq!(rt.state.selected, None);
    assert!(rt.find("jd-detail").is_none());
    assert!(rt.find("subject-4102-jvm-heap-pressure").is_some());
}

#[test]
fn a_cards_subject_opens_its_jvm_without_toggling_the_card() {
    let mut rt = sample();
    let expanded = rt.state.expanded.clone();
    assert!(expanded.is_some());
    press(&mut rt, "subject-4102-jvm-heap-pressure");
    assert_eq!(rt.state.selected, Some(4102));
    assert_eq!(rt.state.expanded, expanded, "the card stays as it was");
    assert!(rt.find("jd-detail").is_some());
}

#[test]
fn the_arguments_open_one_flag_per_line() {
    let mut rt = sample();
    press(&mut rt, "jvm-4101");
    assert!(rt.find("jd-arg-0").is_none(), "collapsed by default");
    rt.update(|_, tx| tx.scroll_into_view("jd-args"));
    settle(&mut rt);
    press(&mut rt, "jd-args/trigger");
    assert!(rt.state.args_open);
    let flags: Vec<Option<String>> = (0..4)
        .map(|i| text_of(&rt, &format!("jd-arg-{i}")))
        .collect();
    let want = [
        "-Xmx2g",
        "-Xms512m",
        "-XX:+UseG1GC",
        "-Dspring.profiles.active=prod",
    ];
    assert_eq!(flags, want.map(|s| Some(s.to_owned())));
    rt.update(|_, tx| tx.scroll_into_view("jd-args"));
    settle(&mut rt);
    press(&mut rt, "jd-args/trigger");
    assert!(!rt.state.args_open);
    assert!(rt.find("jd-arg-0").is_none());
}

#[test]
fn one_reading_says_its_still_collecting() {
    let mut s = init();
    let jvm = Jvm {
        pid: 7,
        main_class: "a.App".into(),
        vm_args: "-Xmx1g".into(),
        gc: Some(Gc {
            ou: 1024.0,
            ..Gc::default()
        }),
        ..Jvm::default()
    };
    s.land(Ok(vec![jvm]));
    s.selected = Some(7);
    let rt = runtime(s);
    assert!(rt.find("jd-timeline-collecting").is_some());
    assert!(rt.find("jd-lane-heap").is_none());
}
