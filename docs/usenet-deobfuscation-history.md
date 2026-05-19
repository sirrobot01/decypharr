# Usenet deobfuscation and PAR2 work on `feat/usenet-deobfuscation`

This file records the main changes made on the branch, including the earlier obfuscation-handling work and the later PAR2-related attempts/fixes.

## Summary

The branch started by improving handling of obfuscated Usenet releases, especially cases where:

- archive parts are uploaded with meaningless names
- the real file names should be derived from the NZB name
- a release is actually RAR data disguised as 7z, or vice versa
- multi-volume archives must preserve their original NZB order to open correctly

The later PAR2 work attempted to use PAR2 `FileDesc` packets as a metadata source for deobfuscation. The final fix that made the provided Matrix sample work was not a new PAR2 parser change by itself, but a correction to how archive volume offsets and sizes were being built.

## Earlier obfuscated-file handling work before the PAR2 attempts

### `1008c7a` — add deobfuscation toggle and rename flow

This introduced the main user-facing deobfuscation feature:

- added `usenet.deobfuscate` to the config
- added `USENET__DEOBFUSCATE` environment variable support
- added `Number` to `storage.NZBFile` so file ordering can survive later parser stages
- updated parser output so media files can be renamed using the NZB name when deobfuscation is enabled
- preserved sequence ordering for extracted files by carrying the original NZB number through parser output

This laid the groundwork for handling releases where the uploaded names are junk but the NZB title is meaningful.

### `8169a3c`, `0d4e6c7`, `6a22aa7` — wire the toggle into the UI

These commits added and completed the settings-side support:

- added a `Deobfuscate Files` checkbox to the usenet settings page
- included the new flag in config collection/saving
- fixed checkbox population logic to use `input.checked`
- refreshed the built/minified config asset so the setting works in the shipped UI

### `4ca63ea` — fallback between archive parsers on signature mismatch

This improved handling for obfuscated archives whose filename extension lies about the real format:

- if something grouped as RAR fails with `unknown RAR format`, retry it as 7z
- if something grouped as 7z fails with `unexpected id`, retry it as RAR

This was important for releases where the subject/name points to one archive type while the actual bytes belong to another.

### `e7f372a` — attempt raw media fallback

This commit briefly added a fallback path that treated failed archive groups as raw media when both archive parsers failed.

That was a reasonable experiment for disguised split media uploads, but it was later removed because it was too broad and could hide archive-layout problems.

### `336b63e` — remove raw media fallback and improve ordering logic instead

This replaced the broad raw-media fallback with stronger archive handling:

- removed the raw media fallback entirely
- added logical volume ordering helpers for RAR, 7z and ZIP-style split names
- handled numeric extensions like `.001`, `.002`, `.z01`, `.r00`, etc.
- changed 7z processing to sort by logical archive order instead of plain filename order
- preserved original NZB sequence numbers in extracted parser output

This was an important step because obfuscated uploads often still need correct volume ordering even when the names are nonsense.

## PAR2-related attempts on this branch

### `9510b42` — initial PAR2 deobfuscation support

This introduced the first PAR2-based approach:

- kept PAR2 groups in parser processing instead of discarding them immediately
- parsed PAR2 `FileDesc` packets
- stored PAR2 file descriptors for later matching
- retried archive parsing after PAR2-based filename deobfuscation

### `d7e4a7d` — move PAR2 collection from `Parse()` to `Process()`

This fixed an instance-boundary issue where PAR2 data gathered during parsing was not reliably available during later processing.

### `12f1855` — guard retries and detect archive type from content

This tightened the PAR2 retry path:

- prevented infinite PAR2 retry recursion
- added archive-type detection from magic bytes after deobfuscation
- improved fallback behavior after PAR2-driven renaming

## Final working fix for the Matrix sample

### `0705222` — use per-file segment metadata for archive offsets

This is the change that fixed the specific failure seen with:

- `The.Matrix.Revolutions.2003.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.REMUX-FraMeSToR-AsRequested.nzb`

### Root cause

The parser was already fetching per-file yEnc metadata, but archive segment building still mostly used group-level heuristics derived from the first file.

For split archives with many volumes of different sizes, that caused incorrect:

- per-volume sizes
- concatenated archive offsets
- byte ranges handed to the 7z reader

That incorrect virtual archive layout caused failures such as `sevenzip: unexpected id`, which made it look like PAR2/deobfuscation was still the core problem.

### What the fix changed

- made archive segment construction use the specific `fileMeta` entry for each NZB file
- stopped reusing the first file's segment size and file size for later volumes
- added tests covering per-file segment sizing and base-segment generation
- changed PAR2 pre-processing to prefer the smallest PAR2 candidate first, since the main/index PAR2 usually has the fewest segments and recovery volumes often do not contain `FileDesc`
- stopped treating no-op PAR2 matches as a successful deobfuscation pass when filenames and detected type did not actually change

## Outcome

With the earlier obfuscation work plus the final per-file archive-offset fix:

- obfuscated split archive releases are grouped and ordered more reliably
- disguised archive types have better fallback handling
- PAR2 metadata loading is less noisy and more targeted
- the provided Matrix sample now parses successfully on this branch
