---
title: Virtual Folders
description: Create accessible, filtered views of your mounted media library.
---

Virtual folders are filtered views of your Decypharr library. They do not move or copy media, and the same item can appear in more than one view.

For example, a virtual folder named `4K Movies` can show items whose names contain `2160p`. Those items also remain available in `__all__`, `torrents`, `nzbs`, and provider folders.

## Create a Virtual Folder

1. Open **Settings**.
2. Under **General**, open **Virtual Folders**.
3. Select **Add virtual folder**.
4. Enter the name that should appear in Browse, mounts, and shares.
5. Choose whether an item must match **all conditions** or **any condition**.
6. Choose a **Quick example**, or add a condition using the **What to check**, **Rule**, and **Value** controls.
7. Select **Preview matches** to check the match count and some example items.
8. Save the settings.

Changes are applied without restarting Decypharr. The folder appears at the top level:

```text
/mnt/decypharr/
  __all__/
  __bad__/
  torrents/
  nzbs/
  4K Movies/
  Recently Added/
```

Folder names cannot duplicate another virtual folder, a built-in folder, `version.txt`, or a provider folder. Decypharr also rejects names and characters that are unsafe on common filesystems, mounts, or SMB clients.

## Quick Examples

Each virtual-folder card lists common examples above its conditions. Select one to fill an unused blank condition or add a new one:

- **Episode files** looks for a file name such as `S01E02` or `1x02`.
- **Likely movie** looks for items without those episode-style file names.
- **More than one file** looks for items containing at least two files.
- **4K items** looks for `2160p` in the item name.
- **Added this week** uses a seven-day window.
- **Torrent only** limits the view to torrent sources.

Examples only populate the controls. You can edit every field before saving, and **Preview matches** shows how the rule behaves with your library. Episode and movie detection is a filename-based estimate rather than media metadata, so review the preview before relying on it.

## Example Conditions

| View you want | What to check | Rule | Value |
|---|---|---|---|
| Items with `2160p` in the name | Item name | contains | `2160p` |
| Items without sample files | File name inside item | does not contain | `sample` |
| Items added in the last week | Date added | is within the last | `7d` |
| Items larger than 20 GB | Total item size | is larger than | `20GB` |
| Items with more than five files | Number of files | is more than | `5` |
| Torrent items only | Source type | is | Torrent |
| Items on one provider | Provider | is | Select the provider |
| Items assigned by Radarr | Category | contains | `radarr` |

Text conditions ignore capitalization by default, so `2160p` also matches `2160P`. Enable **Match capitalization exactly** on an individual condition when capitalization matters.

Regular-expression rules are available for advanced matching. Invalid expressions are rejected before saving and cannot crash Decypharr.

## All Conditions and Any Condition

Use **All conditions** when every rule must pass. For a `Recent 4K` view, add:

| What to check | Rule | Value |
|---|---|---|
| Item name | contains | `2160p` |
| Date added | is within the last | `7d` |

Use **Any condition** when one matching rule is enough. The choice is always explicit; there are no special hidden combinations between name and file-name rules.

## Empty and Unhealthy Views

A virtual folder with no conditions shows every healthy item. This can be useful as a starting point, but preview it before saving.

Unhealthy items are excluded by default. Enable **Include unhealthy items** on a folder if it should also include entries normally shown under `__bad__`.

An empty virtual folder means no current item matches its conditions. Browse provides a link back to the editor so you can adjust and preview the rules.

## Deleting Views and Items

Removing a virtual folder in Settings removes only the filtered view. It never deletes media.

Deleting an item while browsing inside a virtual folder is different: it deletes the original entry from every library and virtual folder and removes its provider placement. Decypharr shows this consequence in the confirmation dialog.

## Configure in JSON

The visual editor is recommended, but the canonical `config.json` representation is also available:

```json
{
  "virtual_folders": [
    {
      "name": "4K Movies",
      "match": "all",
      "conditions": [
        {
          "field": "entry_name",
          "operator": "contains",
          "value": "2160p"
        },
        {
          "field": "file_name",
          "operator": "not_contains",
          "value": "sample"
        }
      ]
    },
    {
      "name": "Recently Added",
      "match": "all",
      "conditions": [
        {
          "field": "added",
          "operator": "within_last",
          "value": "7d"
        }
      ]
    }
  ]
}
```

Existing `custom_folders` configurations are read automatically. They are converted to the ordered `virtual_folders` format the next time settings are saved. Existing text matching remains case-sensitive after migration unless you change that option in the editor.
