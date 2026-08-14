//! Neofetch-style terminal layout: the OctoPort mark drawn on the left of the
//! terminal with the command's output to its right, one row per art line.
//! Long-running commands keep printing to the right of a fixed gutter once the
//! art itself has scrolled past.
//!
//! The mark is rendered as plain (black/white) Unicode braille via
//! `ascii-image-converter <octoport-dark-128.png> -b -d 40,18`, embedded below.

pub const ASCII_ART: &str = include_str!("../assets/octoport-logo.txt");

pub struct Neofetch {
    art: Vec<&'static str>,
    width: usize,
    gutter: usize,
}

impl Neofetch {
    pub fn new(art: &'static str) -> Self {
        let art: Vec<&str> = art.lines().collect();
        let width = art.iter().map(|l| l.chars().count()).max().unwrap_or(0);
        Neofetch { art, width, gutter: 3 }
    }

    /// Render the mark with `right` lines beside it, one row per art line.
    /// Fewer right lines than art rows leave the seams empty; more keep on
    /// going below the art.
    pub fn banner(&self, right: &[String]) -> String {
        let rows = right.len().max(self.art.len());
        let mut out = String::new();
        for i in 0..rows {
            let left = self.art.get(i).copied().unwrap_or("");
            let r = right.get(i).map(|s| s.as_str()).unwrap_or("");
            out.push_str(left);
            out.push_str(&" ".repeat(self.span() - left.chars().count()));
            out.push_str(r);
            out.push('\n');
        }
        out.pop();
        out
    }

    /// A blank left side of the same width, for messages printed on their own
    /// line below or beside the art.
    pub fn blank(&self) -> String {
        " ".repeat(self.span())
    }

    /// Number of columns the left mark occupies, including its trailing gutter.
    pub fn span(&self) -> usize {
        self.width + self.gutter
    }
}