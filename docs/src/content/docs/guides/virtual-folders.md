---
title: Virtual Folders
description: Organize your mounted media into custom folders.
---

Virtual folders let you create extra folders in your Decypharr mount without moving or copying anything.

For example, you can add a folder named `4K Movies`. When you open it, Decypharr only shows items that match the filters
you chose. The same items still remain available in `__all__`, `torrents`, `nzbs`, and provider folders.

## When to Use Them

Use virtual folders when you want your mount to be easier to browse:

- Put 4K releases in a `4K` folder
- Keep recent items in a `Recently Added` folder
- Separate large files from smaller files
- Group items by words in the release name, such as `Movie`, `Season`, `1080p`, or `2160p`

Virtual folders are only views. Deleting a virtual folder removes the view, not the media itself.

## Add a Virtual Folder

1. Open Decypharr.
2. Go to **Settings**.
3. Open the main configuration tab.
4. Find **Virtual Folders**.
5. Click **Add Virtual Folder**.
6. Enter a folder name, such as `4K` or `Recently Added`.
7. Add one or more filters.
8. Save your settings.

After saving, open your mount path. The new folder appears at the top level of the mount.

Example:

```text
/mnt/decypharr/
  __all__/
  __bad__/
  torrents/
  nzbs/
  4K/
  Recently Added/
```

## Simple Filter Examples

Each filter has a type and a value.

| Folder you want                          | Filter type     | Value    |
|------------------------------------------|-----------------|----------|
| Items with `2160p` in the name           | `include`       | `2160p`  |
| Items that do not contain `sample`       | `exclude`       | `sample` |
| Items added in the last 7 days           | `last_added`    | `7d`     |
| Items larger than 20 GB                  | `size_gt`       | `20GB`   |
| Items smaller than 5 GB                  | `size_lt`       | `5GB`    |
| Items with more than 5 files             | `file_count_gt` | `5`      |
| Items with file names matching a pattern | `files_regex`   | `S01E`   |

Most users should start with `include`, `exclude`, `last_added`, `size_gt`, or `size_lt`.

## How Filters Work

If you add more than one filter to a folder, Decypharr only shows items that match the filters.

Example `4K Movies` folder:

| Filter type | Value    |
|-------------|----------|
| `include`   | `2160p`  |
| `exclude`   | `sample` |

This folder shows items with `2160p` in the name and hides items with `sample` in the name.

## Useful Filter Types

| Filter type     | What it checks                                                |
|-----------------|---------------------------------------------------------------|
| `include`       | The item name contains this text                              |
| `exclude`       | The item name does not contain this text                      |
| `starts_with`   | The item name starts with this text                           |
| `ends_with`     | The item name ends with this text                             |
| `exact_match`   | The item name exactly matches this text                       |
| `regex`         | The item name matches a regular expression                    |
| `files_regex`   | One of the files inside the item matches a regular expression |
| `size_gt`       | The item is larger than this size                             |
| `size_lt`       | The item is smaller than this size                            |
| `last_added`    | The item was added within this time period                    |
| `file_count_gt` | The item has more files than this number                      |
| `file_count_lt` | The item has fewer files than this number                     |

Sizes can use `KB`, `MB`, or `GB`, such as `700MB` or `10GB`.

Time periods can use `h`, `d`, or `w`, such as `12h`, `3d`, or `2w`.

## If You Edit config.json

You can also create virtual folders directly in `config.json`:

```json
{
  "custom_folders": {
    "4K": {
      "filters": {
        "include": "2160p",
        "exclude": "sample"
      }
    },
    "Recently Added": {
      "filters": {
        "last_added": "7d"
      }
    }
  }
}
```

Restart Decypharr after changing the file manually.

## Things to Know

- A virtual folder can show the same item as another folder.
- Virtual folders do not change where files are stored.
- An empty virtual folder usually means no current item matches the filters.
- A virtual folder with no filters will show all items.
- Filter text is case-sensitive, so `2160p` and `2160P` are different.
