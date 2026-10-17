//! A directory every path is confined under, symbolic links included: Go's `os.Root`, resolved component by
//! component (a symbolic link is followed when its target stays under the root). Unlike `os.Root`, which holds the
//! directory open and walks with `openat`, the resolution is done on paths: a concurrent rename under the root
//! between the resolution and the use is not guarded against.

use std::io;
use std::path::{Component, Path, PathBuf};

/// Symbolic links followed in one resolution at most (as Go's `os.Root`).
const MAX_SYMLINKS: usize = 255;

pub(crate) struct Root {
    dir: PathBuf,
}

fn escapes(name: &str) -> io::Error {
    io::Error::new(
        io::ErrorKind::PermissionDenied,
        format!("{name}: path escapes from parent"),
    )
}

impl Root {
    /// OpenRoot: the directory must exist.
    pub(crate) fn open(dir: &str) -> io::Result<Root> {
        let md = std::fs::metadata(dir)
            .map_err(|e| io::Error::new(e.kind(), format!("openat {dir}: {e}")))?;
        if !md.is_dir() {
            return Err(io::Error::new(
                io::ErrorKind::NotADirectory,
                format!("openat {dir}: not a directory"),
            ));
        }
        Ok(Root {
            dir: PathBuf::from(dir),
        })
    }

    /// The host path of `name` (relative to the root), every existing component resolved; the components that do
    /// not exist yet are appended as they are.
    pub(crate) fn resolve(&self, name: &str) -> io::Result<PathBuf> {
        if Path::new(name).is_absolute() {
            return Err(escapes(name));
        }
        // the components still to resolve, in reverse order
        let mut todo: Vec<String> = Vec::new();
        push_components(&mut todo, name).map_err(|()| escapes(name))?;
        let mut done: Vec<String> = Vec::new();
        let mut links = 0;
        let mut missing = false;
        while let Some(c) = todo.pop() {
            if c == ".." {
                if done.pop().is_none() {
                    return Err(escapes(name));
                }
                continue;
            }
            if missing {
                done.push(c);
                continue;
            }
            let cur = self.dir.join(done.iter().collect::<PathBuf>()).join(&c);
            match std::fs::symlink_metadata(&cur) {
                Err(e) if e.kind() == io::ErrorKind::NotFound => {
                    missing = true;
                    done.push(c);
                }
                Err(e) => return Err(e),
                Ok(md) if md.file_type().is_symlink() => {
                    links += 1;
                    if links > MAX_SYMLINKS {
                        return Err(io::Error::other(format!("{name}: too many links")));
                    }
                    let target = std::fs::read_link(&cur)?;
                    let target = target.to_string_lossy().into_owned();
                    if Path::new(&target).is_absolute() {
                        return Err(escapes(name));
                    }
                    push_components(&mut todo, &target).map_err(|()| escapes(name))?;
                }
                Ok(_) => done.push(c),
            }
        }
        Ok(self.dir.join(done.iter().collect::<PathBuf>()))
    }
}

/// Pushes the components of a relative path onto `todo` (reversed, so that the first is popped first).
fn push_components(todo: &mut Vec<String>, p: &str) -> Result<(), ()> {
    let mut comps = Vec::new();
    for c in Path::new(p).components() {
        match c {
            Component::Normal(s) => comps.push(s.to_string_lossy().into_owned()),
            Component::ParentDir => comps.push("..".to_string()),
            Component::CurDir => {}
            Component::RootDir | Component::Prefix(_) => return Err(()),
        }
    }
    todo.extend(comps.into_iter().rev());
    Ok(())
}
