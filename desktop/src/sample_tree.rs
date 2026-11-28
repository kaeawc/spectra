//! Sample scenes of the process list as a tree.

use crate::app::App;
use crate::sample::all_scene;
use crate::tree::View;

/// Every process as a tree, with iTerm, a shell running an agent, Slack and Code opened.
pub fn tree_scene(s: &mut App) {
    all_scene(s);
    s.set_view(View::Tree);
    for pid in [1000, 1101, 900, 920, 921] {
        s.toggle_tree(pid);
    }
    s.table.select("p:1101");
}

/// A search in the tree: each `zsh` with the terminals that started it.
pub fn tree_search_scene(s: &mut App) {
    all_scene(s);
    s.set_view(View::Tree);
    s.query = rune::TextEdit::new("zsh");
    s.rebuild();
}
